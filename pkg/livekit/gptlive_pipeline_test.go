package livekit

import (
	"testing"

	"github.com/livekit/media-sdk"
	"github.com/sipeed/picoclaw/pkg/gptlive"
)

func TestPCM16SampleToBytesIsLittleEndian(t *testing.T) {
	got := pcm16ToBytes(media.PCM16Sample{1, -2})
	want := []byte{0x01, 0x00, 0xFE, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got % x want % x", got, want)
	}
	back := bytesToPCM16(got)
	if len(back) != 2 || back[0] != 1 || back[1] != -2 {
		t.Errorf("round trip: %v", back)
	}
}

func TestPipelineRecordsFinalTranscriptsOnly(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hel", Final: false})
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hello", Final: true})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "Hi ", Text: "Hi "})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "there", Text: "Hi there"})
	p.onBurstClose()
	snap := p.TranscriptSnapshot()
	// The brief's original test named this constant chatTypeAgent; agent_bridge.go
	// already defines chatTypeAssistant with the same meaning (and the same value
	// the cascade's own TranscriptSnapshot uses), so this asserts against that
	// existing constant instead of introducing a duplicate name for it.
	if len(snap) != 2 || snap[0].Content != "hello" || snap[0].ChatType != chatTypeUser || snap[1].Content != "Hi there" || snap[1].ChatType != chatTypeAssistant {
		t.Errorf("snapshot: %+v", snap)
	}
}

func TestPipelineIgnoresInterimUserTranscriptInSnapshot(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "not yet done", Final: false})
	if snap := p.TranscriptSnapshot(); len(snap) != 0 {
		t.Errorf("interim transcript must not be recorded: %+v", snap)
	}
}

func TestPipelineVoiceSecondsTracksLatestUsageNotSum(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.VoiceUsage{Seconds: 5})
	p.onEvent(gptlive.VoiceUsage{Seconds: 12})
	// VoiceUsage.Seconds is the session's running total to date, not a delta —
	// summing successive updates would overcount, so the latest value must win.
	if got := p.VoiceSeconds(); got != 12 {
		t.Errorf("VoiceSeconds() = %v, want 12 (latest, not summed)", got)
	}
	p.onEvent(gptlive.Closed{Reason: "close_requested", VoiceSeconds: 20})
	if got := p.VoiceSeconds(); got != 20 {
		t.Errorf("VoiceSeconds() after Closed = %v, want 20", got)
	}
}

func TestPipelineBackendTokensSumsAcrossResponses(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.BackendUsage{Total: 100})
	p.onEvent(gptlive.BackendUsage{Total: 50})
	// BackendUsage.Total is per completed backend response (one per delegation),
	// so the session total is the sum across every response.
	if got := p.BackendTokens(); got != 150 {
		t.Errorf("BackendTokens() = %v, want 150 (summed across responses)", got)
	}
}
