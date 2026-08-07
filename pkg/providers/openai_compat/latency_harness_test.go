//go:build integration

package openai_compat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Diagnostic harness for the 14s greeting TTFT reported 2026-08-06.
//
// It sends the real persona through the real request builder and measures time
// to first streamed token, labelling every sample with the upstream OpenRouter
// routed it to. Driving a full voice session per sample was the alternative;
// this isolates the LLM call and runs in seconds instead of minutes.
//
// Run:
//
//	OPENROUTER_API_KEY=... go test -tags integration -run TestLatencyHarness \
//	  -v -timeout 30m ./pkg/providers/openai_compat/
//
// Env knobs: LATENCY_FIXTURE (persona json), LATENCY_ITERATIONS (default 5),
// LATENCY_CHARACTER (default riddle_master), LATENCY_MODEL.

type persona struct {
	AgentName      string `json:"agent_name"`
	SystemPrompt   string `json:"system_prompt"`
	Soul           string `json:"soul"`
	GreetingPrompt string `json:"greeting_prompt"`
}

// routedProviderRe matches the line parseStreamResponse logs per stream.
var routedProviderRe = regexp.MustCompile(`routed to upstream provider "([^"]+)"`)

// logTap captures the provider's log output so each sample can be attributed to
// an upstream. The provider logs it rather than returning it.
type logTap struct {
	mu    sync.Mutex
	lines []string
}

func (l *logTap) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, string(p))
	return len(p), nil
}

func (l *logTap) lastProvider() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.lines) - 1; i >= 0; i-- {
		if m := routedProviderRe.FindStringSubmatch(l.lines[i]); m != nil {
			return m[1]
		}
	}
	return "unknown"
}

func (l *logTap) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

// harnessTools mirrors the tools=7 seen on the slow request. Only the shape
// matters here: they inflate the prefix identically regardless of contents.
func harnessTools() []ToolDefinition {
	names := []string{
		"web_search", "get_time", "set_reminder", "play_music",
		"remember_fact", "recall_fact", "end_session",
	}
	out := make([]ToolDefinition, 0, len(names))
	for _, n := range names {
		out = append(out, ToolDefinition{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name:        n,
				Description: "Tool " + n + " for the voice agent.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string", "description": "input"},
					},
					"required": []string{"query"},
				},
			},
		})
	}
	return out
}

func loadPersona(t *testing.T) persona {
	t.Helper()
	path := os.Getenv("LATENCY_FIXTURE")
	if path == "" {
		t.Skip("LATENCY_FIXTURE is not set; skipping latency harness")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var all map[string]persona
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	key := os.Getenv("LATENCY_CHARACTER")
	if key == "" {
		key = "riddle_master"
	}
	p, ok := all[key]
	if !ok {
		t.Fatalf("character %q not in fixture (have %v)", key, keysOf(all))
	}
	return p
}

func keysOf(m map[string]persona) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type sample struct {
	ttftMS    int64
	totalMS   int64
	upstream  string
	err       error
	promptLen int
}

func percentileMS(samples []sample, p float64) int64 {
	ok := make([]int64, 0, len(samples))
	for _, s := range samples {
		if s.err == nil {
			ok = append(ok, s.ttftMS)
		}
	}
	if len(ok) == 0 {
		return -1
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i] < ok[j] })
	idx := int(p * float64(len(ok)-1))
	return ok[idx]
}

// TestLatencyHarness compares routing configurations against the real persona.
// It is a measurement tool, not an assertion: it reports, and only fails if
// every single request errored (which means the harness itself is broken).
func TestLatencyHarness(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set; skipping latency harness")
	}
	p := loadPersona(t)

	model := strings.TrimSpace(os.Getenv("LATENCY_MODEL"))
	if model == "" {
		model = "google/gemma-4-31b-it"
	}
	iterations := 5
	if v := os.Getenv("LATENCY_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			iterations = n
		}
	}

	systemPrompt := p.SystemPrompt + "\n\n" + p.Soul
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: p.GreetingPrompt},
	}
	promptLen := len(systemPrompt) + len(p.GreetingPrompt)
	tools := harnessTools()

	configs := []struct {
		name      string
		env       string         // OPENROUTER_PROVIDER_ORDER
		extraBody map[string]any // overrides the built provider block
	}{
		{name: "baseline_unpinned", env: ""},
		{name: "pinned_deepinfra", env: "DeepInfra"},
		{name: "sort_latency", env: "", extraBody: map[string]any{
			"provider": map[string]any{"sort": "latency", "allow_fallbacks": true},
		}},
		{name: "sort_throughput", env: "", extraBody: map[string]any{
			"provider": map[string]any{"sort": "throughput", "allow_fallbacks": true},
		}},
	}

	tap := &logTap{}
	origOut := log.Writer()
	log.SetOutput(tap)
	defer log.SetOutput(origOut)

	results := map[string][]sample{}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			t.Setenv("OPENROUTER_PROVIDER_ORDER", cfg.env)

			opts := []Option{}
			if cfg.extraBody != nil {
				opts = append(opts, WithExtraBody(cfg.extraBody))
			}
			provider := NewProvider(apiKey, "https://openrouter.ai/api/v1", "", opts...)

			for i := 0; i < iterations; i++ {
				tap.reset()
				var ttft time.Duration
				var once sync.Once

				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				start := time.Now()
				_, err := provider.ChatStream(ctx, messages, tools, model, map[string]any{
					"max_tokens":  420,
					"temperature": 0.7,
				}, func(string) {
					once.Do(func() { ttft = time.Since(start) })
				})
				total := time.Since(start)
				cancel()

				s := sample{
					ttftMS:    ttft.Milliseconds(),
					totalMS:   total.Milliseconds(),
					upstream:  tap.lastProvider(),
					err:       err,
					promptLen: promptLen,
				}
				results[cfg.name] = append(results[cfg.name], s)

				if err != nil {
					t.Logf("  run %d: ERROR after %dms: %v", i+1, total.Milliseconds(), err)
					continue
				}
				t.Logf("  run %d: ttft=%5dms total=%6dms upstream=%s",
					i+1, s.ttftMS, s.totalMS, s.upstream)
			}
		})
	}

	// Report
	fmt.Printf("\n=== LATENCY HARNESS ===\nmodel=%s prompt_chars=%d tools=%d iterations=%d\n\n",
		model, promptLen, len(tools), iterations)
	fmt.Printf("%-20s %8s %8s %8s %8s  %s\n", "config", "median", "p90", "worst", "errors", "upstreams")
	anySuccess := false
	for _, cfg := range configs {
		ss := results[cfg.name]
		if len(ss) == 0 {
			continue
		}
		upstreams := map[string]int{}
		errs := 0
		var worst int64
		for _, s := range ss {
			if s.err != nil {
				errs++
				continue
			}
			anySuccess = true
			upstreams[s.upstream]++
			if s.ttftMS > worst {
				worst = s.ttftMS
			}
		}
		names := make([]string, 0, len(upstreams))
		for u, c := range upstreams {
			names = append(names, fmt.Sprintf("%s×%d", u, c))
		}
		sort.Strings(names)
		fmt.Printf("%-20s %8d %8d %8d %8d  %s\n",
			cfg.name, percentileMS(ss, 0.5), percentileMS(ss, 0.9), worst, errs,
			strings.Join(names, " "))
	}
	fmt.Println()

	if !anySuccess {
		t.Fatal("every request failed; harness is broken, not the routing")
	}
}
