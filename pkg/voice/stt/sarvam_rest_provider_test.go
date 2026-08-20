package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// pcmOf returns n bytes of PCM filled with b, so a test can tell whose audio
// reached the server (after the 44-byte WAV header).
func pcmOf(n int, b byte) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = b
	}
	return buf
}

func waitForRESTEvent(t *testing.T, s *sarvamRESTAdapter) (TranscriptEvent, bool) {
	t.Helper()
	select {
	case evt, ok := <-s.Results():
		return evt, ok
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a result")
		return TranscriptEvent{}, false
	}
}

func newTestAdapter(t *testing.T, url string) *sarvamRESTAdapter {
	t.Helper()
	t.Setenv("SARVAM_STT_REST_URL", url)
	return newSarvamRESTAdapter(context.Background(), "test-key", "saaras:v4", "unknown", "transcribe", 16000)
}

// The request must match the documented curl, because a wrong field name fails
// as silence — exactly the failure mode this transport exists to escape.
func TestSarvamRESTSendsTheDocumentedRequest(t *testing.T) {
	var (
		gotKey     string
		gotFields  = map[string]string{}
		gotFile    int
		sampleRate string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-subscription-key")
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		}
		for _, k := range []string{"model", "mode", "language_code"} {
			gotFields[k] = r.FormValue(k)
		}
		sampleRate = r.FormValue("sample_rate")
		if f, _, err := r.FormFile("file"); err == nil {
			buf := make([]byte, 64<<10)
			n, _ := f.Read(buf)
			gotFile = n
			f.Close()
		} else {
			t.Errorf("FormFile(\"file\") error = %v", err)
		}
		w.Write([]byte(`{"transcript":"blue","language_code":"hi-IN","request_id":"req-1"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	if err := s.SendAudio(pcmOf(3200, 1)); err != nil {
		t.Fatalf("SendAudio() error = %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	evt, ok := waitForRESTEvent(t, s)
	if !ok {
		t.Fatal("result channel closed instead of delivering a transcript")
	}
	if evt.Text != "blue" || !evt.IsFinal || !evt.SpeechEnd {
		t.Fatalf("event = %+v, want final SpeechEnd transcript %q", evt, "blue")
	}
	if evt.Language != "hi-IN" {
		t.Errorf("Language = %q, want detected hi-IN", evt.Language)
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
	if gotFields["language_code"] != "unknown" {
		t.Errorf("language_code = %q, want unknown", gotFields["language_code"])
	}
	// Dropped after issue 001: accepted by the API but undocumented.
	if sampleRate != "" {
		t.Errorf("sample_rate = %q, want field absent", sampleRate)
	}
	// A WAV header is 44 bytes, so the body must exceed the raw PCM length.
	if gotFile <= 3200 {
		t.Errorf("uploaded file = %d bytes, want more than the 3200 raw PCM bytes (WAV header missing?)", gotFile)
	}
}

// A 4xx is deterministic: no retry, nothing emitted, no callback.
func TestSarvamRESTClientErrorEmitsNothingWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	var emptyCalls atomic.Int32
	s.SetEmptyResultHandler(func() { emptyCalls.Add(1) })
	_ = s.SendAudio(pcmOf(32000, 1))
	_ = s.Finalize()

	// Close waits for the in-flight request, then closes the channel: a closed
	// channel with no event is the correct outcome.
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if evt, ok := <-s.Results(); ok {
		t.Fatalf("got event %+v, want none on a 401", evt)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want exactly 1 (no retry on 4xx)", got)
	}
	if emptyCalls.Load() != 0 {
		t.Errorf("empty-result handler fired on an HTTP error; must fire only on blank transcripts")
	}
}

// A 5xx retries once; success on the second attempt still delivers the event.
func TestSarvamRESTRetriesOnceOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"transcript":"try again worked","language_code":"en-IN"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	_ = s.SendAudio(pcmOf(32000, 1))
	_ = s.Finalize()

	evt, ok := waitForRESTEvent(t, s)
	if !ok || evt.Text != "try again worked" {
		t.Fatalf("event = %+v ok=%v, want the retried transcript", evt, ok)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", got)
	}
}

// Both attempts failing emits nothing and leaves the stream usable for the
// next turn.
func TestSarvamRESTRetryThenFailLeavesStreamUsable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"transcript":"recovered","language_code":"en-IN"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	_ = s.SendAudio(pcmOf(32000, 1))
	_ = s.Finalize()
	s.inFlight.Wait() // let the failing turn finish

	select {
	case evt := <-s.Results():
		t.Fatalf("got event %+v, want none after retry-then-fail", evt)
	default:
	}

	// Next turn on the same stream succeeds.
	_ = s.SendAudio(pcmOf(32000, 2))
	_ = s.Finalize()
	evt, ok := waitForRESTEvent(t, s)
	if !ok || evt.Text != "recovered" {
		t.Fatalf("event = %+v ok=%v, want the next turn's transcript", evt, ok)
	}
}

// Finalize with nothing buffered must not POST at all, and a CANCELLED turn
// must also stay silent.
//
// This test used to reach the silence by finalizing an empty buffer with no
// cancel at all — conflating "nothing captured" with "child cancelled", which
// is precisely the assumption that left a silent turn unanswered. It now says
// cancel explicitly; the un-cancelled case is covered by
// TestEmptyFinalizeAnnouncesUnlessCancelled.
func TestSarvamRESTFinalizeWithNoAudioDoesNotCall(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"transcript":"x"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	var emptyCalls atomic.Int32
	s.SetEmptyResultHandler(func() { emptyCalls.Add(1) })
	s.CancelTurn()
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	_ = s.Close()
	if calls.Load() != 0 {
		t.Fatalf("requests = %d, want 0 when no audio was buffered", calls.Load())
	}
	if emptyCalls.Load() != 0 {
		t.Fatalf("empty-result handler fired on an empty buffer; Cancel Turn must stay silent")
	}
}

// Real audio that transcribes to blank fires the empty-result handler instead
// of emitting an event — the pipeline answers with "I didn't hear you".
func TestSarvamRESTBlankTranscriptFiresEmptyHandler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"transcript":"   ","request_id":"req-2"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	var emptyCalls atomic.Int32
	s.SetEmptyResultHandler(func() { emptyCalls.Add(1) })
	// 0.1s of audio: even the briefest real tap must fire the handler — a
	// duration floor here silently strands the device in "Thinking…".
	_ = s.SendAudio(pcmOf(3200, 1))
	_ = s.Finalize()
	s.inFlight.Wait()

	select {
	case evt := <-s.Results():
		t.Fatalf("got event %+v, want none for a blank transcript", evt)
	default:
	}
	if emptyCalls.Load() != 1 {
		t.Fatalf("empty-result handler calls = %d, want 1", emptyCalls.Load())
	}
}

// The buffer stops at the REST endpoint's 30s limit instead of growing a
// request that is guaranteed a 400.
func TestSarvamRESTBufferCap(t *testing.T) {
	var gotFile int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		}
		if f, _, err := r.FormFile("file"); err == nil {
			buf := make([]byte, 1<<20)
			total := 0
			for {
				n, readErr := f.Read(buf)
				total += n
				if readErr != nil {
					break
				}
			}
			gotFile = total
			f.Close()
		}
		w.Write([]byte(`{"transcript":"capped","language_code":"en-IN"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	cap := sarvamRESTMaxSeconds * 16000 * 2
	// 35 seconds offered against a 30 second cap.
	for i := 0; i < 35; i++ {
		_ = s.SendAudio(pcmOf(16000*2, 1))
	}
	_ = s.Finalize()

	if _, ok := waitForRESTEvent(t, s); !ok {
		t.Fatal("expected the capped clip to still transcribe")
	}
	if gotFile != cap+44 {
		t.Errorf("uploaded file = %d bytes, want capped PCM %d + 44 WAV header", gotFile, cap)
	}
}

// ResetBuffer discards buffered audio so the next finalize sees only new audio.
func TestSarvamRESTResetBufferDiscardsAudio(t *testing.T) {
	var firstByte byte
	var gotFile int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		}
		if f, _, err := r.FormFile("file"); err == nil {
			buf := make([]byte, 64<<10)
			n, _ := f.Read(buf)
			gotFile = n
			if n > 44 {
				firstByte = buf[44] // first PCM byte after the WAV header
			}
			f.Close()
		}
		w.Write([]byte(`{"transcript":"second","language_code":"en-IN"}`))
	}))
	defer srv.Close()

	s := newTestAdapter(t, srv.URL)
	_ = s.SendAudio(pcmOf(3200, 1))
	s.ResetBuffer()
	_ = s.SendAudio(pcmOf(1600, 2))
	_ = s.Finalize()

	if _, ok := waitForRESTEvent(t, s); !ok {
		t.Fatal("expected a transcript for the post-reset audio")
	}
	if gotFile != 1600+44 {
		t.Errorf("uploaded file = %d bytes, want only the post-reset 1600 + 44 header", gotFile)
	}
	if firstByte != 2 {
		t.Errorf("first PCM byte = %d, want 2 (pre-reset audio leaked)", firstByte)
	}
}

