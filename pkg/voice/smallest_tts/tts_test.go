package smallest_tts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// drain reads a stream to EOF and returns the concatenated audio.
func drain(t *testing.T, s AudioStream) []byte {
	t.Helper()
	var out []byte
	for {
		b, err := s.Read()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		out = append(out, b...)
	}
}

func TestSynthesizeReturnsPCMFromBatchEndpoint(t *testing.T) {
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	var gotPath, gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(want)
	}))
	defer srv.Close()

	tts := NewSmallestTTS(TTSConfig{
		APIKey: "k", VoiceID: "siya", ModelID: "lightning_v3.1",
		OutputFormat: "pcm_24000", SampleRateHz: 24000, BaseURL: srv.URL,
	})
	stream, err := tts.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	defer stream.Close()

	if got := drain(t, stream); string(got) != string(want) {
		t.Fatalf("audio = %v, want %v", got, want)
	}
	// The REST path spells the model with hyphens even though config uses underscores.
	if gotPath != "/api/v1/lightning-v3.1/get_speech" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotBody["voice_id"] != "siya" {
		t.Fatalf("voice_id = %v", gotBody["voice_id"])
	}
	if gotBody["sample_rate"] != float64(24000) {
		t.Fatalf("sample_rate = %v", gotBody["sample_rate"])
	}
}

func TestSynthesizeDefaultsVoiceAndModel(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte{0x00, 0x01})
	}))
	defer srv.Close()

	tts := NewSmallestTTS(TTSConfig{APIKey: "k", BaseURL: srv.URL})
	stream, err := tts.Synthesize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	defer stream.Close()
	drain(t, stream)

	if gotBody["voice_id"] != defaultVoiceID {
		t.Fatalf("voice_id = %v, want %q", gotBody["voice_id"], defaultVoiceID)
	}
	if !strings.Contains(gotPath, "lightning-v3.1") {
		t.Fatalf("path = %q, want default model", gotPath)
	}
}

func TestSynthesizeSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"bad key"}`))
	}))
	defer srv.Close()

	tts := NewSmallestTTS(TTSConfig{APIKey: "k", BaseURL: srv.URL})
	if _, err := tts.Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error for a 401 response")
	} else if !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error should carry the server message, got %v", err)
	}
}

func TestSynthesizeRequiresAPIKey(t *testing.T) {
	tts := NewSmallestTTS(TTSConfig{})
	if _, err := tts.Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error when the api key is empty")
	}
}

func TestBuildSpeechURLDefaults(t *testing.T) {
	got, err := buildSpeechURL(TTSConfig{BaseURL: defaultBaseURL, ModelID: "lightning_v3.1"})
	if err != nil {
		t.Fatalf("buildSpeechURL: %v", err)
	}
	want := "https://waves-api.smallest.ai/api/v1/lightning-v3.1/get_speech"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
