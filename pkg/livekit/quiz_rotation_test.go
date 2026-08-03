package livekit

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPromptPlaceholders(t *testing.T) {
	day := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)

	// no placeholders -> returned untouched (every non-quiz character)
	plain := "Greet the child warmly and ask what they did today."
	if got := renderPromptPlaceholders(plain, "device-a", day); got != plain {
		t.Fatalf("prompt without placeholders was modified: %q", got)
	}

	// {{TODAY_PLAN}} -> comma separated category order, all categories present
	out := renderPromptPlaceholders("Plan: {{TODAY_PLAN}}.", "device-a", day)
	if strings.Contains(out, "{{TODAY_PLAN}}") {
		t.Fatalf("placeholder not replaced: %q", out)
	}
	for _, c := range quizCategories {
		if !strings.Contains(out, c) {
			t.Fatalf("category %q missing from plan: %q", c, out)
		}
	}

	// {{TODAY_DATE}} -> human readable date
	d := renderPromptPlaceholders("Today is {{TODAY_DATE}}.", "device-a", day)
	if !strings.Contains(d, "2026-08-04") || !strings.Contains(d, "Tuesday") {
		t.Fatalf("date placeholder wrong: %q", d)
	}
}

func TestQuizPlanRotates(t *testing.T) {
	seed := "device-a"
	base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

	// same device + same day -> deterministic
	if a, b := quizPlanForDay(seed, base), quizPlanForDay(seed, base.Add(6*time.Hour)); a[0] != b[0] {
		t.Fatalf("plan not stable within a day: %q vs %q", a[0], b[0])
	}

	// consecutive days -> different opening category
	if a, b := quizPlanForDay(seed, base), quizPlanForDay(seed, base.AddDate(0, 0, 1)); a[0] == b[0] {
		t.Fatalf("opening category repeated on consecutive days: %q", a[0])
	}

	// a full cycle covers every category as the opener (no dead categories)
	seen := map[string]bool{}
	for i := 0; i < len(quizCategories); i++ {
		seen[quizPlanForDay(seed, base.AddDate(0, 0, i))[0]] = true
	}
	if len(seen) != len(quizCategories) {
		t.Fatalf("cycle covered %d of %d categories as opener", len(seen), len(quizCategories))
	}

	// two devices on the same day should not be locked to the same order
	differs := false
	for _, other := range []string{"device-b", "device-c", "device-d"} {
		if quizPlanForDay(seed, base)[0] != quizPlanForDay(other, base)[0] {
			differs = true
			break
		}
	}
	if !differs {
		t.Fatal("all devices got the same opening category; seed is not per-device")
	}

	// plan always contains every category exactly once
	plan := quizPlanForDay(seed, base)
	if len(plan) != len(quizCategories) {
		t.Fatalf("plan length %d, want %d", len(plan), len(quizCategories))
	}
	uniq := map[string]bool{}
	for _, c := range plan {
		if uniq[c] {
			t.Fatalf("duplicate category in plan: %q", c)
		}
		uniq[c] = true
	}
}
