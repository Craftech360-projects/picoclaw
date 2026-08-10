package vad

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"
)

// Measures TEN VAD speech probabilities over real child recordings so the
// production threshold is chosen from data rather than from intuition about how
// loud a five-year-old is. Skips unless KID_PCM_FILES is set (16 kHz mono s16le,
// comma-separated), so it never runs in CI.
//
//	KID_PCM_FILES=a.pcm,b.pcm go test -run TestKidSpeechThreshold -v ./pkg/voice/vad/
func TestKidSpeechThreshold(t *testing.T) {
	raw := os.Getenv("KID_PCM_FILES")
	if raw == "" {
		t.Skip("KID_PCM_FILES not set")
	}

	for _, path := range splitList(raw) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		v, err := NewTenVAD(256, 0.5)
		if err != nil {
			t.Fatalf("NewTenVAD: %v", err)
		}

		frame := v.FrameLength()
		samples := make([]int16, frame)
		probs := make([]float32, 0, len(data)/(frame*2))
		for off := 0; off+frame*2 <= len(data); off += frame * 2 {
			for i := 0; i < frame; i++ {
				samples[i] = int16(binary.LittleEndian.Uint16(data[off+i*2:]))
			}
			p, perr := v.Process(samples)
			if perr != nil {
				t.Fatalf("Process: %v", perr)
			}
			probs = append(probs, p)
		}
		v.Close()

		if len(probs) == 0 {
			t.Fatalf("%s produced no frames", path)
		}

		sorted := append([]float32(nil), probs...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		pct := func(p float64) float32 { return sorted[int(float64(len(sorted)-1)*p)] }

		t.Logf("%s: %d frames  p10=%.3f p50=%.3f p75=%.3f p90=%.3f p99=%.3f max=%.3f",
			path, len(probs), pct(0.10), pct(0.50), pct(0.75), pct(0.90), pct(0.99), sorted[len(sorted)-1])

		// Share of frames each candidate threshold would treat as speech. A
		// recording of a child talking should have a substantial speech share; a
		// threshold that yields almost none is why the toy looks deaf.
		for _, th := range []float32{0.4, 0.5, 0.6, 0.68, 0.72, 0.8} {
			var n int
			for _, p := range probs {
				if p >= th {
					n++
				}
			}
			t.Logf("   threshold %.2f -> %5.1f%% of frames are speech", th, 100*float64(n)/float64(len(probs)))
		}
	}
}

func splitList(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
