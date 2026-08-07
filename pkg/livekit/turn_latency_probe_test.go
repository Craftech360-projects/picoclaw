package livekit

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// The dispatch probe is the only thing separating worker-side request assembly
// from provider time inside llm_first_token_ms; if the plumbing breaks, both
// numbers silently read zero and the split looks like "not instrumented".
func TestDispatchProbeSplitsFirstTokenLatency(t *testing.T) {
	var ap *AudioPipeline
	start := time.Now()
	meta := &turnLatencyMeta{TurnID: 1, Session: "s", Path: "greeting", LLMStart: start}
	turn := ap.attachTurnLatencyMeta(voiceTurn{ctx: context.Background()}, meta)

	time.Sleep(20 * time.Millisecond)
	protocoltypes.ReportDispatch(turn.ctx)
	// A second dispatch (tool-loop iteration) must not move the boundary.
	time.Sleep(10 * time.Millisecond)
	protocoltypes.ReportDispatch(turn.ctx)

	ap.logTurnLatency(meta, "llm_first_token", 500*time.Millisecond, nil)

	if meta.LLMRequestBuildMS < 15 || meta.LLMRequestBuildMS > 60 {
		t.Fatalf("request build time not measured from first dispatch: %d ms", meta.LLMRequestBuildMS)
	}
	if got := meta.LLMRequestBuildMS + meta.LLMProviderTTFTMS; got != meta.LLMFirstTokenMS {
		t.Fatalf("split does not sum to first token: %d + %d != %d",
			meta.LLMRequestBuildMS, meta.LLMProviderTTFTMS, meta.LLMFirstTokenMS)
	}
}

func TestFirstTokenSplitAbsentWithoutProbe(t *testing.T) {
	var ap *AudioPipeline
	meta := &turnLatencyMeta{LLMStart: time.Now()}
	ap.logTurnLatency(meta, "llm_first_token", 500*time.Millisecond, nil)

	if meta.LLMRequestBuildMS != 0 || meta.LLMProviderTTFTMS != 0 {
		t.Fatalf("uninstrumented provider must report no split, got %d/%d",
			meta.LLMRequestBuildMS, meta.LLMProviderTTFTMS)
	}
}
