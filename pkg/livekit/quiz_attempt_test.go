package livekit

import "testing"

// The attempt log is assembled from `awaiting=` staying put across turns, not
// from anything the model is asked to report. These cases are the sequences a
// real Daily Ten produces.
func TestTrackQuizAttempt(t *testing.T) {
	memo := func(awaiting string) string {
		return "type=daily_quiz | date=2026-08-14 | status=in_progress | awaiting=" + awaiting
	}

	t.Run("two tries then correct reports both", func(t *testing.T) {
		ab := &AgentBridge{}

		// Turn 1: question five is asked. Nothing has been tried yet.
		if got := ab.trackQuizAttemptLocked(memo("5"), 0, "", false); got != nil {
			t.Fatalf("asking a question is not an attempt: %+v", got)
		}

		// Turn 2: the child answers, and five is still awaiting — a missed try.
		ab.lastUserText = "appu"
		if got := ab.trackQuizAttemptLocked(memo("5"), 0, "", false); got != nil {
			t.Fatalf("a missed try is held, not reported: %+v", got)
		}

		// Turn 3: the child gets it, and the reply moves on to six.
		ab.lastUserText = "apple"
		got := ab.trackQuizAttemptLocked(memo("6"), 5, "correct", true)
		if len(got) != 2 {
			t.Fatalf("expected 2 attempts, got %d (%+v)", len(got), got)
		}
		if got[0].Verdict != "wrong" || got[0].Transcript != "appu" {
			t.Errorf("first try wrong: %+v", got[0])
		}
		if got[1].Verdict != "correct" || got[1].Transcript != "apple" {
			t.Errorf("second try wrong: %+v", got[1])
		}
		if ab.pendingQuizID != 6 {
			t.Errorf("tracking should move to the question now being asked, got %d", ab.pendingQuizID)
		}
		if ab.pendingQuizAttempts != nil {
			t.Errorf("the next question starts with a clean list: %+v", ab.pendingQuizAttempts)
		}
	})

	t.Run("first-time correct reports one attempt", func(t *testing.T) {
		ab := &AgentBridge{}
		ab.trackQuizAttemptLocked(memo("5"), 0, "", false)
		ab.lastUserText = "apple"
		got := ab.trackQuizAttemptLocked(memo("6"), 5, "correct", true)
		if len(got) != 1 || got[0].Verdict != "correct" {
			t.Fatalf("expected one correct attempt, got %+v", got)
		}
	})

	t.Run("silence is not an attempt", func(t *testing.T) {
		// The prompt already says silence does not count as a try. A row for it
		// would make a quiet child read as a struggling one.
		ab := &AgentBridge{}
		ab.trackQuizAttemptLocked(memo("5"), 0, "", false)
		ab.lastUserText = "   "
		ab.trackQuizAttemptLocked(memo("5"), 0, "", false)
		if len(ab.pendingQuizAttempts) != 0 {
			t.Fatalf("silence logged as an attempt: %+v", ab.pendingQuizAttempts)
		}
	})

	t.Run("a verdict for an untracked question reports only its final try", func(t *testing.T) {
		// parseQuizVerdict corrects a stray id to the only pending question, so a
		// verdict can arrive for a question whose earlier turns were never seen.
		// Attaching another question's tries to it would be worse than losing them.
		ab := &AgentBridge{}
		ab.trackQuizAttemptLocked(memo("5"), 0, "", false)
		ab.lastUserText = "guess"
		ab.trackQuizAttemptLocked(memo("5"), 0, "", false)

		ab.lastUserText = "banana"
		got := ab.trackQuizAttemptLocked(memo("8"), 7, "revealed", true)
		if len(got) != 1 || got[0].Transcript != "banana" {
			t.Fatalf("expected only the final try, got %+v", got)
		}
	})

	t.Run("attempts are capped", func(t *testing.T) {
		ab := &AgentBridge{}
		ab.trackQuizAttemptLocked(memo("5"), 0, "", false)
		for i := 0; i < maxTrackedAttempts*2; i++ {
			ab.lastUserText = "again"
			ab.trackQuizAttemptLocked(memo("5"), 0, "", false)
		}
		if len(ab.pendingQuizAttempts) > maxTrackedAttempts {
			t.Fatalf("cap exceeded: %d", len(ab.pendingQuizAttempts))
		}
	})

	t.Run("no memo awaiting leaves tracking alone", func(t *testing.T) {
		// Cheeko and Nani emit no daily_quiz MEMO at all.
		ab := &AgentBridge{}
		if got := ab.trackQuizAttemptLocked("type=chat", 0, "", false); got != nil {
			t.Fatalf("non-quiz memo produced attempts: %+v", got)
		}
	})
}
