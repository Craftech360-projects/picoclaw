// Command tenvad-probe runs a WAV file through the same ten_vad pipeline the
// LiveKit worker uses and prints the speech segments it detects.
//
// It exists so a VAD can be evaluated against recorded audio instead of a live
// room: point it at the same WAVs a hosted VAD saw and the two are directly
// comparable. Defaults match room_session.go (threshold 0.7, endpoint 1000ms).
//
//	go run ./cmd/tenvad-probe take.wav [more.wav...]
//	go run ./cmd/tenvad-probe -threshold 0.5 -endpoint 600 take.wav
//	go run ./cmd/tenvad-probe -probs take.wav
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

const sampleRate = 16000

// readWAV returns the int16 samples of a 16kHz mono PCM WAV. It walks the
// chunk list rather than assuming a 44-byte header, because recorders vary.
func readWAV(path string) ([]int16, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s: not a RIFF/WAVE file", path)
	}
	var channels, bits, rate int
	for off := 12; off+8 <= len(raw); {
		id := string(raw[off : off+4])
		size := int(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		body := off + 8
		if body+size > len(raw) {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			if channels != 1 || bits != 16 || rate != sampleRate {
				return nil, fmt.Errorf("%s: want 16kHz mono int16, got %dHz %dch %dbit",
					path, rate, channels, bits)
			}
			out := make([]int16, size/2)
			for i := range out {
				out[i] = int16(binary.LittleEndian.Uint16(raw[body+i*2 : body+i*2+2]))
			}
			return out, nil
		}
		off = body + size
		if size%2 == 1 {
			off++ // chunks are word-aligned
		}
	}
	return nil, fmt.Errorf("%s: no data chunk", path)
}

// dumpProbs prints the raw engine probability per second, bypassing the
// pipeline. It distinguishes "this VAD thinks everything is speech" from "the
// harness is feeding it wrong", which the segment view alone cannot.
func dumpProbs(path string, samples []int16, engine vad.VAD) {
	fmt.Printf("\n=== %s  per-second probability\n", path)
	var sum, max float32
	var cnt, sec int
	for i := 0; i+256 <= len(samples); i += 256 {
		pr, err := engine.Process(samples[i : i+256])
		if err != nil {
			fmt.Fprintf(os.Stderr, "process: %v\n", err)
			return
		}
		sum += pr
		cnt++
		if pr > max {
			max = pr
		}
		if (i+256)/sampleRate > sec {
			fmt.Printf("  %3ds  mean %.3f  max %.3f\n", sec, sum/float32(cnt), max)
			sum, max, cnt, sec = 0, 0, 0, sec+1
		}
	}
}

func main() {
	probs := flag.Bool("probs", false, "print per-second probabilities instead of segments")
	threshold := flag.Float64("threshold", 0.7, "VAD probability threshold (room_session default)")
	endpoint := flag.Int("endpoint", 1000, "silence in ms before SpeechEnd (room_session default)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: tenvad-probe [-probs] [-threshold f] [-endpoint ms] file.wav...")
		os.Exit(2)
	}

	for _, path := range flag.Args() {
		samples, err := readWAV(path)
		if err != nil {
			fmt.Printf("%s: %v\n", path, err)
			continue
		}
		engine, err := vad.NewTenVAD(256, float32(*threshold))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ten_vad init: %v\n", err)
			os.Exit(1)
		}

		if *probs {
			dumpProbs(path, samples, engine)
			continue
		}

		pipe := vad.NewVADPipeline(engine, float32(*threshold), *endpoint)
		fmt.Printf("\n=== %s  (%.1fs)  threshold=%.2f endpoint=%dms\n",
			path, float64(len(samples))/sampleRate, *threshold, *endpoint)

		// 256-sample hops, matching the worker's frame size, paced at 1x.
		// VADPipeline measures its endpoint silence in WALL-CLOCK time
		// (time.Since), not audio time, so replaying faster than realtime makes
		// the endpoint unreachable and every file collapses to one open segment.
		var startMS float64
		open := false
		n := 0
		frameDur := time.Duration(256) * time.Second / sampleRate
		for i := 0; i+256 <= len(samples); i += 256 {
			time.Sleep(frameDur)
			nowMS := float64(i+256) / sampleRate * 1000
			for _, ev := range pipe.Push(samples[i : i+256]) {
				if ev.SpeechStart {
					startMS, open = nowMS, true
				}
				if ev.SpeechEnd && open {
					fmt.Printf("  #%-2d %7.2fs -> %7.2fs   (%.2fs)\n",
						n, startMS/1000, nowMS/1000, (nowMS-startMS)/1000)
					n, open = n+1, false
				}
			}
		}
		if open {
			// A take that ends mid-utterance: report it rather than dropping it,
			// otherwise the segment count silently disagrees with the audio.
			end := float64(len(samples)) / sampleRate * 1000
			fmt.Printf("  #%-2d %7.2fs -> %7.2fs   (%.2fs)  [still open at EOF]\n",
				n, startMS/1000, end/1000, (end-startMS)/1000)
			n++
		}
		fmt.Printf("  -> %d segment(s)\n", n)
		_ = pipe.Close()
	}
}
