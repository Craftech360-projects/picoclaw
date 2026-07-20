package stt

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeStream struct {
	ch     chan TranscriptEvent
	closed bool
	mu     sync.Mutex
}

func (f *fakeStream) SendAudio(pcm []byte) error { return nil }
func (f *fakeStream) Results() <-chan TranscriptEvent {
	return f.ch
}
func (f *fakeStream) Finalize() error { return nil }
func (f *fakeStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.ch)
	}
	return nil
}

type fakeProvider struct {
	mu      sync.Mutex
	streams []*fakeStream
}

func (p *fakeProvider) Name() string                       { return "fake" }
func (p *fakeProvider) Capabilities() ProviderCapabilities { return ProviderCapabilities{} }
func (p *fakeProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &fakeStream{ch: make(chan TranscriptEvent, 4)}
	p.streams = append(p.streams, s)
	return s, nil
}

func TestAutoReconnectStreamSurvivesDisconnect(t *testing.T) {
	p := &fakeProvider{}
	r, err := NewAutoReconnectStream(context.Background(), p, StreamOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	p.mu.Lock()
	first := p.streams[0]
	p.mu.Unlock()

	first.ch <- TranscriptEvent{Text: "before"}
	if evt := <-r.Results(); evt.Text != "before" {
		t.Fatalf("got %q, want before", evt.Text)
	}

	first.Close() // simulate provider dropping the stream

	// Wait for the redial (first backoff is 500ms).
	deadline := time.After(3 * time.Second)
	var second *fakeStream
	for second == nil {
		select {
		case <-deadline:
			t.Fatal("no reconnect within 3s")
		case <-time.After(50 * time.Millisecond):
			p.mu.Lock()
			if len(p.streams) > 1 {
				second = p.streams[1]
			}
			p.mu.Unlock()
		}
	}

	second.ch <- TranscriptEvent{Text: "after"}
	select {
	case evt := <-r.Results():
		if evt.Text != "after" {
			t.Fatalf("got %q, want after", evt.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after reconnect")
	}
}
