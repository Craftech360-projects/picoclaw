package gptlive

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestBoundAppendContentPassesShortContentThrough pins the no-op path: content
// already within budget must come back byte-for-byte identical, not merely
// "close enough" — a caller like Greet's short nudge must never be mangled.
func TestBoundAppendContentPassesShortContentThrough(t *testing.T) {
	short := "Greet the child now, following your greeting guidance from the session instructions."
	if got := boundAppendContent("commentary", short); got != short {
		t.Errorf("boundAppendContent altered content within budget: got %q, want %q", got, short)
	}
}

// TestBoundAppendContentTruncatesOversizedContent covers the defensive cap
// itself: the exact production failure this fixes was a 2455-byte greeting
// prompt append being rejected outright by the service's 500-token cap,
// silently leaving the child ungreeted. This builds content well past
// maxAppendChars and checks it comes back within budget, non-empty, and
// strictly shorter than the input — a truncated append that still reaches the
// model beats a rejected one.
func TestBoundAppendContentTruncatesOversizedContent(t *testing.T) {
	sentence := "This is one sentence in a very long greeting prompt that a manager configured. "
	oversized := strings.Repeat(sentence, 40) // far past 1600 characters
	if utf8.RuneCountInString(oversized) <= maxAppendChars {
		t.Fatalf("test fixture too small: %d runes, want > %d", utf8.RuneCountInString(oversized), maxAppendChars)
	}
	got := boundAppendContent("instructions", oversized)
	if n := utf8.RuneCountInString(got); n == 0 || n > maxAppendChars {
		t.Fatalf("truncated content has %d runes, want (0, %d]", n, maxAppendChars)
	}
	if got == oversized {
		t.Fatal("oversized content was passed through unchanged")
	}
}

// TestTruncateAppendContentPrefersSentenceBoundary checks the boundary logic
// specifically: cutting mid-sentence would hand the model a dangling fragment,
// so a sentence end within the budget must win over a raw character cut.
func TestTruncateAppendContentPrefersSentenceBoundary(t *testing.T) {
	content := "Open with a riddle about a king. Then ask their name and favorite color today"
	got := truncateAppendContent(content, 40)
	want := "Open with a riddle about a king."
	if got != want {
		t.Errorf("truncateAppendContent = %q, want %q", got, want)
	}
}

// TestTruncateAppendContentFallsBackToWordBoundary covers content with no
// sentence-ending punctuation inside the budget: the cut must still land on a
// word boundary, never mid-word.
func TestTruncateAppendContentFallsBackToWordBoundary(t *testing.T) {
	content := "supercalifragilistic word boundary fallback check here"
	got := truncateAppendContent(content, 30)
	if strings.HasSuffix(got, " ") || got == "" {
		t.Fatalf("truncateAppendContent = %q, unexpected trailing space or empty result", got)
	}
	if !strings.HasPrefix(content, got) {
		t.Fatalf("truncateAppendContent = %q is not a prefix of the original content", got)
	}
	if idx := strings.IndexByte(content[len(got):], ' '); idx != 0 {
		t.Fatalf("cut did not land on a word boundary: next char after %q is %q", got, string(content[len(got)]))
	}
}

// TestTruncateAppendContentDoesNotSplitMultiByteRunes guards the rune-based
// slicing: Hindi content is a realistic case for GPT-Live's language lock, and
// byte-slicing UTF-8 content at an arbitrary offset can split a multi-byte
// character, producing invalid UTF-8 the service would then reject for an
// unrelated reason.
func TestTruncateAppendContentDoesNotSplitMultiByteRunes(t *testing.T) {
	content := strings.Repeat("नमस्ते बच्चे ", 60) // Hindi greeting, repeated well past budget
	got := truncateAppendContent(content, 50)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateAppendContent produced invalid UTF-8: %q", got)
	}
}

// TestAppendInstructionsSendsTruncatedContentOverTheWire is the end-to-end half:
// it dials a real (fake) server and checks what actually reaches the wire for
// an oversized AppendInstructions call — the same path the quiz Door directives
// use, which accumulate through a session as each one declares itself
// superseding the last (gptLiveDirectiveSupersedes in pkg/livekit) and so can
// grow past the service's cap over a long enough session even if no single
// directive is large on its own.
func TestAppendInstructionsSendsTruncatedContentOverTheWire(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) { acceptStart(f)(c, ev) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Dial(ctx, testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	oversized := strings.Repeat("directive text ", 200)
	s.AppendInstructions(oversized)

	deadline := time.After(2 * time.Second)
	for {
		appends := f.received(EventInstructionsAppend)
		if len(appends) > 0 {
			content, _ := appends[0]["content"].(string)
			if content == "" {
				t.Fatal("session.instructions.append reached the wire with empty content")
			}
			if utf8.RuneCountInString(content) > maxAppendChars {
				t.Fatalf("wire content has %d runes, want <= %d", utf8.RuneCountInString(content), maxAppendChars)
			}
			if content == oversized {
				t.Fatal("oversized content reached the wire unchanged")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("session.instructions.append never reached the fake server")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
