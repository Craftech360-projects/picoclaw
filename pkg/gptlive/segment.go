package gptlive

import "time"

const segmentIdle = 800 * time.Millisecond

// Segmenter turns the continuous output stream into bursts: open while the gate
// says the model is audibly speaking, closed on silence or when frames stop coming.
//
// Not safe for concurrent use: Feed and Tick must be serialized, e.g. by calling
// both only from the single audio-processing goroutine that owns this Segmenter.
type Segmenter struct {
	gate     *AdaptiveNoiseGate
	open     bool
	lastFeed time.Time
	onOpen   func()
	onClose  func()
	now      func() time.Time
}

func NewSegmenter(onOpen, onClose func()) *Segmenter {
	return &Segmenter{gate: NewAdaptiveNoiseGate(), onOpen: onOpen, onClose: onClose, now: time.Now}
}

func (s *Segmenter) Open() bool { return s.open }

func (s *Segmenter) Feed(pcm []byte, duration time.Duration) {
	s.lastFeed = s.now()
	if s.gate.Update(pcm, duration) {
		if !s.open {
			s.open = true
			s.onOpen()
		}
		return
	}
	if s.open {
		s.close()
	}
}

// Tick closes a burst whose frames stopped arriving; the pipeline calls it on a timer.
func (s *Segmenter) Tick(now time.Time) {
	if s.open && !s.lastFeed.IsZero() && now.Sub(s.lastFeed) >= segmentIdle {
		s.close()
	}
}

func (s *Segmenter) close() {
	s.open = false
	s.gate.Deactivate()
	s.onClose()
}
