package stt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

		// 2. the turn opens, then one audio frame. Audio is only forwarded
		// inside an activity window, so activityStart precedes it.
		_, raw, err = conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read activityStart: %w", err)
			return
		}
		if !strings.Contains(string(raw), `"activityStart"`) {
			errCh <- fmt.Errorf("first post-setup message = %s, want activityStart", string(raw))
		}
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

	// Open the turn first, as a device tap does: SendAudio forwards nothing
	// while no activity window is open, so audio outside a turn never reaches
	// the service.
	stream.(interface{ ResetBuffer() }).ResetBuffer()

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

// A press must open an activity window, and a tap that captured audio but drew
// no transcript must fire the empty-result handler so the child hears
// "I didn't hear you" rather than silence (ADR 0007 / ptt-batch-stt/003).
func TestGeminiEmptyTapFiresHandler(t *testing.T) {
	upgrader := websocket.Upgrader{}
	sawActivityStart := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if strings.Contains(string(raw), `"activityStart"`) {
				select {
				case sawActivityStart <- struct{}{}:
				default:
				}
			}
			// Deliberately answers nothing: this is the empty-tap case.
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "300ms")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	fired := make(chan struct{}, 1)
	stream.(interface{ SetEmptyResultHandler(func()) }).SetEmptyResultHandler(func() {
		fired <- struct{}{}
	})

	stream.(interface{ ResetBuffer() }).ResetBuffer()
	select {
	case <-sawActivityStart:
	case <-time.After(2 * time.Second):
		t.Fatal("ResetBuffer did not send activityStart")
	}

	if err := stream.SendAudio([]byte("PCMPCM")); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("empty-result handler never fired on a tap that produced no transcript")
	}
}

// Replays the real Cancel Turn sequence from room_session.go:554-562 —
// CancelTurn, CancelTurn, then the Finalize driven by the injected SpeechEnd.
// The turn must stay silent even though the audio already reached Google and a
// final comes back, and exactly one activityEnd may leave the adapter: the Live
// API has no open activity window to close a second time.
func TestGeminiCancelTurnSuppressesLateFinal(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	activityEnds := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Answer the end-of-activity with a final, as the real service does.
			if strings.Contains(string(raw), `"activityEnd"`) {
				mu.Lock()
				activityEnds++
				mu.Unlock()
				_ = conn.WriteMessage(websocket.TextMessage,
					[]byte(`{"serverContent":{"inputTranscription":{"text":"should be discarded"}}}`))
			}
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s") // keep the empty handler out of this test

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))
	// The double wipe plus the Finalize the cancel path always drives.
	stream.(interface{ CancelTurn() }).CancelTurn()
	stream.(interface{ CancelTurn() }).CancelTurn()
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	select {
	case evt := <-stream.Results():
		t.Fatalf("cancelled turn emitted %q; want nothing", evt.Text)
	case <-time.After(1500 * time.Millisecond):
		// Correct: the late final was swallowed.
	}

	mu.Lock()
	got := activityEnds
	mu.Unlock()
	if got != 1 {
		t.Fatalf("server saw %d activityEnd messages, want exactly 1", got)
	}
}

