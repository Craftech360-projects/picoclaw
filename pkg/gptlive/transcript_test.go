package gptlive

import (
	"testing"
	"time"
)

func newTestSession() *Session {
	s := &Session{events: make(chan Event, 64), speech: map[string]*speech{}}
	s.ctx, s.cancel = contextWithCancel()
	return s
}

func ms(v int) *int { return &v }

func TestUserTranscriptSplitsOnAnEightHundredMsGap(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("user", ServerEvent{Delta: "what time", StartMs: ms(0), EndMs: ms(600)})
	s.onTranscriptDelta("user", ServerEvent{Delta: " is it", StartMs: ms(650), EndMs: ms(900)})
	s.onTranscriptDelta("user", ServerEvent{Delta: "tell a story", StartMs: ms(2000), EndMs: ms(2600)}) // 1100 ms gap

	var got []UserTranscript
	for len(s.events) > 0 {
		if ut, ok := (<-s.events).(UserTranscript); ok {
			got = append(got, ut)
		}
	}
	// interim "what time", interim "what time is it", FINAL "what time is it", interim "tell a story"
	if len(got) != 4 {
		t.Fatalf("want 4 user transcript events, got %d: %+v", len(got), got)
	}
	if got[1].Text != "what time is it" || got[1].Final {
		t.Errorf("second delta should extend the same utterance: %+v", got[1])
	}
	if !got[2].Final || got[2].Text != "what time is it" || got[2].ID != got[1].ID {
		t.Errorf("gap must finalise the first utterance: %+v", got[2])
	}
	if got[3].ID == got[2].ID || got[3].Text != "tell a story" {
		t.Errorf("new utterance must get a new id: %+v", got[3])
	}
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != "user" || hist[0].Content[0].Text != "what time is it" {
		t.Errorf("finalised utterance must be in history: %+v", hist)
	}
}

func TestAssistantTranscriptIsRecordedWhenItEnds(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("assistant", ServerEvent{Delta: "Hi ", StartMs: ms(0), EndMs: ms(300)})
	s.onTranscriptDelta("assistant", ServerEvent{Delta: "there", StartMs: ms(320), EndMs: ms(600)})
	s.endSpeech("assistant")
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != "assistant" || hist[0].Content[0].Type != "output_text" || hist[0].Content[0].Text != "Hi there" {
		t.Errorf("assistant speech must land in history as output_text: %+v", hist)
	}
}

func TestIdleTimerFinalisesUserSpeech(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("user", ServerEvent{Delta: "hello", StartMs: ms(0), EndMs: ms(300)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-s.events:
			if ut, ok := ev.(UserTranscript); ok && ut.Final {
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("user speech never finalised by the idle timer")
}
