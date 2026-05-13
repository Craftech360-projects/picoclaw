package livekit

import "testing"

func TestNormalizeSessionLanguagePolicy_NameAndCode(t *testing.T) {
	p := NormalizeSessionLanguagePolicy("Tamil", "ta-IN")
	if p.DisplayName != "Tamil" || p.STTHintCode != "ta" || p.RawCode != "ta-IN" {
		t.Fatalf("unexpected policy: %+v", p)
	}
}

func TestNormalizeSessionLanguagePolicy_FallbackEnglish(t *testing.T) {
	p := NormalizeSessionLanguagePolicy("", "")
	if p.DisplayName != "English" || p.STTHintCode != "en" || p.RawCode != "en" {
		t.Fatalf("unexpected fallback policy: %+v", p)
	}
}

func TestResolveSTTHintWithCapabilities_Exact(t *testing.T) {
	p := SessionLanguagePolicy{DisplayName: "Tamil", RawCode: "ta-IN", STTHintCode: "ta"}
	got := ResolveSTTHintWithCapabilities(p, []string{"auto", "en", "ta"})
	if got != "ta" {
		t.Fatalf("got %q, want ta", got)
	}
}

func TestResolveSTTHintWithCapabilities_BaseToRegion(t *testing.T) {
	p := SessionLanguagePolicy{DisplayName: "Hindi", STTHintCode: "hi"}
	got := ResolveSTTHintWithCapabilities(p, []string{"auto", "hi-IN"})
	if got != "hi-IN" {
		t.Fatalf("got %q, want hi-IN", got)
	}
}

func TestResolveSTTHintWithCapabilities_RegionToBase(t *testing.T) {
	p := SessionLanguagePolicy{DisplayName: "Tamil", RawCode: "ta-IN", STTHintCode: "ta"}
	got := ResolveSTTHintWithCapabilities(p, []string{"auto", "ta"})
	if got != "ta" {
		t.Fatalf("got %q, want ta", got)
	}
}

func TestResolveSTTHintWithCapabilities_FallsBackAuto(t *testing.T) {
	p := SessionLanguagePolicy{DisplayName: "Kannada", STTHintCode: "kn"}
	got := ResolveSTTHintWithCapabilities(p, []string{"auto", "en"})
	if got != "auto" {
		t.Fatalf("got %q, want auto", got)
	}
}