// A fresh press after a cancel must transcribe normally again.
//
// Named for what it actually proves: ResetBuffer deliberately does NOT clear
// cancellation (see cancelledGen's comment) — turn 2's own Finalize is what
// releases the suppression, and this asserts turn 2's transcript comes through.
func TestGeminiTurnAfterCancelTranscribes(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if strings.Contains(string(raw), `"activityEnd"`) {
				_ = conn.WriteMessage(websocket.TextMessage,
					[]byte(`{"serverContent":{"inputTranscription":{"text":"second turn"}}}`))
			}
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCM"))
	stream.(interface{ CancelTurn() }).CancelTurn()

	// New press: the suppression must not carry over.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCM"))
	_ = stream.Finalize()

	select {
	case evt := <-stream.Results():
		if evt.Text != "second turn" {
			t.Fatalf("event text = %q, want \"second turn\"", evt.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no transcript on the turn after a cancel")
	}
}

// Review round 1, finding 1: the mandated design says cancellation
// "persists until the next press" so a cancelled utterance can never be
// answered — but a bare bool cleared by ResetBuffer doesn't fully achieve
// that. Google's final for a cancelled turn arrives asynchronously, well
// after activityEnd; if the child presses again in that gap — the likeliest
// thing to happen right after cancelling — the old implementation cleared
// suppression on the fresh press, before the stale final landed, and let it
// leak out. It also stamped that stale final's gotFinal onto the NEW turn,
// silencing that turn's own legitimate "I didn't hear you" reply.
//
// This replays exactly that timing: cancel turn 1, press again for turn 2
// before turn 1's stale final arrives, and confirm (a) the stale final is
// never emitted, (b) turn 2's empty-tap reply still fires despite the stale
// final's arrival, and (c) a normal turn 3 afterward still transcribes
// correctly — suppression must not leak forward past turn 2 either.
func TestGeminiFastPressAfterCancelSuppressesStaleFinal(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	activityEnds := 0
	staleSent := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if !strings.Contains(string(raw), `"activityEnd"`) {
				continue
			}
			mu.Lock()
			activityEnds++
			n := activityEnds
			mu.Unlock()
			switch n {
			case 1:
				// Turn 1's End (from the cancel). Its final is deliberately
				// delayed until after the child has already pressed again —
				// that delay is the vulnerable window this test targets.
				go func() {
					time.Sleep(150 * time.Millisecond)
					_ = conn.WriteMessage(websocket.TextMessage,
						[]byte(`{"serverContent":{"inputTranscription":{"text":"stale from turn one"}}}`))
					close(staleSent)
				}()
			case 2:
				// Turn 2's End: a genuine empty tap. No reply, ever.
			case 3:
				// Turn 3's End: a real transcript.
				_ = conn.WriteMessage(websocket.TextMessage,
					[]byte(`{"serverContent":{"inputTranscription":{"text":"turn three final"}}}`))
			}
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "300ms")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// A persistent drain, not a one-shot select: this test needs to prove a
	// negative (the stale text never arrives) across the whole timeline, not
	// just at one checkpoint.
	var recvMu sync.Mutex
	var received []string
	turn3Final := make(chan struct{}, 1)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for evt := range stream.Results() {
			recvMu.Lock()
			received = append(received, evt.Text)
			recvMu.Unlock()
			if evt.Text == "turn three final" {
				select {
				case turn3Final <- struct{}{}:
				default:
				}
			}
		}
	}()

	emptyFired := make(chan struct{}, 1)
	stream.(interface{ SetEmptyResultHandler(func()) }).SetEmptyResultHandler(func() {
		select {
		case emptyFired <- struct{}{}:
		default:
		}
	})

	// Turn 1: press, speak, cancel — the driver's real double-cancel sequence.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))
	stream.(interface{ CancelTurn() }).CancelTurn()
	stream.(interface{ CancelTurn() }).CancelTurn()
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize (turn 1): %v", err)
	}

	// Turn 2: the fast press, landing in the gap before turn 1's stale final
	// arrives. Suppression must survive this press.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))

	select {
	case <-staleSent:
	case <-time.After(2 * time.Second):
		t.Fatal("server never sent the stale final")
	}
	// Give the stale final time to reach and be processed by readLoop before
	// turn 2 ends — this is the exact window the finding describes.
	time.Sleep(100 * time.Millisecond)

	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize (turn 2): %v", err)
	}

	// (c) Turn 2 produced no real transcript; its empty-tap reply must still
	// fire despite the stale final having arrived while cancellation was
	// outstanding.
	select {
	case <-emptyFired:
	case <-time.After(2 * time.Second):
		t.Fatal("turn 2's empty-result handler never fired: the stale final from the cancelled turn suppressed it")
	}

	// Turn 3: a normal turn after the cancel — must transcribe like nothing
	// happened, proving suppression doesn't leak forward past turn 2 either.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize (turn 3): %v", err)
	}

	// (b) the new turn's own final IS emitted.
	select {
	case <-turn3Final:
	case <-time.After(3 * time.Second):
		t.Fatal("turn 3's own final was never emitted")
	}

	stream.Close()
	<-drainDone

	// (a) the stale final is NOT emitted, at any point across the whole run.
	recvMu.Lock()
	defer recvMu.Unlock()
	for _, text := range received {
		if text == "stale from turn one" {
			t.Fatalf("stale final from the cancelled turn was emitted: %v", received)
		}
	}
}

