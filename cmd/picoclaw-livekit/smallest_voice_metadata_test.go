package main

import "testing"

// The per-character SmallestAI voice must survive the dispatch-metadata parse
// under both the snake_case and camelCase key the gateway may send.
func TestParseRoomMetadataSmallestVoiceID(t *testing.T) {
	for _, key := range []string{"smallest_voice_id", "smallestVoiceId"} {
		bs, err := parseRoomMetadataBootstrap(`{"` + key + `":"emily"}`)
		if err != nil {
			t.Fatalf("%s: parse error: %v", key, err)
		}
		if bs.Metadata.SmallestVoiceID != "emily" {
			t.Fatalf("%s: got %q, want %q", key, bs.Metadata.SmallestVoiceID, "emily")
		}
	}

	bs, err := parseRoomMetadataBootstrap(`{"sarvam_voice_id":"pooja"}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if bs.Metadata.SmallestVoiceID != "" {
		t.Fatalf("absent key should stay empty, got %q", bs.Metadata.SmallestVoiceID)
	}
}
