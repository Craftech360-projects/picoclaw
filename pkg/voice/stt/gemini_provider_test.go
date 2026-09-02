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
func TestGeminiResetBufferClearsCancel(t *testing.T) {
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
