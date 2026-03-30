package vad

import (
	"sync"
	"time"
)

type VADEvent struct {
	SpeechStart bool
	SpeechEnd   bool
	Probability float32
}

// VADPipeline buffers incoming audio frames, calls the VAD engine, and
// emits discrete SpeechStart/SpeechEnd events with debouncing/endpointing.
type VADPipeline struct {
	engine         VAD
	threshold      float32
	endThreshold   float32 // Lower threshold for ending speech (hysteresis)
	endpointMS     int
	minSpeechMS    int // Minimum speech duration to avoid false positives

	buffer []int16

	isSpeaking   bool
	speechStart  time.Time
	silenceStart time.Time

	mu sync.Mutex
}

// NewVADPipeline creates a new VAD pipeline wrapper around the provided VAD engine.
// threshold is the probability [0, 1] required to trigger voice detection.
// endpointMS is the amount of silence required to emit a SpeechEnd event.
func NewVADPipeline(engine VAD, threshold float32, endpointMS int) *VADPipeline {
	// Use hysteresis: lower threshold for ending speech to prevent rapid toggling
	endThreshold := threshold * 0.7 // 30% lower for ending
	if endThreshold < 0.3 {
		endThreshold = 0.3
	}
	
	return &VADPipeline{
		engine:       engine,
		threshold:    threshold,
		endThreshold: endThreshold,
		endpointMS:   endpointMS,
		minSpeechMS:  300, // Require at least 300ms of speech to avoid false positives
		buffer:       make([]int16, 0, engine.FrameLength()*2),
	}
}

// Push adds audio samples to the VAD. It may emit one or more VAD events.
func (v *VADPipeline) Push(samples []int16) []VADEvent {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.buffer = append(v.buffer, samples...)
	var events []VADEvent

	frameLen := v.engine.FrameLength()
	for len(v.buffer) >= frameLen {
		frame := v.buffer[:frameLen]
		v.buffer = v.buffer[frameLen:]

		prob, err := v.engine.Process(frame)
		if err != nil {
			// Handle error if necessary; for now just ignore this frame
			continue
		}

		if prob >= v.threshold {
			if !v.isSpeaking {
				v.isSpeaking = true
				v.speechStart = time.Now()
				events = append(events, VADEvent{SpeechStart: true, Probability: prob})
			}
			// Reset silence timer because we detected voice
			v.silenceStart = time.Time{}
		} else if prob < v.endThreshold {
			// Only consider ending speech if probability drops below the lower threshold
			if v.isSpeaking {
				// Check if we've had enough speech duration to be valid
				speechDuration := time.Since(v.speechStart).Milliseconds()
				
				if v.silenceStart.IsZero() {
					v.silenceStart = time.Now()
				} else if time.Since(v.silenceStart).Milliseconds() >= int64(v.endpointMS) {
					// Only emit SpeechEnd if we had sufficient speech duration
					if speechDuration >= int64(v.minSpeechMS) {
						v.isSpeaking = false
						events = append(events, VADEvent{SpeechEnd: true, Probability: prob})
					} else {
						// False positive - just reset without emitting event
						v.isSpeaking = false
					}
					v.silenceStart = time.Time{}
					v.speechStart = time.Time{}
				}
			}
		}
		// If probability is between endThreshold and threshold, maintain current state
	}

	return events
}

// Close releases the underlying VAD engine
func (v *VADPipeline) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.engine.Close()
}
