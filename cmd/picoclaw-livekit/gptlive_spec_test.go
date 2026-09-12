package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/livekit"
)

func TestGPTLiveMemoTypeFollowsTheCharacter(t *testing.T) {
	cases := map[string]string{"Quizzy": "daily_quiz", "bujho": "daily_riddle", "Ginti": "daily_math", "Cheeko": ""}
	for in, want := range cases {
		if got := gptLiveMemoTypeFor(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestGPTLiveMetadataDefaults(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Cheeko","gptlive":{"voice":"vesper","accent":"indian"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if bs.Metadata.GPTLive == nil || bs.Metadata.GPTLive.Voice != "vesper" || bs.Metadata.GPTLive.Accent != "indian" {
		t.Fatalf("gptlive block not parsed: %+v", bs.Metadata.GPTLive)
	}
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{APIKey: "sk", Metadata: bs.Metadata, Workspace: t.TempDir(), CharacterName: "Cheeko"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Voice != "vesper" || spec.SampleRate != 16000 || spec.BackendModel == "" || spec.Quiz != nil {
		t.Errorf("spec defaults wrong: %+v", spec)
	}
	if _, err := buildGPTLiveSpec(gptLiveSpecInput{Metadata: bs.Metadata, Workspace: t.TempDir()}); err == nil {
		t.Error("missing OPENAI_API_KEY must be an error")
	}
}

// TestGPTLiveMetadataAbsentBlockIsNotAnError covers the "gptlive dispatch
// metadata is untrusted; a missing block is normal" requirement: a session
// with no "gptlive" key at all (every non-gptlive-pipeline dispatch, and any
// gptlive dispatch that simply omits per-session overrides) must still parse
// cleanly and get every safe default, not an error.
func TestGPTLiveMetadataAbsentBlockIsNotAnError(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Quizzy"}`)
	if err != nil {
		t.Fatal(err)
	}
	if bs.Metadata.GPTLive != nil {
		t.Fatalf("no gptlive key in the payload must leave GPTLive nil, got %+v", bs.Metadata.GPTLive)
	}
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{APIKey: "sk", Metadata: bs.Metadata, Workspace: t.TempDir(), CharacterName: "Quizzy"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Voice != "marin" || spec.SampleRate != 16000 {
		t.Errorf("defaults wrong for an absent gptlive block: %+v", spec)
	}
}

// TestGPTLiveMetadataRejectsUnknownVoiceAndBogusRate covers the trust
// boundary directly: an unrecognised voice (including the explicitly refused
// "aster") and a rate that is neither 16000 nor 24000 must fall back to safe
// defaults instead of reaching gptlive.Dial unchanged.
func TestGPTLiveMetadataRejectsUnknownVoiceAndBogusRate(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Cheeko","gptlive":{"voice":"aster","rate":48000}}`)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{APIKey: "sk", Metadata: bs.Metadata, Workspace: t.TempDir(), CharacterName: "Cheeko"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Voice == "aster" {
		t.Error("the refused 'aster' voice must never reach the spec")
	}
	if spec.Voice != "marin" {
		t.Errorf("unrecognised voice should fall back to the default, got %v", spec.Voice)
	}
	if spec.SampleRate != 16000 {
		t.Errorf("an unsupported rate should fall back to 16000, got %d", spec.SampleRate)
	}
}

// TestGPTLiveMetadataAccepts24000Rate is the mirror of the bogus-rate case:
// the one other value gptlive.Session actually supports must pass through.
func TestGPTLiveMetadataAccepts24000Rate(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Cheeko","gptlive":{"rate":24000}}`)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{APIKey: "sk", Metadata: bs.Metadata, Workspace: t.TempDir(), CharacterName: "Cheeko"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.SampleRate != 24000 {
		t.Errorf("SampleRate = %d, want 24000", spec.SampleRate)
	}
}

// TestGPTLiveMetadataAssignsQuizTrackerForQuizCharacters checks the other
// half of gptLiveMemoTypeFor's contract: a quiz-bearing character with a
// non-nil QuizBatch must get a live QuizTracker in the spec (Quizzy/Bujho/
// Ginti's scored-quiz path), not just a memo type string.
func TestGPTLiveMetadataAssignsQuizTrackerForQuizCharacters(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Quizzy"}`)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{
		APIKey: "sk", Metadata: bs.Metadata, Workspace: t.TempDir(), CharacterName: "Quizzy",
		QuizBatch: &livekit.QuizBatch{Bank: "quiz", Questions: []livekit.QuizQuestion{{ID: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Quiz == nil {
		t.Error("a quiz character with a non-nil QuizBatch must get a QuizTracker")
	}
}
