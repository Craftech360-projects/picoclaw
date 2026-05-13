package livekit

import "testing"

func TestParseSessionLanguageUpdateMessage(t *testing.T) {
	payload := []byte(`{"type":"session_language_update","session_language_name":"Tamil","session_language_code":"ta-IN","rfid_uid":"ABC"}`)
	update, ok := parseSessionLanguageUpdate(payload)
	if !ok {
		t.Fatal("parseSessionLanguageUpdate() = false, want true")
	}
	if update.Name != "Tamil" || update.Code != "ta-IN" || update.RFIDUID != "ABC" {
		t.Fatalf("unexpected update: %+v", update)
	}
}

func TestParseSessionLanguageUpdateRejectsEmptyLanguage(t *testing.T) {
	payload := []byte(`{"type":"session_language_update","rfid_uid":"ABC"}`)
	if _, ok := parseSessionLanguageUpdate(payload); ok {
		t.Fatal("parseSessionLanguageUpdate() = true, want false")
	}
}
