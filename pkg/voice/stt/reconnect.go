package stt

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// autoReconnectStream wraps a provider stream and transparently redials when
// the underlying stream dies mid-session (e.g. Sarvam "ASR model call
// failed" closing the websocket). Without this, one provider hiccup leaves
// the whole voice session permanently deaf.
type autoReconnectStream struct {
	provider Provider
	ctx      context.Context
	opts     StreamOptions

	mu  sync.RWMutex
	cur TranscriptionStream

	out       chan TranscriptEvent
	closed    chan struct{}
	closeOnce sync.Once
}

// NewAutoReconnectStream opens a provider stream and keeps it alive across
// unexpected disconnects. The initial open error is returned as-is; later
// failures trigger redials with backoff.
func NewAutoReconnectStream(ctx context.Context, provider Provider, opts StreamOptions) (TranscriptionStream, error) {
	first, err := provider.OpenStream(ctx, opts)
	if err != nil {
		return nil, err
	}
	r := &autoReconnectStream{
		provider: provider,
		ctx:      ctx,
		opts:     opts,
		cur:      first,
		out:      make(chan TranscriptEvent, 32),
		closed:   make(chan struct{}),
	}
	go r.pump()
	return r, nil
}

func (r *autoReconnectStream) current() TranscriptionStream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cur
}

func (r *autoReconnectStream) pump() {
	defer close(r.out)
	for {
		s := r.current()
		for evt := range s.Results() {
			select {
			case r.out <- evt:
			case <-r.closed:
				return
			}
		}
		// Underlying stream ended. If we were closed on purpose, stop.
		select {
		case <-r.closed:
			return
		default:
		}

		var next TranscriptionStream
		// ponytail: fixed 5-attempt backoff; make configurable if a provider
		// ever needs longer outage tolerance.
		for attempt, wait := 1, 500*time.Millisecond; attempt <= 5; attempt, wait = attempt+1, wait*2 {
			select {
			case <-time.After(wait):
			case <-r.closed:
				return
			case <-r.ctx.Done():
				return
			}
			ns, err := r.provider.OpenStream(r.ctx, r.opts)
			if err == nil {
				next = ns
				break
			}
			logger.WarnCF("livekit", "STT reconnect attempt failed", map[string]any{
				"provider": r.provider.Name(),
				"attempt":  attempt,
				"error":    err.Error(),
			})
		}
		if next == nil {
			logger.ErrorCF("livekit", "STT reconnect gave up; transcription stopped", map[string]any{
				"provider": r.provider.Name(),
			})
			return
		}
		r.mu.Lock()
		r.cur = next
		r.mu.Unlock()
		logger.InfoCF("livekit", "STT stream reconnected after disconnect", map[string]any{
			"provider": r.provider.Name(),
		})
	}
}

func (r *autoReconnectStream) SendAudio(pcm []byte) error {
	// Errors during a reconnect window are swallowed: that audio is lost, but
	// the pump goroutine is already redialing and the next utterance works.
	_ = r.current().SendAudio(pcm)
	return nil
}

func (r *autoReconnectStream) Results() <-chan TranscriptEvent {
	return r.out
}

func (r *autoReconnectStream) Finalize() error {
	return r.current().Finalize()
}

func (r *autoReconnectStream) Close() error {
	var err error
	r.closeOnce.Do(func() {
		close(r.closed)
		err = r.current().Close()
	})
	return err
}
