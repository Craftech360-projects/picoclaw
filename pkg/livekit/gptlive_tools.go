package livekit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// gptLiveSharedTools are copied from the worker's registry when present, for
// every character's GPT-Live backend model. exec, write_file, list_dir and
// web_fetch are deliberately absent from this release: a backend model
// talking directly to a child gets no shell, no arbitrary filesystem access,
// no directory listing and no outbound fetch. remember_child_fact (below) is
// the only write path offered, and it writes to one fixed, tool-internal
// path rather than anything the model names.
var gptLiveSharedTools = []string{"get_time_date", "get_weather", "read_file"}

const (
	// maxChildFactBytes bounds a single remembered fact to roughly one short
	// sentence, so one call cannot dump an essay - or an adversarial wall of
	// text - into MEMORY.md. Rejected outright rather than truncated: a
	// truncated fact is still an unvalidated, silently-altered write.
	maxChildFactBytes = 280

	// maxChildMemoryBytes bounds the whole file. This mirrors the cap
	// persistSummaryToMemoryFile (post_session_persistence.go) already
	// applies to the same MEMORY.md: once full, the oldest entries roll off
	// instead of the file growing without limit across a long-lived
	// workspace or a chatty/adversarial session.
	maxChildMemoryBytes = 64 * 1024
)

// rememberChildFactTool is the only write path offered to a GPT-Live backend
// model. Unlike write_file, it takes no path from the model at all: the
// destination is fixed to <workspace>/memory/MEMORY.md, so there is no
// path-traversal surface here to validate in the first place. What it does
// validate is the fact content itself, since that is the part a model
// talking to a child actually controls.
type rememberChildFactTool struct {
	workspace string

	// mu serializes concurrent calls to this one tool instance so a
	// read-modify-write on MEMORY.md can't interleave with itself. It is
	// only ever held around local file I/O, never across a callback or any
	// call into other code.
	mu sync.Mutex
}

// NewRememberChildFactTool builds that tool for one session's workspace.
func NewRememberChildFactTool(workspace string) tools.Tool {
	return &rememberChildFactTool{workspace: strings.TrimSpace(workspace)}
}

func (t *rememberChildFactTool) Name() string { return "remember_child_fact" }

func (t *rememberChildFactTool) Description() string {
	return "Remember one durable fact about the child for future sessions: a pet's name, a favourite, a family member. One short sentence per call."
}

func (t *rememberChildFactTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fact": map[string]any{
				"type":        "string",
				"description": "The fact, as one short sentence.",
			},
		},
		"required": []string{"fact"},
	}
}

func (t *rememberChildFactTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	if t.workspace == "" {
		return tools.ErrorResult("workspace is not defined")
	}

	// Validate at the trust boundary: args come from a model that is talking
	// to a child. A wrong type is rejected outright with an explicit error
	// rather than coerced (e.g. via fmt.Sprint, which would happily
	// stringify a map, a number or a nil into something written to disk).
	rawFact, ok := args["fact"].(string)
	if !ok {
		return tools.ErrorResult("fact must be a string")
	}

	fact := sanitizeChildFact(rawFact)
	if fact == "" {
		return tools.ErrorResult("fact is required")
	}
	if len(fact) > maxChildFactBytes {
		return tools.ErrorResult(fmt.Sprintf(
			"fact is too long (%d bytes, max %d); say it in one short sentence", len(fact), maxChildFactBytes,
		))
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	dir := filepath.Join(t.workspace, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tools.ErrorResult(err.Error())
	}
	path := filepath.Join(dir, "MEMORY.md")

	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return tools.ErrorResult(err.Error())
	}

	var sb strings.Builder
	if strings.TrimSpace(existing) == "" {
		sb.WriteString("# Memory\n\n")
	} else {
		sb.WriteString(strings.TrimRight(existing, "\n"))
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "- %s: %s\n", time.Now().Format("2006-01-02"), fact)

	output := sb.String()
	if len(output) > maxChildMemoryBytes {
		// Keep the tail (the most recent entries) and re-attach the header,
		// the same rotation persistSummaryToMemoryFile uses for this file.
		output = output[len(output)-maxChildMemoryBytes:]
		output = "# Memory\n\n" + strings.TrimLeft(output, "\n")
	}

	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.SilentResult("remembered: " + fact)
}

// sanitizeChildFact collapses newlines, tabs and other control characters to
// single spaces and trims the result. This is what keeps the write's shape
// fixed: a fact can never inject a fake "- " bullet line, a fake date, or any
// other Markdown structure into MEMORY.md - whatever the model sends becomes
// exactly one bullet, in the one format this tool writes.
func sanitizeChildFact(raw string) string {
	var b strings.Builder
	lastWasSpace := true
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		if r == ' ' {
			if lastWasSpace {
				continue
			}
			lastWasSpace = true
			b.WriteRune(' ')
			continue
		}
		lastWasSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// BuildGPTLiveTools assembles the registry offered to one character's
// GPT-Live backend model for one session: the shared read-only tools it
// inherits from the base agent registry, the memory tool, and that
// character's quiz tools. The base registry is never mutated and never
// reused across sessions - a fresh *tools.ToolRegistry is built every call,
// so one room's tools (and its workspace-scoped remember_child_fact) can
// never leak into another's.
func BuildGPTLiveTools(base *tools.ToolRegistry, workspace string, quiz *QuizTracker) *tools.ToolRegistry {
	reg := tools.NewToolRegistry()
	if base != nil {
		for _, name := range gptLiveSharedTools {
			if tool, ok := base.Get(name); ok {
				reg.Register(tool)
			}
		}
	}
	reg.Register(NewRememberChildFactTool(workspace))
	if quiz != nil {
		for _, tool := range quiz.Tools() {
			reg.Register(tool)
		}
	}
	return reg
}

// GPTLiveToolDefs renders the registry into the flat function-tool shape the
// Responses backend expects, with the hosted web_search tool appended last
// when the character has it enabled.
func GPTLiveToolDefs(reg *tools.ToolRegistry, webSearch bool) []map[string]any {
	defs := gptlive.FunctionTools(reg.ToProviderDefs())
	if webSearch {
		defs = append(defs, gptlive.HostedWebSearch())
	}
	return defs
}

// registryExecutor adapts a *tools.ToolRegistry, scoped to one session, to
// the gptlive.ToolExecutor interface the backend session calls into for
// every function call the model makes.
type registryExecutor struct {
	reg        *tools.ToolRegistry
	sessionKey string
}

// NewRegistryExecutor builds that adapter. sessionKey is per-session (the
// registry itself already comes from BuildGPTLiveTools scoped to one
// session), so it is only ever used as the chatID passed through to the
// registry for logging/context - never stored anywhere shared.
func NewRegistryExecutor(reg *tools.ToolRegistry, sessionKey string) gptlive.ToolExecutor {
	return registryExecutor{reg: reg, sessionKey: sessionKey}
}

func (r registryExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, bool) {
	res := r.reg.ExecuteWithContext(ctx, name, args, "livekit", r.sessionKey, nil)
	if res == nil {
		return "tool returned nothing", true
	}
	return res.ForLLM, res.IsError
}
