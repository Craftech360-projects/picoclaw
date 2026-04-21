package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestXAIProviderCapabilities(t *testing.T) {
	provider := NewXAIProvider("", "")

	if provider.Name() != "xai" {
		t.Fatalf("Name() = %q, want xai", provider.Name())
	}

	caps := provider.Capabilities()
	if !caps.SupportsStreaming {
		t.Fatal("xAI STT should support streaming")
	}
	if !caps.SupportsDiarization {
		t.Fatal("xAI STT should support diarization")
	}
	if !caps.SupportsMultilingual {
		t.Fatal("xAI STT should support multilingual transcription")
	}
}

func TestFactory_RegisterBuiltInProvidersIncludesXAI(t *testing.T) {
	factory := &Factory{providers: make(map[string]Provider)}

	factory.registerBuiltInProviders()

	provider, ok := factory.providers["xai"]
	if !ok {
		t.Fatal("built-in STT providers should include xai")
	}
	if provider.Name() != "xai" {
		t.Fatalf("registered provider Name() = %q, want xai", provider.Name())
	}
}

func TestXAIProviderStreamingProtocol(t *testing.T) {
	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-xai-key" {
			errCh <- fmt.Errorf("Authorization header = %q, want Bearer test-xai-key", got)
		}

		q := r.URL.Query()
		assertQuery := func(key, want string) {
			if got := q.Get(key); got != want {
				errCh <- fmt.Errorf("query %s = %q, want %q", key, got, want)
			}
		}
		assertQuery("sample_rate", "16000")
		assertQuery("encoding", "pcm")
		assertQuery("interim_results", "true")
		assertQuery("endpointing", "800")
		assertQuery("language", "en")
		assertQuery("channels", "1")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{"type": "transcript.created"}); err != nil {
			errCh <- err
			return
		}

		messageType, audio, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if messageType != websocket.BinaryMessage {
			errCh <- fmt.Errorf("first client message type = %d, want binary", messageType)
		}
		if string(audio) != "pcm" {
			errCh <- fmt.Errorf("audio payload = %q, want pcm", string(audio))
		}

		if err := conn.WriteJSON(map[string]any{
			"type":         "transcript.partial",
			"text":         "hel",
			"is_final":     false,
			"speech_final": false,
			"duration":     0.5,
		}); err != nil {
			errCh <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"type":         "transcript.partial",
			"text":         "hello",
			"is_final":     true,
			"speech_final": true,
			"duration":     1.2,
		}); err != nil {
			errCh <- err
			return
		}

		_, finalizeData, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		var finalizeMsg map[string]string
		if err := json.Unmarshal(finalizeData, &finalizeMsg); err != nil {
			errCh <- err
			return
		}
		if got := finalizeMsg["type"]; got != "audio.done" {
			errCh <- fmt.Errorf("finalize message type = %q, want audio.done", got)
		}

		if err := conn.WriteJSON(map[string]any{
			"type":     "transcript.done",
			"text":     "hello world",
			"duration": 1.4,
		}); err != nil {
			errCh <- err
			return
		}
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	t.Setenv("XAI_STT_STREAMING_URL", wsURL)

	provider := NewXAIProvider("test-xai-key", "")
	stream, err := provider.OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       "en",
		InterimResults: true,
		EndpointingMS:  800,
	})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendAudio([]byte("pcm")); err != nil {
		t.Fatalf("SendAudio failed: %v", err)
	}

	first := nextXAIEvent(t, stream.Results())
	if first.Text != "hel" || first.IsFinal || first.SpeechEnd {
		t.Fatalf("first event = %+v, want interim text without final flags", first)
	}

	second := nextXAIEvent(t, stream.Results())
	if second.Text != "hello" || !second.IsFinal || !second.SpeechEnd {
		t.Fatalf("second event = %+v, want utterance-final transcript", second)
	}

	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	third := nextXAIEvent(t, stream.Results())
	if third.Text != "hello world" || !third.IsFinal || !third.SpeechEnd {
		t.Fatalf("third event = %+v, want final transcript.done event", third)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestXAIStreamBuffersAudioUntilTranscriptDoneAfterFinalize(t *testing.T) {
	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{"type": "transcript.created"}); err != nil {
			errCh <- err
			return
		}

		messageType, firstAudio, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if messageType != websocket.BinaryMessage || string(firstAudio) != "turn-one" {
			errCh <- fmt.Errorf("first audio message type=%d data=%q, want binary turn-one", messageType, string(firstAudio))
			return
		}

		_, finalizeData, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		var finalizeMsg map[string]string
		if err := json.Unmarshal(finalizeData, &finalizeMsg); err != nil {
			errCh <- err
			return
		}
		if finalizeMsg["type"] != "audio.done" {
			errCh <- fmt.Errorf("message after first audio = %q, want audio.done", finalizeMsg["type"])
			return
		}

		type wsMessage struct {
			messageType int
			data        []byte
			err         error
		}
		nextMessage := make(chan wsMessage, 1)
		go func() {
			messageType, data, err := conn.ReadMessage()
			nextMessage <- wsMessage{messageType: messageType, data: data, err: err}
		}()

		select {
		case msg := <-nextMessage:
			if msg.err != nil {
				errCh <- msg.err
				return
			}
			errCh <- fmt.Errorf("received audio before transcript.done: %q", string(msg.data))
			return
		case <-time.After(100 * time.Millisecond):
		}

		if err := conn.WriteJSON(map[string]any{
			"type":     "transcript.done",
			"text":     "done",
			"duration": 1.0,
		}); err != nil {
			errCh <- err
			return
		}

		msg := <-nextMessage
		if msg.err != nil {
			errCh <- msg.err
			return
		}
		if msg.messageType != websocket.BinaryMessage || string(msg.data) != "turn-two" {
			errCh <- fmt.Errorf("second audio message type=%d data=%q, want binary turn-two", msg.messageType, string(msg.data))
		}
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	t.Setenv("XAI_STT_STREAMING_URL", wsURL)

	stream, err := NewXAIProvider("test-xai-key", "").OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		InterimResults: true,
	})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendAudio([]byte("turn-one")); err != nil {
		t.Fatalf("SendAudio turn one failed: %v", err)
	}
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if err := stream.SendAudio([]byte("turn-two")); err != nil {
		t.Fatalf("SendAudio turn two failed: %v", err)
	}

	doneEvt := nextXAIEvent(t, stream.Results())
	if doneEvt.Text != "done" || !doneEvt.IsFinal || !doneEvt.SpeechEnd {
		t.Fatalf("done event = %+v, want final speech-end event", doneEvt)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestXAIStreamUsesFinalPartialsWhenTranscriptDoneTextIsEmpty(t *testing.T) {
	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{"type": "transcript.created"}); err != nil {
			errCh <- err
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			errCh <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"type":         "transcript.partial",
			"text":         "stitched final text",
			"is_final":     true,
			"speech_final": false,
			"duration":     0.8,
		}); err != nil {
			errCh <- err
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			errCh <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"type":     "transcript.done",
			"text":     "",
			"duration": 0.9,
		}); err != nil {
			errCh <- err
		}
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	t.Setenv("XAI_STT_STREAMING_URL", wsURL)

	stream, err := NewXAIProvider("test-xai-key", "").OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		InterimResults: true,
	})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendAudio([]byte("pcm")); err != nil {
		t.Fatalf("SendAudio failed: %v", err)
	}

	partial := nextXAIEvent(t, stream.Results())
	if partial.Text != "stitched final text" || !partial.IsFinal || partial.SpeechEnd {
		t.Fatalf("partial event = %+v, want final chunk without speech end", partial)
	}

	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	done := nextXAIEvent(t, stream.Results())
	if done.Text != "stitched final text" || !done.IsFinal || !done.SpeechEnd {
		t.Fatalf("done event = %+v, want stitched final text with speech end", done)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestXAIStreamReconnectsAfterUnexpectedClose(t *testing.T) {
	upgrader := websocket.Upgrader{}
	firstDone := make(chan struct{})
	secondAudio := make(chan string, 1)
	errCh := make(chan error, 4)
	var connectionCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectionCount++
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{"type": "transcript.created"}); err != nil {
			errCh <- err
			return
		}

		if connectionCount == 1 {
			if _, data, err := conn.ReadMessage(); err != nil || string(data) != "before-close" {
				errCh <- fmt.Errorf("first connection audio=%q err=%v, want before-close", string(data), err)
				return
			}
			_, finalizeData, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			var finalizeMsg map[string]string
			if err := json.Unmarshal(finalizeData, &finalizeMsg); err != nil {
				errCh <- err
				return
			}
			if finalizeMsg["type"] != "audio.done" {
				errCh <- fmt.Errorf("first connection second message = %q, want audio.done", finalizeMsg["type"])
				return
			}
			close(firstDone)
			_ = conn.UnderlyingConn().Close()
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		secondAudio <- string(data)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	t.Setenv("XAI_STT_STREAMING_URL", wsURL)

	stream, err := NewXAIProvider("test-xai-key", "").OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		InterimResults: true,
	})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendAudio([]byte("before-close")); err != nil {
		t.Fatalf("SendAudio before close failed: %v", err)
	}
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not close first xAI connection")
	}

	for i := 0; i < 50; i++ {
		if err := stream.SendAudio([]byte("after-reconnect")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
		if i == 49 {
			t.Fatal("SendAudio kept failing after unexpected close")
		}
	}

	select {
	case got := <-secondAudio:
		if got != "after-reconnect" {
			t.Fatalf("second connection audio = %q, want after-reconnect", got)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for audio on reconnected xAI stream")
	}
}

func TestXAIProviderOpenStreamRequiresAPIKey(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("XAI_STT_STREAMING_URL", "ws://127.0.0.1:1")

	_, err := NewXAIProvider("", "").OpenStream(context.Background(), StreamOptions{})
	if err == nil {
		t.Fatal("OpenStream should fail when no xAI API key is configured")
	}
}

func TestNormalizeXAILanguage(t *testing.T) {
	tests := map[string]string{
		"":        "",
		"auto":    "",
		"English": "en",
		"Hindi":   "hi",
		"EN-us":   "en-US",
		"bogus":   "",
	}

	for input, want := range tests {
		if got := normalizeXAILanguage(input); got != want {
			t.Fatalf("normalizeXAILanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

func nextXAIEvent(t *testing.T, ch <-chan TranscriptEvent) TranscriptEvent {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("result channel closed before event")
		}
		return evt
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transcript event")
	}
	return TranscriptEvent{}
}

func TestXAIStreamingBaseURLCanBeOverridden(t *testing.T) {
	t.Setenv("XAI_STT_STREAMING_URL", "ws://example.test/v1/stt")

	got, err := url.Parse(xaiStreamingBaseURL())
	if err != nil {
		t.Fatalf("xaiStreamingBaseURL returned invalid URL: %v", err)
	}
	if got.String() != "ws://example.test/v1/stt" {
		t.Fatalf("xaiStreamingBaseURL() = %q, want override", got.String())
	}
}
