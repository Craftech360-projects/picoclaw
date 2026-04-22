package main

import "testing"

func TestParseRoomMetadataBootstrapUsesValidRoomMetadata(t *testing.T) {
	metadata := `{
		"device_mac": "aa:bb:cc:dd:ee:ff",
		"device_uuid": "device-1",
		"character": "Cheeko",
		"child_profile": {
			"name": "Asha",
			"age": 7,
			"gender": "female",
			"interests": "space"
		},
		"long_term_memories": ["likes planets"],
		"memory_relations": [{"source":"Asha","relation":"likes","target":"planets"}],
		"memory_entities": [{"name":"Asha","type":"person"}],
		"session_language_name": "English"
	}`

	bootstrap, err := parseRoomMetadataBootstrap(metadata)
	if err != nil {
		t.Fatalf("parseRoomMetadataBootstrap returned error: %v", err)
	}
	if bootstrap.Source != bootstrapSourceRoomMetadata {
		t.Fatalf("Source = %q, want %q", bootstrap.Source, bootstrapSourceRoomMetadata)
	}
	if bootstrap.Metadata.ChildProfile.Name != "Asha" {
		t.Fatalf("ChildProfile.Name = %q, want Asha", bootstrap.Metadata.ChildProfile.Name)
	}
	if got := len(bootstrap.Metadata.LongTermMemories); got != 1 {
		t.Fatalf("LongTermMemories len = %d, want 1", got)
	}
	if bootstrap.Metadata.PrimaryLanguage != "English" {
		t.Fatalf("PrimaryLanguage = %q, want English", bootstrap.Metadata.PrimaryLanguage)
	}
}

func TestParseRoomMetadataBootstrapFallsBackForInvalidMetadata(t *testing.T) {
	bootstrap, err := parseRoomMetadataBootstrap(`{"child_profile":`)
	if err == nil {
		t.Fatal("parseRoomMetadataBootstrap error = nil, want error")
	}
	if bootstrap.Source != bootstrapSourceManagerAPIFallback {
		t.Fatalf("Source = %q, want %q", bootstrap.Source, bootstrapSourceManagerAPIFallback)
	}
}
