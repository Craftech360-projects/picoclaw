package stt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestGeminiProviderCapabilities(t *testing.T) {
	caps := NewGeminiProvider("", "").Capabilities()
	if !caps.SupportsStreaming {
		t.Fatal("gemini STT must report streaming support")
	}
	if !caps.SupportsMultilingual {
		t.Fatal("gemini STT must report multilingual support")
	}
	if caps.SupportsDiarization {
		t.Fatal("the live model has no speaker diarization; capability must be false")
	}
}

func TestGeminiProviderName(t *testing.T) {
	if got := NewGeminiProvider("", "").Name(); got != "gemini" {
		t.Fatalf("Name() = %q, want gemini", got)
	}
}

// Drives a full session against a fake Live API socket: setup handshake, an
// audio frame, an interim, a final, and the flush signal.
func TestGeminiProviderStreamingProtocol(t *testing.T) {
	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 8)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "test-gemini-key" {
			errCh <- fmt.Errorf("key query param = %q, want test-gemini-key", got)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade: %w", err)
			return
		}
		defer conn.Close()

		// 1. setup message
		_, raw, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read setup: %w", err)
			return
		}
		var setup struct {
			Setup struct {
				Model                   string `json:"model"`
				InputAudioTranscription struct {
					LanguageCodes []string `json:"languageCodes"`
				} `json:"inputAudioTranscription"`
				RealtimeInputConfig struct {
					AutomaticActivityDetection struct {
						Disabled bool `json:"disabled"`
					} `json:"automaticActivityDetection"`
				} `json:"realtimeInputConfig"`
			} `json:"setup"`
		}
		if err := json.Unmarshal(raw, &setup); err != nil {
			errCh <- fmt.Errorf("unmarshal setup: %w", err)
			return
		}
		if setup.Setup.Model != "models/gemini-3.5-transcribe-live" {
			errCh <- fmt.Errorf("setup model = %q, want models/gemini-3.5-transcribe-live", setup.Setup.Model)
		}
		// ADR 0007: the device owns the turn boundary, so server VAD is off.
		if !setup.Setup.RealtimeInputConfig.AutomaticActivityDetection.Disabled {
			errCh <- fmt.Errorf("automaticActivityDetection.disabled = false, want true (ADR 0007)")
		}
		if len(setup.Setup.InputAudioTranscription.LanguageCodes) != 1 ||
			setup.Setup.InputAudioTranscription.LanguageCodes[0] != "hi-IN" {
			errCh <- fmt.Errorf("languageCodes = %v, want [hi-IN]", setup.Setup.InputAudioTranscription.LanguageCodes)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`)); err != nil {
			errCh <- fmt.Errorf("write setupComplete: %w", err)
			return
		}

		// 2. one audio frame
		_, raw, err = conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read audio: %w", err)
			return
		}
		var audio struct {
			RealtimeInput struct {
				Audio struct {
					Data     string `json:"data"`
					MimeType string `json:"mimeType"`
				} `json:"audio"`
			} `json:"realtimeInput"`
		}
		if err := json.Unmarshal(raw, &audio); err != nil {
			errCh <- fmt.Errorf("unmarshal audio: %w", err)
			return
		}
		if audio.RealtimeInput.Audio.MimeType != "audio/pcm;rate=16000" {
			errCh <- fmt.Errorf("mimeType = %q, want audio/pcm;rate=16000", audio.RealtimeInput.Audio.MimeType)
		}
		decoded, err := base64.StdEncoding.DecodeString(audio.RealtimeInput.Audio.Data)
		if err != nil {
			errCh <- fmt.Errorf("audio data not base64: %w", err)
		} else if string(decoded) != "PCMPCM" {
			errCh <- fmt.Errorf("decoded audio = %q, want PCMPCM", string(decoded))
		}

		// 3. interim then final
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"serverContent":{"interimInputTranscription":{"text":"AAA"}}}`))
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"serverContent":{"inputTranscription":{"text":"AAA BBB CCC"}}}`))

		// 4. flush
		_, raw, err = conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read flush: %w", err)
			return
		}
		if !strings.Contains(string(raw), `"activityEnd"`) {
			errCh <- fmt.Errorf("flush message = %s, want activityEnd", string(raw))
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))

	stream, err := NewGeminiProvider("test-gemini-key", "").OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       "hi-IN",
		InterimResults: true,
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	if err := stream.SendAudio([]byte("PCMPCM")); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}

	var interim, final TranscriptEvent
	deadline := time.After(5 * time.Second)
	for final.Text == "" {
		select {
		case evt := <-stream.Results():
			if evt.IsFinal {
				final = evt
			} else {
				interim = evt
			}
		case <-deadline:
			t.Fatal("no final transcript within 5s")
		}
	}
	if interim.Text != "AAA" {
		t.Fatalf("interim text = %q, want AAA", interim.Text)
	}
	if final.Text != "AAA BBB CCC" {
		t.Fatalf("final text = %q, want AAA BBB CCC", final.Text)
	}

	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestGeminiProviderRequiresAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	_, err := NewGeminiProvider("", "").OpenStream(context.Background(), StreamOptions{SampleRate: 16000})
	if err == nil {
		t.Fatal("OpenStream with no API key must fail")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Fatalf("error = %v, want it to mention the API key", err)
	}
}

func TestNormalizeGeminiLanguages(t *testing.T) {
	cases := map[string][]string{
		"hi-IN": {"hi-IN"},
		"hi":    {"hi-IN"},
		"en":    {"en-US"},
		"en-IN": {"en-IN"},
		"auto":  {},
		"":      {},
	}
	for in, want := range cases {
		got := normalizeGeminiLanguages(in)
		if len(got) != len(want) {
			t.Fatalf("normalizeGeminiLanguages(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("normalizeGeminiLanguages(%q) = %v, want %v", in, got, want)
			}
		}
	}
}
