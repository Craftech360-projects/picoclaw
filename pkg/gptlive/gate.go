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
// speaking, within the window; speech never raises it.
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

// Update reports whether the frame belongs to an open burst of output.
func (g *AdaptiveNoiseGate) Update(pcm []byte, duration time.Duration) bool {
	level := rms(pcm)
	if !g.open {
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
		floor = level
	}
	if floor < gateSilenceFloor {
		floor = gateSilenceFloor
	}
	if !g.open {
		if level > floor*gateActivationRatio {
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
	return g.open
}
