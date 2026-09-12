package gptlive

import (
	"sync"
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
}

var speechMu sync.Mutex // guards Session.speech across the read goroutine and idle timers

func (s *Session) onTranscriptDelta(role string, ev ServerEvent) {
	if ev.Delta == "" {
		return
	}
	speechMu.Lock()
	sp := s.speech[role]
	if sp != nil && sp.endMs != nil && ev.StartMs != nil && *ev.StartMs-*sp.endMs > speechGapMs {
		s.endSpeechLocked(role)
		sp = nil
	}
	if sp == nil {
		sp = &speech{id: newEventID("speech_"), startedAt: time.Now()}
		s.speech[role] = sp
	}
	sp.text += ev.Delta
	if ev.EndMs != nil && (sp.endMs == nil || *ev.EndMs > *sp.endMs) {
		v := *ev.EndMs
		sp.endMs = &v
	}
	if sp.idle != nil {
		sp.idle.Stop()
	}
	sp.idle = time.AfterFunc(speechIdle, func() { s.endSpeech(role) })
	id, text, startedAt := sp.id, sp.text, sp.startedAt
	speechMu.Unlock()

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
	speechMu.Lock()
	s.endSpeechLocked(role)
	speechMu.Unlock()
}

func (s *Session) endSpeechLocked(role string) {
	sp := s.speech[role]
	if sp == nil {
		return
	}
	delete(s.speech, role)
	if sp.idle != nil {
		sp.idle.Stop()
	}
	if sp.text == "" {
		return
	}
	s.historyMu.Lock()
	s.history = append(s.history, TextItem(role, sp.text))
	s.historyMu.Unlock()
	if role == "user" {
		s.emit(UserTranscript{ID: sp.id, Text: sp.text, Final: true, StartedAt: sp.startedAt})
	}
}

// History is the conversation so far, as the service accepts it as startup history.
func (s *Session) History() []InputItem {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]InputItem(nil), s.history...)
}

func (s *Session) resetTranscripts() {
	speechMu.Lock()
	defer speechMu.Unlock()
	s.endSpeechLocked("user")
	for role, sp := range s.speech {
		if sp.idle != nil {
			sp.idle.Stop()
		}
		delete(s.speech, role)
	}
}
