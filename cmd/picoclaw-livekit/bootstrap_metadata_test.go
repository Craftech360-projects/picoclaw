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
		"session_language_name": "English",
		"session_language_code": "en-IN"
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
	if bootstrap.Metadata.SessionLanguageCode != "en-IN" {
		t.Fatalf("SessionLanguageCode = %q, want en-IN", bootstrap.Metadata.SessionLanguageCode)
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

func TestParseRoomMetadataBootstrapAcceptsArrayInterestsAndCamelCaseProfile(t *testing.T) {
	metadata := `{
		"child_profile": {
			"name": "Rahul",
			"age": "6",
			"gender": "male",
			"interests": ["science", "music", "sports"],
			"primaryLanguage": "en",
			"additionalNotes": "Loves story time."
		},
		"long_term_memories": ["likes dinosaurs"]
	}`

	bootstrap, err := parseRoomMetadataBootstrap(metadata)
	if err != nil {
		t.Fatalf("parseRoomMetadataBootstrap returned error: %v", err)
	}
	if bootstrap.Metadata.ChildProfile.Name != "Rahul" {
		t.Fatalf("ChildProfile.Name = %q, want Rahul", bootstrap.Metadata.ChildProfile.Name)
	}
	if bootstrap.Metadata.ChildProfile.Age != 6 {
		t.Fatalf("ChildProfile.Age = %d, want 6", bootstrap.Metadata.ChildProfile.Age)
	}
	if bootstrap.Metadata.ChildProfile.Interests != "science, music, sports" {
		t.Fatalf("ChildProfile.Interests = %q, want joined list", bootstrap.Metadata.ChildProfile.Interests)
	}
	if bootstrap.Metadata.PrimaryLanguage != "en" {
		t.Fatalf("PrimaryLanguage = %q, want en", bootstrap.Metadata.PrimaryLanguage)
	}
	if bootstrap.Metadata.AdditionalNotes != "Loves story time." {
		t.Fatalf("AdditionalNotes = %q, want child profile notes", bootstrap.Metadata.AdditionalNotes)
	}
}

func TestParseRoomMetadataBootstrapAcceptsWrappedMetadataPayload(t *testing.T) {
	metadata := `{
		"metadata": "{\"child_profile\":{\"name\":\"Asha\",\"age\":7},\"session_language_name\":\"Hindi\",\"long_term_memories\":[\"likes planets\"]}"
	}`

	bootstrap, err := parseRoomMetadataBootstrap(metadata)
	if err != nil {
		t.Fatalf("parseRoomMetadataBootstrap returned error: %v", err)
	}
	if bootstrap.Metadata.ChildProfile.Name != "Asha" {
		t.Fatalf("ChildProfile.Name = %q, want Asha", bootstrap.Metadata.ChildProfile.Name)
	}
	if bootstrap.Metadata.PrimaryLanguage != "Hindi" {
		t.Fatalf("PrimaryLanguage = %q, want Hindi", bootstrap.Metadata.PrimaryLanguage)
	}
	if got := len(bootstrap.Metadata.LongTermMemories); got != 1 {
		t.Fatalf("LongTermMemories len = %d, want 1", got)
	}
}

func TestParseRoomMetadataBootstrapParsesSessionLanguageCode(t *testing.T) {
	metadata := `{"session_language_name":"Kannada","session_language_code":"kn-IN"}`
	bootstrap, err := parseRoomMetadataBootstrap(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Metadata.SessionLanguageCode != "kn-IN" {
		t.Fatalf("SessionLanguageCode = %q, want kn-IN", bootstrap.Metadata.SessionLanguageCode)
	}
}

func TestParseRoomMetadataBootstrapLanguagePrecedence(t *testing.T) {
	metadata := `{"session_language_name":"Hindi","session_language_code":"hi-IN","primary_language":"en"}`
	bootstrap, err := parseRoomMetadataBootstrap(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Metadata.SessionLanguageCode != "hi-IN" {
		t.Fatalf("SessionLanguageCode = %q, want hi-IN", bootstrap.Metadata.SessionLanguageCode)
	}
	if bootstrap.Metadata.PrimaryLanguage != "en" {
		t.Fatalf("PrimaryLanguage = %q, want en", bootstrap.Metadata.PrimaryLanguage)
	}
}
