package gptlive

import (
	"encoding/binary"
	"math"
	"time"
)

const (
	gateActivationRatio   = 3.0
	gateDeactivationRatio = 1.8
	gateMinSilence        = 500 * time.Millisecond
	gateWindow            = 10 * time.Second
	gateSilenceFloor      = 1e-4
)

type stretch struct {
	level    float64
	duration time.Duration
}

// AdaptiveNoiseGate opens on output that stands out from the model's own silence.
// The floor is the quietest min-silence stretch the model produced while not
// speaking, within the window; speech never raises it, and the frame under test
// is always compared against a baseline learned from frames strictly before it.
//
// Not safe for concurrent use: callers must serialize Update/Deactivate, e.g. by
// calling both only from the single audio-processing goroutine that owns them.
type AdaptiveNoiseGate struct {
	history         []stretch
	historyDuration time.Duration
	stretchSum      float64
	stretchDuration time.Duration
	open            bool
	quiet           time.Duration
}

func NewAdaptiveNoiseGate() *AdaptiveNoiseGate { return &AdaptiveNoiseGate{} }

func rms(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768.0
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

func (g *AdaptiveNoiseGate) Deactivate() {
	g.open = false
	g.quiet = 0
}

// floor reports the current baseline, learned strictly from frames already
// accumulated (flushed history plus any not-yet-flushed pending stretch). It
// never reflects the frame currently being evaluated by Update.
func (g *AdaptiveNoiseGate) floor() float64 {
	var floor float64
	switch {
	case len(g.history) > 0:
		floor = g.history[0].level
		for _, h := range g.history[1:] {
			if h.level < floor {
				floor = h.level
			}
		}
	case g.stretchDuration > 0:
		floor = g.stretchSum / g.stretchDuration.Seconds()
	default:
		floor = gateSilenceFloor
	}
	if floor < gateSilenceFloor {
		floor = gateSilenceFloor
	}
	return floor
}

// hasBaseline reports whether any quiet stretch, flushed or still pending,
// has ever been accumulated. Until it has, floor() is just the fixed
// noise-floor constant rather than a value learned from this stream's own
// ambient level.
func (g *AdaptiveNoiseGate) hasBaseline() bool {
	return len(g.history) > 0 || g.stretchDuration > 0
}

// accumulate folds a frame that was quiet for its whole duration into the
// pending silence stretch, flushing it to history once it reaches the
// minimum silence duration and evicting stretches that have aged out of the
// rolling window.
func (g *AdaptiveNoiseGate) accumulate(level float64, duration time.Duration) {
	g.stretchSum += level * duration.Seconds()
	g.stretchDuration += duration
	if g.stretchDuration >= gateMinSilence {
		mean := g.stretchSum / g.stretchDuration.Seconds()
		g.history = append(g.history, stretch{mean, g.stretchDuration})
		g.historyDuration += g.stretchDuration
		g.stretchSum, g.stretchDuration = 0, 0
		for g.historyDuration > gateWindow && len(g.history) > 1 {
			g.historyDuration -= g.history[0].duration
			g.history = g.history[1:]
		}
	}
}

// Update reports whether the frame belongs to an open burst of output. The
// activation and deactivation tests are always evaluated against the floor
// learned from frames strictly before this one: the frame under test is
// folded into the baseline only after that decision, and only if it turns
// out to have been quiet for its whole duration (the gate was closed before
// and after the call). A frame that triggers activation, or the frame that
// completes a deactivation, is never counted as part of the silence it was
// judged against.
//
// Before any baseline has ever been learned, floor() falls back to the fixed
// noise-floor constant, which is not tuned to this stream's actual ambient
// level and can sit within a small multiple of ordinary quiet background
// noise. Demanding the usual activation ratio above it there would risk
// mistaking that ambient noise for speech on the very first frame, before
// the gate has had any chance to learn what "quiet" looks like here; squaring
// the ratio for that one case keeps comfortable headroom below plausible
// ambient noise while still opening promptly on audibly loud output.
func (g *AdaptiveNoiseGate) Update(pcm []byte, duration time.Duration) bool {
	level := rms(pcm)
	floor := g.floor()
	wasOpen := g.open
	if !wasOpen {
		activationRatio := gateActivationRatio
		if !g.hasBaseline() {
			activationRatio *= gateActivationRatio
		}
		if level > floor*activationRatio {
			g.open = true
			g.quiet = 0
		}
	} else if level < floor*gateDeactivationRatio {
		g.quiet += duration
		if g.quiet >= gateMinSilence {
			g.open = false
		}
	} else {
		g.quiet = 0
	}
	if !wasOpen && !g.open {
		g.accumulate(level, duration)
	}
	return g.open
}
