package livekit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

type echoTool struct{ name string }

func (e echoTool) Name() string        { return e.name }
func (e echoTool) Description() string { return "echo" }
func (e echoTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"type": "string"}}, "required": []string{}}
}
func (e echoTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	v, _ := args["v"].(string)
	return tools.NewToolResult(e.name + ":" + v)
}

func TestRememberChildFactAppendsToMemoryFile(t *testing.T) {
	ws := t.TempDir()
	tool := NewRememberChildFactTool(ws)
	res := tool.Execute(context.Background(), map[string]any{"fact": "has a dog named Harry"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "has a dog named Harry") {
		t.Errorf("fact not written: %s", raw)
	}
	if res := tool.Execute(context.Background(), map[string]any{"fact": ""}); !res.IsError {
		t.Error("empty fact must be rejected")
	}
}

func TestBuildGPTLiveToolsCopiesSharedAndAddsQuiz(t *testing.T) {
	base := tools.NewToolRegistry()
	base.Register(echoTool{"get_time_date"})
	base.Register(echoTool{"exec"}) // never offered
	quiz := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir()})
	reg := BuildGPTLiveTools(base, t.TempDir(), quiz)
	var names []string
	for _, d := range reg.ToProviderDefs() {
		names = append(names, d.Function.Name)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"get_time_date", "remember_child_fact", "quiz_status", "quiz_score_answer", "quiz_record_wonder"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "exec") {
		t.Errorf("exec must never be offered: %s", got)
	}
	defs := GPTLiveToolDefs(reg, true)
	if defs[len(defs)-1]["type"] != "web_search" {
		t.Errorf("hosted web search must be appended last: %v", defs[len(defs)-1])
	}
}

func TestRegistryExecutorReturnsForLLMAndErrorFlag(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(echoTool{"get_time_date"})
	ex := NewRegistryExecutor(reg, "session-1")
	out, isErr := ex.Execute(context.Background(), "get_time_date", map[string]any{"v": "now"})
	if isErr || out != "get_time_date:now" {
		t.Errorf("got %q err=%v", out, isErr)
	}
	if _, isErr := ex.Execute(context.Background(), "nope", nil); !isErr {
		t.Error("unknown tool must be an error")
	}
}
