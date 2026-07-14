package edge_tts

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestSecMSGECTokenDeterministic(t *testing.T) {
	// Fixed instant -> fixed token (rounded down to 5-minute boundary).
	at := time.Date(2026, 7, 14, 12, 1, 0, 0, time.UTC) // inside the 12:00–12:05 window
	got := secMSGECToken(at)
	if len(got) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(got))
	}
	if got != secMSGECToken(at) {
		t.Fatal("token not deterministic for the same instant")
	}
	// Any time within the same 5-minute window yields the same token.
	within := at.Add(90 * time.Second) // 12:02:30, same window
	if secMSGECToken(within) != got {
		t.Fatal("token changed within the same 5-minute window")
	}
}

func makeFrame(header string, audio []byte) []byte {
	frame := make([]byte, 2+len(header)+len(audio))
	binary.BigEndian.PutUint16(frame[0:2], uint16(len(header)))
	copy(frame[2:], header)
	copy(frame[2+len(header):], audio)
	return frame
}

func TestExtractAudioPayload(t *testing.T) {
	audio := []byte{10, 20, 30}
	frame := makeFrame("X-RequestId:abc\r\nPath:audio\r\n", audio)
	if got := extractAudioPayload(frame); string(got) != string(audio) {
		t.Fatalf("audio = %v, want %v", got, audio)
	}

	// Non-audio frame (e.g. metadata) yields no payload.
	meta := makeFrame("Path:audio.metadata\r\n", []byte("{}"))
	if got := extractAudioPayload(meta); got != nil {
		t.Fatalf("metadata frame returned payload %v, want nil", got)
	}

	// Truncated frame is safe.
	if got := extractAudioPayload([]byte{0x00}); got != nil {
		t.Fatalf("truncated frame returned %v, want nil", got)
	}
}

func TestSSMLLangFromVoice(t *testing.T) {
	cases := map[string]string{
		"en-US-AnaNeural": "en-US",
		"hi-IN-SwaraNeural": "hi-IN",
		"weird":             "en-US",
	}
	for in, want := range cases {
		if got := ssmlLangFromVoice(in); got != want {
			t.Errorf("ssmlLangFromVoice(%q) = %q, want %q", in, got, want)
		}
	}
}
