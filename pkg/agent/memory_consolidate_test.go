package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// fakeProvider returns a fixed consolidated body and records the prompt it saw.
type fakeProvider struct {
	sawPrompt string
	reply     string
}

func (f *fakeProvider) Chat(_ context.Context, msgs []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	if len(msgs) > 0 {
		f.sawPrompt = msgs[len(msgs)-1].Content
	}
	return &providers.LLMResponse{Content: f.reply}, nil
}
func (f *fakeProvider) GetDefaultModel() string    { return "" }
func (f *fakeProvider) SupportsNativeSearch() bool { return false }

func TestConsolidateLongTerm_UnderThreshold_NoOp(t *testing.T) {
	ms := NewMemoryStore(t.TempDir())
	_ = ms.WriteLongTerm("- kid loves dinosaurs\n")
	fp := &fakeProvider{reply: "SHOULD NOT BE USED"}
	changed, err := ms.ConsolidateLongTerm(context.Background(), fp, "m", 10_000)
	if err != nil || changed {
		t.Fatalf("under threshold must no-op; changed=%v err=%v", changed, err)
	}
	if fp.sawPrompt != "" {
		t.Fatalf("provider must not be called under threshold")
	}
}

func TestConsolidateLongTerm_OverThreshold_RewritesSmaller(t *testing.T) {
	ms := NewMemoryStore(t.TempDir())
	big := strings.Repeat("- kid asked about T-rex again\n", 200) // long, dupey
	_ = ms.WriteLongTerm(big)
	fp := &fakeProvider{reply: "- Kid loves dinosaurs, especially T-rex.\n"}

	changed, err := ms.ConsolidateLongTerm(context.Background(), fp, "m", 100)
	if err != nil || !changed {
		t.Fatalf("over threshold must consolidate; changed=%v err=%v", changed, err)
	}
	got := ms.ReadLongTerm()
	if !strings.Contains(got, "T-rex") {
		t.Errorf("consolidated memory lost the fact: %q", got)
	}
	if len(got) >= len(big) {
		t.Errorf("consolidation did not shrink: %d -> %d", len(big), len(got))
	}
	if !strings.Contains(fp.sawPrompt, "T-rex") {
		t.Errorf("provider prompt should include existing memory to compress")
	}
}

func TestConsolidateLongTerm_EmptyReply_KeepsOriginal(t *testing.T) {
	ms := NewMemoryStore(t.TempDir())
	orig := strings.Repeat("- fact line\n", 200)
	_ = ms.WriteLongTerm(orig)
	fp := &fakeProvider{reply: "   "} // empty/whitespace
	changed, err := ms.ConsolidateLongTerm(context.Background(), fp, "m", 100)
	if err != nil || changed {
		t.Fatalf("empty LLM reply must NOT overwrite memory; changed=%v err=%v", changed, err)
	}
	if ms.ReadLongTerm() != orig {
		t.Fatalf("memory must be unchanged on empty reply")
	}
}
