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

// TestSpeechMuIsPerSessionNotShared guards against speechMu being (or becoming again) a
// package-level variable: if it were, holding session A's lock would block session B's
// unrelated delta from ever proceeding.
func TestSpeechMuIsPerSessionNotShared(t *testing.T) {
	a := newTestSession()
	b := newTestSession()

	a.speechMu.Lock()
	defer a.speechMu.Unlock()

	done := make(chan struct{})
	go func() {
		b.onTranscriptDelta("user", ServerEvent{Delta: "hi", StartMs: ms(0), EndMs: ms(100)})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session B's onTranscriptDelta blocked while session A's speechMu was held; speechMu must be per-Session, not shared")
	}
}

// TestIdleTimerGenerationGuardIgnoresStaleFire drives the timer callback path directly
// with a stale generation, rather than relying on sleep timing, to deterministically
// reproduce the race: an idle timer armed for one delta can still fire and reach the
// lock after a later delta has extended the same utterance. sp.idle.Stop() does not
// prevent an already-fired timer goroutine from running, so the callback itself must
// refuse to finalise once its generation is stale.
func TestIdleTimerGenerationGuardIgnoresStaleFire(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("user", ServerEvent{Delta: "hello", StartMs: ms(0), EndMs: ms(300)})

	s.speechMu.Lock()
	staleID, staleGen := s.speech["user"].id, s.speech["user"].gen
	s.speechMu.Unlock()

	// A second delta extends the same utterance before the first idle timer fires.
	s.onTranscriptDelta("user", ServerEvent{Delta: " world", StartMs: ms(320), EndMs: ms(600)})

	// Simulate the first timer's goroutine finally acquiring the lock after losing the
	// race against the delta above: same speech id, stale generation.
	s.endSpeechIfCurrent("user", staleID, staleGen)

	s.speechMu.Lock()
	sp, stillOpen := s.speech["user"]
	s.speechMu.Unlock()
	if !stillOpen {
		t.Fatal("stale-generation timer fire wrongly finalised a still-continuing utterance")
	}
	if sp.text != "hello world" {
		t.Fatalf("speech text corrupted by the stale fire: %q", sp.text)
	}

	// A genuine fire for the current id+generation must still finalise it.
	s.speechMu.Lock()
	curID, curGen := sp.id, sp.gen
	s.speechMu.Unlock()
	s.endSpeechIfCurrent("user", curID, curGen)

	s.speechMu.Lock()
	_, stillOpen = s.speech["user"]
	s.speechMu.Unlock()
	if stillOpen {
		t.Fatal("current id+generation idle fire should have finalised the utterance")
	}

	var sawFinal bool
	for len(s.events) > 0 {
		if ut, ok := (<-s.events).(UserTranscript); ok && ut.Final && ut.Text == "hello world" {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Fatal("expected a final UserTranscript for \"hello world\"")
	}
}

// TestIdleTimerGenerationGuardIgnoresStaleFireAfterReplacement covers the case a bare
// generation counter misses: a gap closes and replaces a role's speech with a brand new
// one, whose gen restarts at 1 — the same value the just-closed speech's timer may have
// captured. A stale timer must still be rejected by comparing the speech's id, not gen
// alone, or it would wrongly finalise the new, unrelated utterance.
func TestIdleTimerGenerationGuardIgnoresStaleFireAfterReplacement(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("user", ServerEvent{Delta: "first", StartMs: ms(0), EndMs: ms(300)})

	s.speechMu.Lock()
	staleID, staleGen := s.speech["user"].id, s.speech["user"].gen // gen == 1
	s.speechMu.Unlock()

	// A gap longer than speechGapMs closes "first" and starts a brand new speech for
	// the same role; the new speech's gen restarts at 1, coincidentally equal.
	s.onTranscriptDelta("user", ServerEvent{Delta: "second", StartMs: ms(2000), EndMs: ms(2300)})

	s.speechMu.Lock()
	newID, newGen := s.speech["user"].id, s.speech["user"].gen
	s.speechMu.Unlock()
	if newID == staleID {
		t.Fatal("test setup invalid: expected a new speech id after the gap")
	}
	if newGen != staleGen {
		t.Fatalf("test setup invalid: expected the coincidental gen match this test targets, got stale=%d new=%d", staleGen, newGen)
	}

	// The stale timer, armed for "first", finally fires. Its gen matches the new
	// speech's gen by coincidence, but its id does not: it must not finalise "second".
	s.endSpeechIfCurrent("user", staleID, staleGen)

	s.speechMu.Lock()
	sp, stillOpen := s.speech["user"]
	s.speechMu.Unlock()
	if !stillOpen || sp.text != "second" {
		t.Fatalf("stale timer from a replaced speech wrongly finalised the new one: open=%v sp=%+v", stillOpen, sp)
	}
}
