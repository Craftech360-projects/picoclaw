package livekit

import (
	"sort"
	"strings"
	"testing"
)

// The failure this guards is silent in both directions: an item wrongly counted
// as delivered is never heard again, and one wrongly missed comes back as a
// repeat. Both look like normal sessions from the outside.

func jokePayload(items ...[2]string) *ContentPayload {
	p := &ContentPayload{Bank: "joke"}
	for _, it := range items {
		p.Items = append(p.Items, map[string]any{"code": it[0], "punchline": it[1]})
	}
	return p
}

func said(turns ...string) []PersistedChatMessage {
	out := make([]PersistedChatMessage, 0, len(turns))
	for _, t := range turns {
		out = append(out, PersistedChatMessage{ChatType: 2, Content: t})
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestDeliveredCodesCountsOnlyWhatWasSpoken(t *testing.T) {
	payload := jokePayload(
		[2]string{"MJ-1", "Because it was not PEELING well!"},
		[2]string{"MJ-2", "Because the ocean waved back!"},
		[2]string{"MJ-3", "He was stuffed with laddoos!"},
	)
	// Two told, one served but never reached — the real 2026-08-20 shape.
	transcript := said(
		"[silly] Why did the banana go to the doctor? Because it was not PEELING well!",
		"[laughing] Why did the sea say hello? Because the ocean waved back!",
	)

	got := DeliveredCodes(payload, transcript)
	if want := []string{"MJ-1", "MJ-2"}; strings.Join(sorted(got), ",") != strings.Join(want, ",") {
		t.Errorf("delivered = %v, want %v (MJ-3 was served but never told)", got, want)
	}
}

func TestDeliveredCodesIgnoresTheChildsOwnWords(t *testing.T) {
	payload := jokePayload([2]string{"MJ-9", "Because the ocean waved back!"})
	// The CHILD says the punchline; Masti never did. Counting the user side
	// would burn a joke she still has to tell.
	transcript := []PersistedChatMessage{
		{ChatType: chatTypeUser, Content: "I know one! Because the ocean waved back!"},
		{ChatType: 2, Content: "[happy] Ha! Tell me another one."},
	}

	if got := DeliveredCodes(payload, transcript); len(got) != 0 {
		t.Errorf("delivered = %v, want none — only the assistant's turns count", got)
	}
}

func TestDeliveredCodesSurvivesRetellingWithDifferentWrapping(t *testing.T) {
	payload := jokePayload([2]string{"MJ-4", "Because it was not PEELING well!"})
	// Expression tags, punctuation and casing all differ from the bank text.
	transcript := said("[giggling] because it was NOT peeling well, hehe")

	if got := DeliveredCodes(payload, transcript); len(got) != 1 {
		t.Errorf("delivered = %v, want MJ-4 — a real telling must not be missed", got)
	}
}

func TestDeliveredCodesEmptyTranscriptBurnsNothing(t *testing.T) {
	payload := jokePayload([2]string{"MJ-1", "Because it was not PEELING well!"})

	// A session that recorded no assistant turns delivered nothing. Returning
	// the served list here would burn the payload on the way out, which is the
	// whole defect.
	if got := DeliveredCodes(payload, nil); len(got) != 0 {
		t.Errorf("delivered = %v on an empty transcript, want none", got)
	}
}

func TestDeliveredCodesFallsBackWhenBankHasNoProbe(t *testing.T) {
	// Tara paraphrases her kid-answers, so `why` has no stable probe and keeps
	// the marking-served behaviour rather than guessing.
	payload := &ContentPayload{Bank: "why", Items: []map[string]any{
		{"code": "TR-1", "answer_text": "the sky loves the blue bit most"},
		{"code": "TR-2", "answer_text": "sleep is how your body charges"},
	}}

	got := DeliveredCodes(payload, said("[curious] Sunlight is secretly a rainbow!"))
	if want := []string{"TR-1", "TR-2"}; strings.Join(sorted(got), ",") != strings.Join(want, ",") {
		t.Errorf("delivered = %v, want all served %v for a probe-less bank", got, want)
	}
}

func TestDeliveredCodesUnrelatedChatterDeliversNothing(t *testing.T) {
	payload := jokePayload([2]string{"MJ-7", "Because it was not PEELING well!"})
	// Free chat that shares only filler with the punchline must not match, or
	// jokes burn during a session that told none.
	transcript := said("[happy] That is a wonderful number of steps! You are a great counter.")

	if got := DeliveredCodes(payload, transcript); len(got) != 0 {
		t.Errorf("delivered = %v, want none — chatter must not count as a telling", got)
	}
}

func TestDeliveredCodesHandlesNilAndEmptyPayloads(t *testing.T) {
	if got := DeliveredCodes(nil, said("anything")); got != nil {
		t.Errorf("nil payload returned %v", got)
	}
	if got := DeliveredCodes(&ContentPayload{Bank: "joke"}, said("anything")); got != nil {
		t.Errorf("empty payload returned %v", got)
	}
}

func TestDeliveredCodesSpellBankMatchesTheWordItself(t *testing.T) {
	payload := &ContentPayload{Bank: "spell", Items: []map[string]any{
		{"code": "SB-1", "word": "chair"},
		{"code": "SB-2", "word": "friend"},
	}}
	transcript := said("[excited] Your word is CHAIR. Spell chair!")

	got := DeliveredCodes(payload, transcript)
	if len(got) != 1 || got[0] != "SB-1" {
		t.Errorf("delivered = %v, want only SB-1", got)
	}
}

// The actual 2026-08-20 session, replayed against the real bank text.
//
// Masti was served six jokes and told two before the child hung up. Every one
// of the six was marked heard, so four jokes the child never encountered were
// spent. At that rate a sixty-joke bank is exhausted in ten sittings having
// delivered twenty jokes.
func TestDeliveredCodesReplaysTheBurnIncident(t *testing.T) {
	served := jokePayload(
		[2]string{"MJ-02-10", "You are my biggest FAN!"},
		[2]string{"MJ-03-01", "Because seven ATE nine!"},
		[2]string{"MJ-03-02", "An idli-GO-round... okay okay - a WANDER-idli, hihihi!"},
		[2]string{"MJ-03-03", "Because the teacher said it was a piece of CAKE!"},
		[2]string{"MJ-03-04", "Put a little BOOGIE in it!"},
		[2]string{"MJ-03-05", "Because it had a lot of TRUNK space!"},
	)
	// What the session's parent_summary records: the seven-ate-nine joke and
	// the wander-idli one, then the child left.
	transcript := said(
		"[excited] Why was six afraid of seven? [laughing] Because seven ATE nine!",
		"[silly] What do you call an idli on a merry-go-round? An idli-GO-round... okay okay - a WANDER-idli, hihihi!",
	)

	got := sorted(DeliveredCodes(served, transcript))
	want := []string{"MJ-03-01", "MJ-03-02"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivered = %v, want %v", got, want)
	}
	// The four unheard jokes must remain available.
	if len(served.Items)-len(got) != 4 {
		t.Errorf("burned %d jokes the child never heard, want 0", len(served.Items)-len(got)-4)
	}
}

// The false positive that word-set overlap produced, pinned so a future
// "simplify the matcher" cannot reintroduce it.
//
// Two punchlines share only ordinary words with what was actually said. Under
// overlap-counting they matched and were burned unheard; only adjacency
// separates a quotation from a coincidence.
func TestDeliveredCodesRejectsIncidentalWordSharing(t *testing.T) {
	payload := jokePayload(
		[2]string{"TOLD", "Because seven ATE nine!"},
		[2]string{"SHARES-BECAUSE-AND-WAS", "Because the teacher said it was a piece of CAKE!"},
		[2]string{"SHARES-BECAUSE", "Because it had a lot of TRUNK space!"},
	)
	transcript := said("[excited] Why was six afraid of seven? [laughing] Because seven ATE nine!")

	got := DeliveredCodes(payload, transcript)
	if len(got) != 1 || got[0] != "TOLD" {
		t.Errorf("delivered = %v, want only TOLD — the others were never spoken", got)
	}
}

// Adjacency must not be satisfied by words that are merely present. A run has
// to be contiguous in BOTH texts.
func TestDeliveredCodesRequiresContiguity(t *testing.T) {
	payload := jokePayload([2]string{"MJ-X", "Because seven ATE nine!"})
	// Every probe word appears, none of them adjacent in the same order.
	transcript := said("[happy] nine friends were here, and because of that, seven of them ate later")

	if got := DeliveredCodes(payload, transcript); len(got) != 0 {
		t.Errorf("delivered = %v, want none — scattered words are not a telling", got)
	}
}