// The factory resolves sarvam_rest by name and applies the config row's key and
// model, like any other provider.
func TestSarvamRESTResolvableThroughFactory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"transcript":"via factory","language_code":"en-IN"}`))
	}))
	defer srv.Close()
	t.Setenv("SARVAM_STT_REST_URL", srv.URL)

	factory, err := NewFactoryFromProviders([]ProviderInfo{{
		Name: "sarvam_rest", APIKey: "row-key", Model: "saaras:v4", IsActive: true, Priority: 1,
	}})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders() error = %v", err)
	}
	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider() error = %v", err)
	}
	if provider.Name() != "sarvam_rest" {
		t.Fatalf("provider = %q, want sarvam_rest", provider.Name())
	}
	stream, err := provider.OpenStream(context.Background(), StreamOptions{SampleRate: 16000})
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	defer stream.Close()
	_ = stream.SendAudio(pcmOf(3200, 1))
	_ = stream.Finalize()
	evt, ok := waitForRESTEvent(t, stream.(*sarvamRESTAdapter))
	if !ok || evt.Text != "via factory" {
		t.Fatalf("event = %+v ok=%v, want the factory-configured transcript", evt, ok)
	}
}

// Without a key the provider refuses to open rather than failing every turn.
func TestSarvamRESTOpenStreamRequiresKey(t *testing.T) {
	p := NewSarvamRESTProvider("", "")
	if _, err := p.OpenStream(context.Background(), StreamOptions{}); err == nil {
		t.Fatal("OpenStream() with no key succeeded, want error")
	}
}

// An empty Finalize has two causes that want opposite endings, and only one of
// them was served before: a cancelled turn stayed silent (correct), and a turn
// that captured nothing ALSO stayed silent (a dead-looking toy). Observed
// 2026-08-20: the log ended at "finalize with no audio buffered" and the
// session never answered.
func TestEmptyFinalizeAnnouncesUnlessCancelled(t *testing.T) {
	t.Run("silent turn is announced", func(t *testing.T) {
		s := newTestAdapter(t, "http://unused.invalid")
		defer s.Close()
		var empties atomic.Int32
		s.SetEmptyResultHandler(func() { empties.Add(1) })

		// The child tapped, said nothing, tapped again: press resets, no audio
		// arrives, End Turn finalizes.
		s.ResetBuffer()
		if err := s.Finalize(); err != nil {
			t.Fatalf("Finalize() error = %v", err)
		}
		if got := empties.Load(); got != 1 {
			t.Errorf("empty-handler calls = %d, want 1 (the child must get a reply)", got)
		}
	})

	t.Run("cancelled turn stays silent", func(t *testing.T) {
		s := newTestAdapter(t, "http://unused.invalid")
		defer s.Close()
		var empties atomic.Int32
		s.SetEmptyResultHandler(func() { empties.Add(1) })

		_ = s.SendAudio(pcmOf(16000, 1))
		s.CancelTurn()
		if err := s.Finalize(); err != nil {
			t.Fatalf("Finalize() error = %v", err)
		}
		if got := empties.Load(); got != 0 {
			t.Errorf("empty-handler calls = %d, want 0 on a deliberate cancel", got)
		}
	})

	t.Run("the cancel flag is one-shot", func(t *testing.T) {
		s := newTestAdapter(t, "http://unused.invalid")
		defer s.Close()
		var empties atomic.Int32
		s.SetEmptyResultHandler(func() { empties.Add(1) })

		s.CancelTurn()
		_ = s.Finalize() // consumes the flag, stays silent
		// A LATER silent turn must not inherit the cancel and go quiet too.
		_ = s.Finalize()
		if got := empties.Load(); got != 1 {
			t.Errorf("empty-handler calls = %d, want 1 (cancel must not persist)", got)
		}
	})

	t.Run("a plain buffer reset does not suppress the announcement", func(t *testing.T) {
		s := newTestAdapter(t, "http://unused.invalid")
		defer s.Close()
		var empties atomic.Int32
		s.SetEmptyResultHandler(func() { empties.Add(1) })

		// ResetBuffer is the fresh-press path; it must not be mistaken for a
		// cancel, or every silent turn after a press would go unanswered.
		_ = s.SendAudio(pcmOf(16000, 1))
		s.ResetBuffer()
		_ = s.Finalize()
		if got := empties.Load(); got != 1 {
			t.Errorf("empty-handler calls = %d, want 1 after a plain reset", got)
		}
	})
}
