package deepgram

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDeepgramStreamSendsKeepAliveDuringSilence(t *testing.T) {
	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 2)
	keepAliveCh := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token test-key" {
			errCh <- fmt.Errorf("Authorization header = %q, want Token test-key", got)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}

			var msg map[string]string
			if err := json.Unmarshal(data, &msg); err != nil {
				errCh <- err
				return
			}
			if msg["type"] == "KeepAlive" {
				keepAliveCh <- struct{}{}
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	transcriber := &DeepgramTranscriber{
		apiKey:            "test-key",
		baseURL:           wsURL,
		dialer:            websocket.DefaultDialer,
		keepAliveInterval: 10 * time.Millisecond,
	}

	stream, err := transcriber.OpenStream(StreamOpts{SampleRate: 16000})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer stream.Close()

	select {
	case <-keepAliveCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Deepgram KeepAlive message")
	}
}
