package stt

import "testing"

// Every branch that returns ok=false is a reply the pipeline never sees. The one
// that matters is an empty final transcript: Sarvam has answered, RunInbound is
// still waiting, and the turn hangs until the device gives up. These assert the
// classification so a future edit cannot quietly reintroduce a silent drop.
func TestParseMessageDropsAreClassified(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"unparseable json", `not json at all`},
		{"empty partial", `{"event":"transcript.partial","text":"  "}`},
		{"empty final", `{"event":"transcript.final","text":"   "}`},
		{"unknown event", `{"event":"something.new"}`},
		// The old schema. These used to be the only shapes the parser understood,
		// which is why every real reply was discarded.
		{"legacy type field", `{"type":"data","data":{"transcript":"blue"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &sarvamStreamAdapter{language: "en-IN"}
			evt, ok := s.parseMessage([]byte(tt.raw))
			if ok {
				t.Fatalf("parseMessage(%q) ok = true, want false (got %+v)", tt.raw, evt)
			}
		})
	}
}

// The parse path that must still work, so the logging additions did not break
// recognition of a real transcript.
func TestParseMessageStillReadsATranscript(t *testing.T) {
	s := &sarvamStreamAdapter{language: "en-IN"}
	evt, ok := s.parseMessage([]byte(`{"event":"transcript.final","text":"blue","language":"en-IN","start_s":"0.5","end_s":"2.0"}`))
	if !ok {
		t.Fatal("parseMessage() ok = false, want true")
	}
	if evt.Text != "blue" {
		t.Fatalf("Text = %q, want %q", evt.Text, "blue")
	}
	if !evt.IsFinal {
		t.Fatal("IsFinal = false, want true")
	}
	if evt.Duration != 1.5 {
		t.Fatalf("Duration = %v, want 1.5 (end_s - start_s)", evt.Duration)
	}
}

// Interim results are what let barge-in happen mid-utterance; a partial must not
// be reported as final or the turn ends on the first word.
func TestParseMessageReadsAPartial(t *testing.T) {
	s := &sarvamStreamAdapter{language: "en-IN"}
	evt, ok := s.parseMessage([]byte(`{"event":"transcript.partial","text":"blu","language":"en-IN"}`))
	if !ok {
		t.Fatal("parseMessage() ok = false, want true")
	}
	if evt.Text != "blu" {
		t.Fatalf("Text = %q, want %q", evt.Text, "blu")
	}
	if evt.IsFinal {
		t.Fatal("IsFinal = true on a partial; the turn would end early")
	}
}

// vad.speech_end carries no text, so it must not claim finality.
func TestParseMessageVadSpeechEndIsNotFinal(t *testing.T) {
	s := &sarvamStreamAdapter{language: "en-IN", speaking: true}
	evt, ok := s.parseMessage([]byte(`{"event":"vad.speech_end","utterance_idx":0,"confidence":"0.92"}`))
	if !ok {
		t.Fatal("parseMessage() ok = false, want true")
	}
	if !evt.SpeechEnd {
		t.Fatal("SpeechEnd = false, want true")
	}
	if evt.IsFinal || evt.Text != "" {
		t.Fatalf("event = %+v, want SpeechEnd only with no text and no finality", evt)
	}
}

// The first version of the close diagnostic reported sync/once.go because it used
// a fixed skip depth. Whatever it returns, it must not be inside the stdlib sync
// package or this file.
func TestCloseCallerSkipsSyncAndAdapter(t *testing.T) {
	got := closeCallerOutsideAdapter()
	if got == "unknown" {
		t.Fatal("closeCallerOutsideAdapter() = unknown; attribution is broken")
	}
	for _, bad := range []string{"/sync/", "sarvam_provider.go"} {
		if contains(got, bad) {
			t.Fatalf("closeCallerOutsideAdapter() = %q, must not point inside %q", got, bad)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
