package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// pcm returns n bytes of silence-shaped PCM; content does not matter to a fake
// endpoint, only that the multipart file arrives.
func pcm(n int) []byte { return make([]byte, n) }

func waitForEvent(t *testing.T, s *sarvamRESTAdapter) (TranscriptEvent, bool) {
	t.Helper()
	select {
	case evt, ok := <-s.Results():
		return evt, ok
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a result")
		return TranscriptEvent{}, false
	}
}

// The request must match the documented curl, because a wrong field name fails as
// silence — exactly the failure mode this transport exists to escape.
func TestSarvamRESTSendsTheDocumentedRequest(t *testing.T) {
	var (
		gotKey    string
		gotFields = map[string]string{}
		gotFile   int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-subscription-key")
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		}
		for _, k := range []string{"model", "mode", "sample_rate", "language_code"} {
			gotFields[k] = r.FormValue(k)
		}
		if f, _, err := r.FormFile("file"); err == nil {
			buf := make([]byte, 64<<10)
			n, _ := f.Read(buf)
			gotFile = n
			f.Close()
		} else {
			t.Errorf("FormFile(\"file\") error = %v", err)
		}
		w.Write([]byte(`{"transcript":"blue","language_code":"en-IN","request_id":"req-1"}`))
	}))
	defer srv.Close()
	t.Setenv("SARVAM_STT_REST_URL", srv.URL)

	s := newSarvamRESTAdapter(context.Background(), "test-key", "saaras:v4", "en-IN", "transcribe", 16000)
	if err := s.SendAudio(pcm(3200)); err != nil {
		t.Fatalf("SendAudio() error = %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	evt, ok := waitForEvent(t, s)
	if !ok {
		t.Fatal("result channel closed instead of delivering a transcript")
	}
	if evt.Text != "blue" || !evt.IsFinal || !evt.SpeechEnd {
		t.Fatalf("event = %+v, want final SpeechEnd transcript %q", evt, "blue")
	}
	if gotKey != "test-key" {
		t.Errorf("api-subscription-key = %q, want %q", gotKey, "test-key")
	}
	if gotFields["model"] != "saaras:v4" {
		t.Errorf("model = %q, want saaras:v4", gotFields["model"])
	}
	if gotFields["mode"] != "transcribe" {
		t.Errorf("mode = %q, want transcribe", gotFields["mode"])
	}
	if gotFields["sample_rate"] != "16000" {
		t.Errorf("sample_rate = %q, want 16000", gotFields["sample_rate"])
	}
	if gotFields["language_code"] != "en-IN" {
		t.Errorf("language_code = %q, want en-IN", gotFields["language_code"])
	}
	// A WAV header is 44 bytes, so the body must exceed the raw PCM length —
	// the streaming path sent headerless PCM while declaring audio/wav.
	if gotFile <= 3200 {
		t.Errorf("uploaded file = %d bytes, want more than the 3200 raw PCM bytes (WAV header missing?)", gotFile)
	}
}

// An HTTP error must surface and emit nothing, rather than being mistaken for a
// transcript or hanging the turn.
func TestSarvamRESTErrorStatusEmitsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()
	t.Setenv("SARVAM_STT_REST_URL", srv.URL)

	s := newSarvamRESTAdapter(context.Background(), "bad", "saaras:v4", "en-IN", "transcribe", 16000)
	_ = s.SendAudio(pcm(3200))
	_ = s.Finalize()

	// Close waits for the in-flight request, then closes the channel: a closed
	// channel with no event is the correct outcome.
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if evt, ok := <-s.Results(); ok {
		t.Fatalf("got event %+v, want none on a 401", evt)
	}
}

func TestSarvamRESTEmptyTranscriptEmitsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"transcript":"   ","request_id":"req-2"}`))
	}))
	defer srv.Close()
	t.Setenv("SARVAM_STT_REST_URL", srv.URL)

	s := newSarvamRESTAdapter(context.Background(), "k", "saaras:v4", "en-IN", "transcribe", 16000)
	_ = s.SendAudio(pcm(1600))
	_ = s.Finalize()
	_ = s.Close()

	if evt, ok := <-s.Results(); ok {
		t.Fatalf("got event %+v, want none for an empty transcript", evt)
	}
}

// Finalize with nothing buffered must not POST at all.
func TestSarvamRESTFinalizeWithNoAudioDoesNotCall(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"transcript":"x"}`))
	}))
	defer srv.Close()
	t.Setenv("SARVAM_STT_REST_URL", srv.URL)

	s := newSarvamRESTAdapter(context.Background(), "k", "saaras:v4", "en-IN", "transcribe", 16000)
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	_ = s.Close()
	if calls != 0 {
		t.Fatalf("requests = %d, want 0 when no audio was buffered", calls)
	}
}

func TestSarvamTransportSelection(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{{"", false}, {"rest", true}, {"REST", true}, {"batch", true}, {"http", true}, {"ws", false}, {"streaming", false}} {
		t.Setenv("SARVAM_STT_TRANSPORT", tt.value)
		if got := sarvamTransportIsREST(); got != tt.want {
			t.Errorf("SARVAM_STT_TRANSPORT=%q -> %v, want %v", tt.value, got, tt.want)
		}
	}
}
