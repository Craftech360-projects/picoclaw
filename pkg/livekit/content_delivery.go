package livekit

import (
	"strings"
)

// Which of the served content items the character actually DELIVERED.
//
// The no-repeat ledger used to record everything served, because the MEMO's
// id-carrying fields are not uniform across characters — Masti lists joke
// codes, Nani one story key, the spelling banks plain words, and Tara names no
// items at all. Marking-served covered every bank, at a price: a session that
// serves six jokes and tells two burns four the child never heard. Measured
// 2026-08-20 on two real sessions — 12 served, 4 told, a 67% loss rate that
// exhausts a sixty-joke bank in ten sittings.
//
// So do not ask the model what it said; check what it SAID. The served text is
// known exactly and the transcript holds the spoken turns, which makes this a
// string comparison rather than a question for an LLM. It is the same stance
// questionTextMatchesBank takes for the quiz — self-reporting was already found
// untrustworthy there, when four false "cleared" rows reached the database on
// 2026-08-04.
//
// Where a bank has no line distinctive enough to match on, it falls back to
// marking-served: today's behaviour is a known quantity, and inventing a
// half-confident match would trade a visible loss for a silent one.

// deliveryProbe is the field of a bank's item that a character necessarily
// speaks nearly verbatim when it uses the item, and the number of its
// distinctive words that must appear in the transcript to count as delivered.
//
// The probe is the LEAST paraphrasable part of each item. A joke's punchline
// survives retelling because the wording is the joke; a wonder's kid-answer
// does not, because Tara re-explains it in her own words every time — which is
// why `why` is absent here rather than matched loosely.
type deliveryProbe struct {
	field string
	// minRun is how many CONSECUTIVE probe words must appear in the transcript.
	//
	// Consecutive, not a word-set overlap: overlap counts scattered incidental
	// words and two of those are easy to find. Replaying the real 2026-08-20
	// payload, "Because the teacher said it was a piece of CAKE!" matched a
	// session that never told it, purely on "because" and "was" appearing
	// elsewhere — a joke silently burned. A run of three is a quotation; three
	// scattered words are a coincidence.
	minRun int
}

var deliveryProbes = map[string]deliveryProbe{
	// The punchline IS the joke; Masti delivers it intact or the joke fails.
	"joke": {field: "punchline", minRun: 3},
	// Announced as a word to spell, then spelled back letter by letter. A
	// one-word probe can only ever run to one, which is why the threshold is
	// clamped to the probe length below.
	"spell": {field: "word", minRun: 3},
	// Mitthu says the word, its meaning and an example sentence.
	"word": {field: "word", minRun: 3},
	// Absent on purpose:
	//   why   - Tara paraphrases the kid-answer freely; no stable probe.
	//   story - already gated on the MEMO's completed=true, which is a better
	//           signal than any text match, and a story spans many turns.
}

// DeliveredCodes returns the codes from `payload` that the assistant turns in
// `transcript` show were actually used. Banks without a probe return every
// served code, preserving the marking-served behaviour.
//
// An empty transcript returns nothing rather than everything: a session that
// recorded no assistant turns delivered nothing, and burning the payload on the
// way out is exactly the loss this exists to stop.
func DeliveredCodes(payload *ContentPayload, transcript []PersistedChatMessage) []string {
	if payload == nil || len(payload.Items) == 0 {
		return nil
	}

	served := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		if code := strings.TrimSpace(str(item, "code")); code != "" {
			served = append(served, code)
		}
	}

	probe, ok := deliveryProbes[payload.Bank]
	if !ok {
		return served // no reliable probe for this bank
	}

	var spoken strings.Builder
	for _, msg := range transcript {
		if msg.ChatType == chatTypeUser {
			continue
		}
		spoken.WriteString(" ")
		spoken.WriteString(msg.Content)
	}
	spokenSeq := normalizedWords(spoken.String())
	if len(spokenSeq) == 0 {
		return nil
	}

	delivered := make([]string, 0, len(served))
	for _, item := range payload.Items {
		code := strings.TrimSpace(str(item, "code"))
		if code == "" {
			continue
		}
		probeSeq := normalizedWords(str(item, probe.field))
		if len(probeSeq) == 0 {
			continue
		}
		// A probe shorter than the threshold must match in full rather than be
		// unmatchable: "chair" is one word and is still a real telling.
		need := probe.minRun
		if len(probeSeq) < need {
			need = len(probeSeq)
		}
		if longestRun(probeSeq, spokenSeq) >= need {
			delivered = append(delivered, code)
		}
	}
	return delivered
}

// normalizedWords lowercases and strips punctuation, keeping ORDER and
// duplicates — contentWords returns a set, which cannot express adjacency.
// Filler words are kept for the same reason: dropping "it" from "a piece of
// cake" would let a run jump a gap that was never spoken.
func normalizedWords(s string) []string {
	fields := strings.Fields(strings.ToLower(s))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if word := strings.Trim(field, ".,!?;:'\"()-"); word != "" {
			out = append(out, word)
		}
	}
	return out
}

// longestRun returns the length of the longest run of `probe` appearing
// consecutively in `spoken`.
func longestRun(probe, spoken []string) int {
	best := 0
	for start := range spoken {
		for offset := range probe {
			run := 0
			for start+run < len(spoken) && offset+run < len(probe) &&
				spoken[start+run] == probe[offset+run] {
				run++
			}
			if run > best {
				best = run
			}
		}
	}
	return best
}
