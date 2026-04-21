package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestBuildXAIURL(t *testing.T) {
	got, err := buildXAIURL(config{
		wsURL:          "ws://example.test/v1/stt",
		language:       "en",
		interimResults: true,
		endpointingMS:  500,
		diarize:        true,
	}, wavInfo{
		SampleRate: 16000,
		Channels:   2,
	})
	if err != nil {
		t.Fatalf("buildXAIURL failed: %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL: %v", err)
	}
	q := u.Query()
	assertQuery := func(key, want string) {
		t.Helper()
		if value := q.Get(key); value != want {
			t.Fatalf("query %s = %q, want %q", key, value, want)
		}
	}
	assertQuery("sample_rate", "16000")
	assertQuery("encoding", "pcm")
	assertQuery("interim_results", "true")
	assertQuery("endpointing", "500")
	assertQuery("language", "en")
	assertQuery("diarize", "true")
	assertQuery("multichannel", "true")
	assertQuery("channels", "2")
}

func TestParseWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(path, testWAV(16000, 1, []byte{0, 0, 1, 0}), 0o600); err != nil {
		t.Fatalf("write test wav: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open test wav: %v", err)
	}
	defer file.Close()

	info, err := parseWAV(file)
	if err != nil {
		t.Fatalf("parseWAV failed: %v", err)
	}
	if info.SampleRate != 16000 || info.Channels != 1 || info.BitsPerSample != 16 || info.DataSize != 4 {
		t.Fatalf("wav info = %+v, want 16kHz mono PCM16 with 4 bytes", info)
	}
}

func TestRunStreamsWAVToMockXAI(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(audioPath, testWAV(16000, 1, []byte("pcm-audio")), 0o600); err != nil {
		t.Fatalf("write test wav: %v", err)
	}

	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer test-key", auth)
		}
		if r.URL.Query().Get("sample_rate") != "16000" {
			t.Fatalf("sample_rate = %q, want 16000", r.URL.Query().Get("sample_rate"))
		}

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
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if messageType != websocket.BinaryMessage || string(data) != "pcm-audio" {
			t.Fatalf("audio message type=%d data=%q, want binary pcm-audio", messageType, string(data))
		}

		_, done, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		var doneMsg map[string]string
		if err := json.Unmarshal(done, &doneMsg); err != nil {
			errCh <- err
			return
		}
		if doneMsg["type"] != "audio.done" {
			t.Fatalf("done message = %q, want audio.done", doneMsg["type"])
		}

		if err := conn.WriteJSON(map[string]any{
			"type":      "transcript.partial",
			"text":      "hello",
			"is_final":  true,
			"duration":  0.5,
			"message":   "",
			"finalized": true,
		}); err != nil {
			errCh <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"type":     "transcript.done",
			"text":     "",
			"duration": 0.75,
		}); err != nil {
			errCh <- err
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	wsURL := "ws" + server.URL[len("http"):]
	err := run(context.Background(), []string{
		"-api-key", "test-key",
		"-url", wsURL,
		"-audio", audioPath,
		"-language", "en",
	}, &out)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if output := out.String(); !strings.Contains(output, "Server ready") || !strings.Contains(output, "Full transcript: hello") {
		t.Fatalf("output = %q, want ready and stitched final transcript", output)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func testWAV(sampleRate, channels int, pcm []byte) []byte {
	var buf bytes.Buffer
	byteRate := sampleRate * channels * 2
	blockAlign := channels * 2
	dataSize := len(pcm)

	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcm)
	return buf.Bytes()
}
