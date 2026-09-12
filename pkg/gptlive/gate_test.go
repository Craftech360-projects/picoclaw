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