// Review round 1, finding 2: Finalize goes to real trouble to send exactly
// one activityEnd per generation, on the stated grounds that the Live API has
// no open activity window to close twice — but ResetBuffer wrote
// activityStart unconditionally, with no matching guard, so an unbalanced
// open was reachable: two presses with no intervening Finalize
// (room_session.go:541-544 resets the buffer on every press), among other
// paths. This asserts the server never sees two activityStart messages
// without an activityEnd between them, replaying the plain double-press case.
func TestGeminiResetBufferClosesStaleActivityWindow(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	var sequence []string // "start" or "end", in wire arrival order

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			mu.Lock()
			switch {
			case strings.Contains(string(raw), `"activityStart"`):
				sequence = append(sequence, "start")
			case strings.Contains(string(raw), `"activityEnd"`):
				sequence = append(sequence, "end")
			}
			mu.Unlock()
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	reset := stream.(interface{ ResetBuffer() }).ResetBuffer
	// Three presses with no Finalize between any of them — the real driver
	// does exactly this on every "press" (room_session.go:541-544).
	reset()
	reset()
	reset()

	// A legitimate End Turn afterward must still work normally.
	_ = stream.SendAudio([]byte("PCM"))
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Let the last activityEnd land on the server before inspecting the log.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	seq := append([]string(nil), sequence...)
	mu.Unlock()

	open := false
	starts := 0
	for i, tok := range seq {
		if tok == "start" {
			if open {
				t.Fatalf("two activityStart messages with no activityEnd between them at position %d: %v", i, seq)
			}
			open = true
			starts++
		} else {
			open = false
		}
	}
	if starts < 3 {
		t.Fatalf("expected at least 3 activityStart messages (one per press), got %d: %v", starts, seq)
	}
}

