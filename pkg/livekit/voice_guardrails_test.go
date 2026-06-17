package livekit

import "testing"

func TestVoiceSafetyOverride(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantMatch bool
		wantResp  string
	}{
		{"self-harm direct", "I feel really sad and I want to hurt myself", true, selfHarmSafetyResponse},
		{"self-harm kill", "sometimes i want to kill myself", true, selfHarmSafetyResponse},
		{"model probe chatgpt", "Are you ChatGPT or GPT-4?", true, creatorIdentityResponse},
		{"model probe gemini", "are you gemini from google?", true, creatorIdentityResponse},
		{"normal chat", "tell me a story about a tiger", false, ""},
		{"google verb not probe", "can you google the weather?", false, ""},
		{"empty", "   ", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, ok := voiceSafetyOverride(c.text)
			if ok != c.wantMatch {
				t.Fatalf("match=%v want %v (text=%q)", ok, c.wantMatch, c.text)
			}
			if resp != c.wantResp {
				t.Fatalf("resp=%q want %q", resp, c.wantResp)
			}
		})
	}
}

func TestSanitizeVoiceTextStripsEmojiAndMarkdown(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Yay! 🎉 You did it! 😊", "Yay! You did it!"},
		{"Here are facts:\n1. **Owls** are cool\n2. Penguins waddle", "Here are facts: Owls are cool Penguins waddle"},
		{"Check [Childline](https://x.org) okay", "Check Childline okay"},
		{"Visit https://thetrevorproject.org now", "Visit now"},
		{"Plain sentence with no junk.", "Plain sentence with no junk."},
		{"He smiled. ( *Dramatic pause* ) Then he ran!", "He smiled. Then he ran!"},
		{"Once upon a time (laughs) there was a lion.", "Once upon a time there was a lion."},
		{"Here's a joke! *pauses dramatically* Why did the chicken cross?", "Here's a joke! Why did the chicken cross?"},
		{"That was *really* cool!", "That was really cool!"},
	}
	for _, c := range cases {
		got := sanitizeVoiceTextForTTS(c.in)
		if got != c.want {
			t.Fatalf("sanitize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
