package livekit

import (
	"sync"

	"github.com/sipeed/picoclaw/pkg/voice/stt"
)

// sttStreamHolder is the one place a session's STT stream lives, because two
// goroutines hold it: the PCM track writer calls SendAudio, and RunInbound reads
// Results. Before this, each kept its own copy, so a stream could not be replaced
// without one of them still writing into the dead one.
//
// Replacing matters because closing an STT stream closes its result channel,
// which ends RunInbound and leaves the session unable to hear anything for the
// rest of the call — observed live on 2026-08-10, mid-conversation.
type sttStreamHolder struct {
	mu     sync.RWMutex
	stream stt.TranscriptionStream
}

func newSTTStreamHolder(stream stt.TranscriptionStream) *sttStreamHolder {
	return &sttStreamHolder{stream: stream}
}

func (h *sttStreamHolder) Get() stt.TranscriptionStream {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stream
}

// Swap installs a replacement and returns the stream it displaced, so the caller
// decides whether to close it. Closing here would risk closing a stream another
// goroutine is mid-write on.
func (h *sttStreamHolder) Swap(stream stt.TranscriptionStream) stt.TranscriptionStream {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	previous := h.stream
	h.stream = stream
	return previous
}