// Review round 2: round 1's fix suppressed EVERY event while a cancellation
// was outstanding, not just finals. cancelledGen stays outstanding from a
// cancel all the way through the following turn's own Finalize, so that
// blanket suppression silently ate the following turn's own interims too —
// which the pipeline depends on for barge-in (audio_pipeline.go:1790,
// 1841-1870) and as the finalize-timeout safety net text
// (audio_pipeline.go:1679-1687, 1837). Only finals should be suppressed.
//
// This replays exactly that gap: turn 1 is cancelled (leaving cancelledGen
// outstanding), turn 2 presses immediately after, and turn 2's own interim —
// arriving before turn 2's own Finalize, i.e. while the old turn's
// cancellation is still outstanding — must still reach Results().
func TestGeminiInterimAfterCancelReachesResults(t *testing.T) {
	upgrader := websocket.Upgrader{}
	audioCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Deliberately silent on activityEnd: this test is only about
			// the interim, and turn 2 must stay a "no final yet" turn so
			// cancelledGen (set by turn 1's cancel) is still outstanding
			// when turn 2's interim is checked.
			if strings.Contains(string(raw), `"audio"`) {
				audioCount++
				// Only respond to turn 2's audio (the second frame), with
				// distinct text — turn 1's audio must elicit nothing, or a
				// stray reply to it could let this test pass without ever
				// exercising the suppression-while-outstanding path.
				if audioCount == 2 {
					_ = conn.WriteMessage(websocket.TextMessage,
						[]byte(`{"serverContent":{"interimInputTranscription":{"text":"turn two interim"}}}`))
				}
			}
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// Turn 1: press, speak, cancel — leaves cancelledGen outstanding.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))
	stream.(interface{ CancelTurn() }).CancelTurn()
	stream.(interface{ CancelTurn() }).CancelTurn()
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize (turn 1): %v", err)
	}

	// Turn 2: a fresh press while turn 1's cancellation is still outstanding
	// (cleared only by turn 2's own Finalize, deliberately not called here).
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	if err := stream.SendAudio([]byte("PCMPCM")); err != nil {
		t.Fatalf("SendAudio (turn 2): %v", err)
	}

	select {
	case evt := <-stream.Results():
		if evt.IsFinal || evt.Text != "turn two interim" {
			t.Fatalf("event = %+v, want a non-final \"turn two interim\"", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn 2's own interim never reached Results(): an outstanding cancellation suppressed it")
	}
}

// Finding 4: ResetBuffer runs on the data-message goroutine and Finalize on
// RunInbound's, so they really do overlap. When each mutated state under
// turnMu, released it, and only then wrote, a press landing in that gap put
// turn 2's activityStart on the wire ahead of turn 1's activityEnd — closing
// the new window the instant it opened, with both adapters' flags insisting
// nothing was wrong. The wire order must match the state transitions, so
// neither an unbalanced start nor an unbalanced end may ever appear.
func TestGeminiConcurrentPressAndFinalizeKeepWireOrder(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	var sequence []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			mu.Lock()
			switch {
			case strings.Contains(string(raw), `"activityStart"`):
				sequence = append(sequence, "start")
			case strings.Contains(string(raw), `"activityEnd"`):
				sequence = append(sequence, "end")
			}
			mu.Unlock()
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	reset := stream.(interface{ ResetBuffer() }).ResetBuffer
	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			reset()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_ = stream.Finalize()
		}
	}()
	wg.Wait()
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	seq := append([]string(nil), sequence...)
	mu.Unlock()

	// Every critical section emits either [end?, start] (ResetBuffer) or [end]
	// (Finalize, once per generation), and holding turnMu across the write
	// makes the wire order equal the section order. Two consequences follow,
	// and both are violated by the interleavings the unlocked writes allowed:
	// a start never lands on an already-open window, and two ends never land
	// back to back (a ResetBuffer's stale-close is always followed by its own
	// start, and a second Finalize cannot write until a ResetBuffer has).
	open := false
	for i, tok := range seq {
		from := i - 6
		if from < 0 {
			from = 0
		}
		if tok == "start" {
			if open {
				t.Fatalf("activityStart on an already-open window at position %d: %v", i, seq[from:i+1])
			}
			open = true
			continue
		}
		if i > 0 && seq[i-1] == "end" {
			t.Fatalf("two activityEnd messages back to back at position %d: %v", i, seq[from:i+1])
		}
		open = false
	}
	if len(seq) < rounds {
		t.Fatalf("only %d boundary messages reached the server across %d rounds", len(seq), rounds)
	}
}

// The Live API caps a session at 10 minutes. The adapter must retire itself
// just before that so the pipeline's reopen path runs on our schedule rather
// than on a mid-utterance server close.
func TestGeminiStreamRetiresBeforeSessionCap(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_SESSION_TTL", "300ms")
	// Retirement always waits one retire grace (2x this) for a final in flight,
	// even with no window open — the live endpoint delivers finals ~0.54s after
	// activityEnd, so a TTL landing in that gap must not discard one.
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "100ms")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	select {
	case _, open := <-stream.Results():
		if open {
			t.Fatal("expected the results channel to close at the TTL, got an event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not retire itself within 3s of a 300ms TTL")
	}
}

