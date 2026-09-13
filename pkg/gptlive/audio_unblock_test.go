package gptlive

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// TestReaderNeverBlocksOnUndrainedAudioConsumer is the direct regression test
// for the GPT-Live audio jitter root cause: handleEvent's EventOutputAudioDelta
// case used to send straight into the buffered s.audio channel, so once a slow
// (or, as here, entirely absent) consumer let that buffer fill, the read
// goroutine stopped reading the socket at all — the actual throttle a
// standalone probe against OpenAI proved was ours, not OpenAI's.
//
// The fake server floods far more audio deltas than audioQueue's cap (a few
// seconds of PCM) or s.audio's own 1024-capacity buffer could ever hold, all
// before this test ever calls Audio(), then sends one further, distinguishable
// event on the session.usage.updated channel. Events() is a completely
// separate path from Audio() (its own queue and feeder — see eventQueue), so
// this event can only be observed if the read goroutine kept consuming the
// socket all the way through the flood instead of stalling on the undrained
// audio.
func TestReaderNeverBlocksOnUndrainedAudioConsumer(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] != EventSessionStart {
			return
		}
		f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "unblock_test"}})
		payload := base64.StdEncoding.EncodeToString(make([]byte, 800)) // 25ms at 16kHz mono PCM16
		// audioQueue's cap at 16kHz is 3s * 16000 * 2 = 96000 bytes, i.e. 120
		// of these chunks; 2000 is far beyond both that and s.audio's buffer.
		for i := 0; i < 2000; i++ {
			f.send(c, map[string]any{"type": EventOutputAudioDelta, "delta": payload})
		}
		f.send(c, map[string]any{"type": EventSessionUsageUpdated, "usage": map[string]any{"seconds": 42}})
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.cancel()

	// Deliberately never read s.Audio(): that omission is the whole point.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if u, ok := ev.(VoiceUsage); ok && u.Seconds == 42 {
				return // the reader made it past the entire flood without ever blocking
			}
		case <-timeout:
			t.Fatal("the distinguishing event after the audio flood was never delivered; " +
				"the read goroutine appears stalled behind the undrained audio channel")
		}
	}
}

// TestAudioOverflowDropsOldestAndLogsCount covers the memory-honesty and
// observability half of the fix at the Session level (TestHandoffQueue*
// exercises handoffQueue directly): once audioQueue is over its cap, with
// nothing draining Audio(), the WarnCF overflow log must actually fire and
// report how much was dropped.
//
// audioQueue itself only starts accumulating a backlog once its feeder is
// blocked trying to hand a chunk to s.audio, which only happens once s.audio's
// own 1024-item buffer is completely full — so this needs enough chunks to
// fill that buffer FIRST (1024 of them) before any further chunk can ever
// start piling up in, and then overflow, audioQueue's own much smaller
// byte cap.
func TestAudioOverflowDropsOldestAndLogsCount(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "overflow.log")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	t.Cleanup(logger.DisableFileLogging)

	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] != EventSessionStart {
			return
		}
		f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "overflow_test"}})
		payload := base64.StdEncoding.EncodeToString(make([]byte, 3200)) // 100ms at 16kHz mono PCM16
		for i := 0; i < 2000; i++ {                                      // fill s.audio's 1024-buffer, then overflow audioQueue's 30-chunk (3s) cap
			f.send(c, map[string]any{"type": EventOutputAudioDelta, "delta": payload})
		}
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.cancel()

	deadline := time.Now().Add(3 * time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		raw, _ = os.ReadFile(logPath)
		if strings.Contains(string(raw), "dropping oldest buffered audio") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(raw), "dropping oldest buffered audio") {
		t.Fatalf("no audio-overflow drop warning found in log:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"dropped_chunks"`) || !strings.Contains(string(raw), `"dropped_bytes"`) {
		t.Errorf("drop warning missing dropped-count fields: %s", raw)
	}
}

// TestTeardownWithFullAudioQueueDoesNotHangOrPanic guards the shutdown half of
// the fix: audioQueue can be completely full (its cap is only a few seconds of
// audio, trivially exceeded with nobody draining Audio()) at the exact moment
// the session is asked to close. The feeder goroutine must notice
// audioFeederStop and return immediately rather than trying to flush a large
// backlog into a channel nobody reads — and closeChannelsWhenIdle's eventual
// close(s.audio)/close(s.events) must never race a feeder still trying to
// send, which would panic.
func TestTeardownWithFullAudioQueueDoesNotHangOrPanic(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "teardown_test"}})
			payload := base64.StdEncoding.EncodeToString(make([]byte, 3200))
			// Enough to fill s.audio's 1024-item buffer and then overflow
			// audioQueue's own 30-chunk (3s) cap, so the queue is genuinely
			// full — not just "some audio was sent" — at teardown.
			for i := 0; i < 2000; i++ {
				f.send(c, map[string]any{"type": EventOutputAudioDelta, "delta": payload})
			}
		case EventSessionClose:
			f.send(c, map[string]any{"type": EventSessionClosed, "reason": "close_requested"})
		}
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	// Give the flood time to land and overflow audioQueue's cap; Audio() is
	// deliberately never drained.
	time.Sleep(150 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Close did not return in time with a full, undrained audio queue")
	}

	// Draining to completion, rather than crashing this whole test binary with
	// a send-on-closed-channel panic from a feeder goroutine still trying to
	// flush a backlog when the channels closed, is the actual assertion here.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range s.Audio() {
		}
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("s.Audio() never closed after Close returned")
	}
}
