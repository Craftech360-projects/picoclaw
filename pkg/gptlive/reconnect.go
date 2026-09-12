package gptlive

import "errors"

// Reconnected is emitted after a dropped connection is replaced by a new one and the
// new session.start has been sent, seeded from history.
type Reconnected struct{}

func (Reconnected) isEvent() {}

// resetForReconnect: a new connection is a new session, reseeded from history.
// Whatever the dropped one was still carrying never arrives.
func (s *Session) resetForReconnect() {
	s.resetTranscripts()
	s.resetDelegations()
	s.mu.Lock()
	s.sessionID = ""
	s.startSent = false
	s.started = make(chan struct{})
	s.mu.Unlock()
	// drain queued client events; they belonged to the old session
	for {
		select {
		case <-s.out:
		default:
			return
		}
	}
}

// isFatal reports whether err is (or wraps) a FatalProtocolError. classify only ever
// produces that type for a server-reported error whose ErrorBody.Fatal() is true, so
// the type itself is the fatal signal — no string matching on err.Error() needed.
func isFatal(err error) bool {
	var fatal *FatalProtocolError
	return errors.As(err, &fatal)
}