func TestGeminiSessionTTLDefault(t *testing.T) {
	t.Setenv("GEMINI_STT_SESSION_TTL", "")
	if got := geminiSessionTTL(); got != 9*time.Minute {
		t.Fatalf("geminiSessionTTL() = %v, want 9m", got)
	}
}

// The backstop must land inside the server's own 10-minute cap.
func TestGeminiRetireBackstopDefault(t *testing.T) {
	t.Setenv("GEMINI_STT_RETIRE_BACKSTOP", "")
	if got := geminiRetireBackstop(); got != 45*time.Second {
		t.Fatalf("geminiRetireBackstop() = %v, want 45s", got)
	}
	if total := geminiSessionTTL() + geminiRetireBackstop(); total >= 10*time.Minute {
		t.Fatalf("TTL+backstop = %v, must stay under the server's 10m session cap", total)
	}
}

// geminiTapServer is a fake Live API socket that only completes the handshake
// and then reads. Enough for the retirement tests, which are about when the
// adapter closes, not about transcripts.
func geminiTapServer(t *testing.T) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
}

// resultsClosed reports whether the stream retired within the timeout.
func resultsClosed(t *testing.T, stream TranscriptionStream, timeout time.Duration) bool {
	t.Helper()
	select {
	case _, open := <-stream.Results():
		if open {
			t.Fatal("expected the results channel to close, got an event")
		}
		return true
	case <-time.After(timeout):
		return false
	}
}

// Critical 2: retiring on a bare timer closes the socket mid-utterance — the
// exact failure the TTL exists to prevent, just moved to our side of the wire.
// A TTL that elapses with an activity window open must defer to the turn
// boundary, and take the retirement there.
func TestGeminiRetirementDeferredUntilTurnBoundary(t *testing.T) {
	geminiTapServer(t)
	t.Setenv("GEMINI_STT_SESSION_TTL", "150ms")
	t.Setenv("GEMINI_STT_RETIRE_BACKSTOP", "30s") // far away: Finalize must be what closes
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "100ms")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// The child is mid-utterance when the TTL lands.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))

	if resultsClosed(t, stream, 700*time.Millisecond) {
		t.Fatal("the socket retired mid-utterance; the TTL must defer to the turn boundary")
	}

	// End Turn: now the retirement may be taken.
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if !resultsClosed(t, stream, 3*time.Second) {
		t.Fatal("the deferred retirement was never taken at the turn boundary")
	}
}

// The deferred retirement must re-check turn state when it fires, not only
// when it was spawned. Nothing cancels the goroutine, so a press landing
// inside the grace window would otherwise be closed on top of — the same
// mid-utterance close the deferral exists to prevent, just narrowed to the
// grace window. The retirement is only postponed: the next Finalize takes it.
func TestGeminiDeferredRetirementYieldsToAFreshPress(t *testing.T) {
	geminiTapServer(t)
	t.Setenv("GEMINI_STT_SESSION_TTL", "150ms")
	t.Setenv("GEMINI_STT_RETIRE_BACKSTOP", "30s") // far away: not what closes here
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "100ms")   // retire grace is 200ms

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	reset := stream.(interface{ ResetBuffer() }).ResetBuffer

	// Turn 1 is open when the TTL lands, so retirement defers to its Finalize.
	reset()
	_ = stream.SendAudio([]byte("PCMPCM"))
	time.Sleep(400 * time.Millisecond)

	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize (turn 1): %v", err)
	}
	// The child presses again immediately — inside the retirement grace.
	reset()
	_ = stream.SendAudio([]byte("PCMPCM"))

	if resultsClosed(t, stream, time.Second) {
		t.Fatal("the deferred retirement closed on top of a fresh press; that is the mid-utterance close it exists to prevent")
	}

	// Turn 2 ends: now the postponed retirement may be taken.
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize (turn 2): %v", err)
	}
	if !resultsClosed(t, stream, 3*time.Second) {
		t.Fatal("the postponed retirement was never taken at the next turn boundary")
	}
}

