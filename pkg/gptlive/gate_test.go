package gptlive

import (
	"math"
	"testing"
	"time"
)

func tone(amplitude float64, samples int) []byte {
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(amplitude * 32767 * math.Sin(float64(i)*2*math.Pi*440/16000))
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}

const frame = 20 * time.Millisecond // 320 samples at 16 kHz

func TestGateOpensOnSpeechAfterLearningSilence(t *testing.T) {
	g := NewAdaptiveNoiseGate()
	quiet := tone(0.0005, 320)
	for i := 0; i < 50; i++ { // 1 s of near-silence teaches the floor
		if g.Update(quiet, frame) {
			t.Fatalf("gate opened on silence at frame %d", i)
		}
	}
	if !g.Update(tone(0.3, 320), frame) {
		t.Fatal("gate must open on speech well above the floor")
	}
	for i := 0; i < 24; i++ { // 480 ms quiet: still open (min silence 500 ms)
		if !g.Update(quiet, frame) {
			t.Fatalf("gate closed too early at %d ms", (i+1)*20)
		}
	}
	if g.Update(quiet, frame) { // 500 ms reached
		t.Fatal("gate must close after 500 ms below the floor")
	}
}

// TestGateColdStartOpensPromptlyOnLoudAudio covers the session-start greeting
// case: the model starts speaking with no prior silence to learn a floor
// from at all. The gate must not be stuck comparing the first frame against
// itself (which can never exceed any ratio of its own level).
func TestGateColdStartOpensPromptlyOnLoudAudio(t *testing.T) {
	g := NewAdaptiveNoiseGate()
	if !g.Update(tone(0.3, 320), frame) {
		t.Fatal("gate must open on loud audio from the very first frame, with no prior silence to learn a floor from")
	}
}

// TestGateBurstDoesNotContaminateLearnedFloor covers the regression this
// bug fix targets: the frame that opens the gate, the frames that hold it
// open, and the frame that closes it must never be folded into the quiet
// baseline, or repeated bursts would drift the learned floor upward and
// require progressively louder audio to activate.
func TestGateBurstDoesNotContaminateLearnedFloor(t *testing.T) {
	g := NewAdaptiveNoiseGate()
	quiet := tone(0.0005, 320)
	for i := 0; i < 25; i++ { // exactly 500 ms: one flushed quiet stretch
		g.Update(quiet, frame)
	}
	if len(g.history) != 1 {
		t.Fatalf("expected exactly one flushed quiet stretch before the burst, got %d", len(g.history))
	}
	baseline := g.history[0].level

	loud := tone(0.3, 320)
	if !g.Update(loud, frame) {
		t.Fatal("expected the burst to open the gate")
	}
	for i := 0; i < 5; i++ { // hold the burst open
		if !g.Update(loud, frame) {
			t.Fatalf("burst closed unexpectedly at frame %d", i)
		}
	}

	// close the burst (500 ms of quiet) and flush a fresh quiet stretch behind it
	for i := 0; i < 60; i++ {
		g.Update(quiet, frame)
	}
	if g.open {
		t.Fatal("burst should have closed during the trailing quiet frames")
	}
	if len(g.history) < 2 {
		t.Fatalf("expected a second flushed quiet stretch after the burst, got %d entries", len(g.history))
	}
	postBurst := g.history[len(g.history)-1].level
	if postBurst > baseline*1.5 {
		t.Fatalf("post-burst quiet stretch is contaminated by the burst: baseline=%v post-burst=%v", baseline, postBurst)
	}
}

// TestGateWindowEvictsOldestStretch exercises the 10 s rolling window: once
// enough newer quiet stretches accumulate, the oldest one ages out and no
// longer contributes to the learned floor.
func TestGateWindowEvictsOldestStretch(t *testing.T) {
	g := NewAdaptiveNoiseGate()
	quiet1 := tone(0.0005, 320)
	for i := 0; i < 25; i++ { // one flushed 500 ms stretch
		g.Update(quiet1, frame)
	}
	if len(g.history) != 1 {
		t.Fatalf("expected 1 entry after the first flush, got %d", len(g.history))
	}
	firstLevel := g.history[0].level

	quiet2 := tone(0.001, 320) // a louder, but still non-activating, ambient level
	for block := 0; block < 25; block++ {
		for i := 0; i < 25; i++ { // 25 more 500 ms stretches: 12.5 s of new history
			if g.Update(quiet2, frame) {
				t.Fatalf("quiet2 frame unexpectedly opened the gate (block %d, frame %d)", block, i)
			}
		}
	}
	if g.historyDuration > gateWindow {
		t.Fatalf("history duration %v exceeds the %v window after eviction", g.historyDuration, gateWindow)
	}
	for _, h := range g.history {
		if h.level == firstLevel {
			t.Fatal("the oldest stretch should have aged out of the 10 s window")
		}
	}
}
