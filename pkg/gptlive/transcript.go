package gptlive

import (
	"time"
)

const speechGapMs = 800 // a pause this long on the model clock ends a message
const speechIdle = 800 * time.Millisecond

type UserTranscript struct {
	ID        string
	Text      string
	Final     bool
	StartedAt time.Time
}

type AgentTranscript struct {
	ID      string
	Delta   string
	Text    string
	StartMs int
	EndMs   int
}

func (UserTranscript) isEvent()  {}
func (AgentTranscript) isEvent() {}

type speech struct {
	id        string
	text      string
	endMs     *int
	startedAt time.Time
	idle      *time.Timer
	gen       int // bumped on every delta; guards against a stale idle-timer fire
}

func (s *Session) onTranscriptDelta(role string, ev ServerEvent) {
	if ev.Delta == "" {
		return
	}
	s.speechMu.Lock()
	var gapFinal *UserTranscript
	sp := s.speech[role]
	if sp != nil && sp.endMs != nil && ev.StartMs != nil && *ev.StartMs-*sp.endMs > speechGapMs {
		gapFinal = s.endSpeechLocked(role)
		sp = nil
	}
	if sp == nil {
		sp = &speech{id: newEventID("speech_"), startedAt: time.Now()}
		s.speech[role] = sp
	}
	sp.gen++
	gen := sp.gen
	sp.text += ev.Delta
	if ev.EndMs != nil && (sp.endMs == nil || *ev.EndMs > *sp.endMs) {
		v := *ev.EndMs
		sp.endMs = &v
	}
	if sp.idle != nil {
		sp.idle.Stop()
	}
	// sp.idle.Stop() above cannot stop a timer goroutine that already fired and is
	// waiting on speechMu, so the callback re-checks id+gen once it gets the lock and
	// bails if this delta (or a later one) has since moved the speech on. id alone
	// guards against this role's speech having been gap-closed and replaced by a fresh
	// speech object (whose gen restarts at 1, so gen alone could coincidentally match
	// a stale timer's captured value); gen guards against further deltas landing on
	// that same, still-open speech object before this timer fires.
	id, gen := sp.id, sp.gen
	sp.idle = time.AfterFunc(speechIdle, func() { s.endSpeechIfCurrent(role, id, gen) })
	text, startedAt := sp.text, sp.startedAt
	s.speechMu.Unlock()

	// Never emit while holding speechMu: emit can block until the events channel
	// drains or the session ends, which would stall every other delta and timer
	// callback waiting on this Session's lock.
	if gapFinal != nil {
		s.emit(*gapFinal)
	}

	if role == "user" {
		s.emit(UserTranscript{ID: id, Text: text, Final: false, StartedAt: startedAt})
		return
	}
	var start, end int
	if ev.StartMs != nil {
		start = *ev.StartMs
	}
	if ev.EndMs != nil {
		end = *ev.EndMs
	}
	s.emit(AgentTranscript{ID: id, Delta: ev.Delta, Text: text, StartMs: start, EndMs: end})
}

// endSpeech closes a role's open message: the user's becomes final, both go to history.
func (s *Session) endSpeech(role string) {
	s.speechMu.Lock()
	final := s.endSpeechLocked(role)
	s.speechMu.Unlock()
	if final != nil {
		s.emit(*final)
	}
}

// endSpeechIfCurrent is the idle timer's callback. sp.idle.Stop() cannot prevent an
// already-fired timer goroutine from running concurrently with a new delta, so this
// re-checks the speech's identity (id) and generation (gen) once it holds the lock: if
// this role's speech has since been replaced (a gap closed it and a new one started) or
// extended by a later delta, id or gen will no longer match and the fire is stale and
// ignored, leaving the still-continuing utterance alone.
func (s *Session) endSpeechIfCurrent(role, id string, gen int) {
	s.speechMu.Lock()
	sp := s.speech[role]
	if sp == nil || sp.id != id || sp.gen != gen {
		s.speechMu.Unlock()
		return
	}
	final := s.endSpeechLocked(role)
	s.speechMu.Unlock()
	if final != nil {
		s.emit(*final)
	}
}

// endSpeechLocked closes role's open speech (caller holds speechMu) and returns the
// final UserTranscript to emit once the caller has released the lock, or nil when
// there is nothing to emit (no open speech, empty text, or a non-user role).
func (s *Session) endSpeechLocked(role string) *UserTranscript {
	sp := s.speech[role]
	if sp == nil {
		return nil
	}
	delete(s.speech, role)
	if sp.idle != nil {
		sp.idle.Stop()
	}
	if sp.text == "" {
		return nil
	}
	s.historyMu.Lock()
	s.history = append(s.history, TextItem(role, sp.text))
	s.historyMu.Unlock()
	if role == "user" {
		return &UserTranscript{ID: sp.id, Text: sp.text, Final: true, StartedAt: sp.startedAt}
	}
	return nil
}

// History is the conversation so far, as the service accepts it as startup history.
func (s *Session) History() []InputItem {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]InputItem(nil), s.history...)
}

// resetTranscripts finalises any open user speech and drops open speech for every
// other role without finalising it. Only the user role is finalised deliberately: an
// in-flight assistant utterance interrupted by a reconnect is not a completed message.
func (s *Session) resetTranscripts() {
	s.speechMu.Lock()
	final := s.endSpeechLocked("user")
	for role, sp := range s.speech {
		if sp.idle != nil {
			sp.idle.Stop()
		}
		delete(s.speech, role)
	}
	s.speechMu.Unlock()
	if final != nil {
		s.emit(*final)
	}
}