// Finalize spawns announceIfEmpty and the retirement together, and
// announceIfEmpty returns early on <-s.closed. Given the same duration they
// race, and a Close that wins answers an empty tap with silence. The
// retirement grace must be strictly longer so the announcement always wins.
func TestGeminiRetireGraceOutlastsEmptyGrace(t *testing.T) {
	for _, empty := range []string{"", "100ms", "3s", "10s"} {
		t.Setenv("GEMINI_STT_EMPTY_GRACE", empty)
		if got, want := geminiRetireGrace(), geminiEmptyGrace(); got <= want {
			t.Fatalf("GEMINI_STT_EMPTY_GRACE=%q: retire grace %v <= empty grace %v; the retirement would race the announcement", empty, got, want)
		}
		if geminiRetireGrace() >= geminiRetireBackstop() {
			t.Fatalf("GEMINI_STT_EMPTY_GRACE=%q: retire grace %v is not inside the backstop %v", empty, geminiRetireGrace(), geminiRetireBackstop())
		}
	}
}

// The behavioural half of the same finding: an empty tap on the very turn that
// takes the retirement must still get "I didn't hear you".
//
// Asserting only that the handler fires is not enough to catch the bug — with
// both goroutines waking on the same deadline the announcement usually wins on
// timer-creation order, so the race passes most runs. The load-bearing
// assertion is the timing one: the socket must still be open comfortably past
// the empty grace, which is false by construction when the retirement is
// spawned with that same grace.
func TestGeminiRetirementTurnStillAnnouncesEmptyTap(t *testing.T) {
	geminiTapServer(t) // answers nothing: every tap here is an empty one
	t.Setenv("GEMINI_STT_SESSION_TTL", "150ms")
	t.Setenv("GEMINI_STT_RETIRE_BACKSTOP", "30s")
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "300ms")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	fired := make(chan struct{}, 1)
	stream.(interface{ SetEmptyResultHandler(func()) }).SetEmptyResultHandler(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})

	// Open a window, let the TTL land on it, then end the turn: this Finalize
	// spawns the announcement and the retirement together.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))
	time.Sleep(400 * time.Millisecond)
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// The announcement is due at +300ms and the retirement at +600ms. At +450ms
	// the socket must still be open: a retirement sharing the empty grace would
	// already have closed it, and announceIfEmpty returns early on <-s.closed.
	if resultsClosed(t, stream, 450*time.Millisecond) {
		t.Fatal("the retiring socket closed within the empty grace; the announcement is racing the close")
	}

	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("the retiring socket closed before the empty-tap announcement; the child got silence")
	}
}

// A window that never closes — a tap the firmware never released — must not
// let the session run into the server's own 10-minute cap.
func TestGeminiRetirementBackstopClosesOpenWindow(t *testing.T) {
	geminiTapServer(t)
	t.Setenv("GEMINI_STT_SESSION_TTL", "150ms")
	t.Setenv("GEMINI_STT_RETIRE_BACKSTOP", "250ms")
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// Opened and never finalized.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	_ = stream.SendAudio([]byte("PCMPCM"))

	if !resultsClosed(t, stream, 3*time.Second) {
		t.Fatal("a window left open forever never hit the retirement backstop")
	}
}

// Findings 4 and 5: the activityEnd write and the flags recording it must move
// together. Committing activityEnded before attempting the write left a failed
// write — the dying-socket case — with the turn permanently marked ended, so a
// retried Finalize returned nil without retrying and the server's window stayed
// open forever.
//
// Built by hand rather than through OpenStream: this needs to substitute the
// socket underneath the adapter, which is only safe with no readLoop running
// on it.
func TestGeminiFinalizeRetriableAfterWriteFailure(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	var sequence []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			mu.Lock()
			switch {
			case strings.Contains(string(raw), `"activityStart"`):
				sequence = append(sequence, "start")
			case strings.Contains(string(raw), `"activityEnd"`):
				sequence = append(sequence, "end")
			}
			mu.Unlock()
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	live, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer live.Close()
	broken, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial throwaway socket: %v", err)
	}
	_ = broken.Close() // every write on this one fails

	adapter := &geminiStreamAdapter{
		conn:       live,
		resultChan: make(chan TranscriptEvent, 4),
		closed:     make(chan struct{}),
	}

	adapter.ResetBuffer()

	// The dying socket: the rotation case the old code turned into a permanent
	// "this turn already ended".
	adapter.conn = broken
	if err := adapter.Finalize(); err == nil {
		t.Fatal("Finalize on a dead socket returned nil; the write failure was swallowed")
	}
	adapter.turnMu.Lock()
	ended, open := adapter.activityEnded, adapter.activityOpen
	adapter.turnMu.Unlock()
	if ended {
		t.Fatal("a failed activityEnd write still marked the turn ended; the retry can never reach the wire")
	}
	if !open {
		t.Fatal("a failed activityEnd write closed the window locally; the server's window would stay open forever")
	}

	adapter.conn = live
	if err := adapter.Finalize(); err != nil {
		t.Fatalf("retried Finalize: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	got := append([]string(nil), sequence...)
	mu.Unlock()
	want := []string{"start", "end"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("wire sequence = %v, want %v — the retried activityEnd never landed", got, want)
	}
}

// The LiveKit track delivers audio for the whole session, but only a turn's
// own speech should ever reach the service: anything outside an activity
// window is discarded server-side under manual activity detection, and
// uploading it billed the session's wall-clock instead of the child's speech.
func TestGeminiSendAudioOnlyInsideActivityWindow(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	audioFrames := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if strings.Contains(string(raw), `"audio"`) {
				mu.Lock()
				audioFrames++
				mu.Unlock()
			}
		}
	}))
	defer server.Close()

	t.Setenv("GEMINI_STT_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("GEMINI_STT_EMPTY_GRACE", "10s")

	stream, err := NewGeminiProvider("k", "").OpenStream(context.Background(), StreamOptions{
		SampleRate: 16000, Channels: 1, Language: "hi-IN",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// Before any press: the track is live but no turn is open.
	for i := 0; i < 5; i++ {
		if err := stream.SendAudio([]byte("PCMPCM")); err != nil {
			t.Fatalf("SendAudio before press: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	before := audioFrames
	mu.Unlock()
	if before != 0 {
		t.Fatalf("server saw %d audio frames before any press, want 0", before)
	}

	// Inside a turn: the child's speech must get through.
	stream.(interface{ ResetBuffer() }).ResetBuffer()
	for i := 0; i < 3; i++ {
		if err := stream.SendAudio([]byte("PCMPCM")); err != nil {
			t.Fatalf("SendAudio during turn: %v", err)
		}
	}
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	during := audioFrames
	mu.Unlock()
	if during != 3 {
		t.Fatalf("server saw %d audio frames during the turn, want 3", during)
	}

	// After the turn ends: back to dropping.
	for i := 0; i < 5; i++ {
		if err := stream.SendAudio([]byte("PCMPCM")); err != nil {
			t.Fatalf("SendAudio after turn: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	after := audioFrames
	mu.Unlock()
	if after != 3 {
		t.Fatalf("server saw %d audio frames total after the turn closed, want 3", after)
	}
}

func TestGeminiAudioSeconds(t *testing.T) {
	cases := map[int64]float64{
		0:      0,
		-1:     0,
		640:    0.02,   // one 20ms frame
		32000:  1,      // one second of 16kHz mono s16le
		470400: 14.7,   // the 14.7s device clip
		960000: 30,
	}
	for in, want := range cases {
		if got := geminiAudioSeconds(in); got != want {
			t.Errorf("geminiAudioSeconds(%d) = %v, want %v", in, got, want)
		}
	}
}
