package main

import (
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// gptLiveVoices is the allow-list of voice names buildGPTLiveSpec will pass
// through as-is. "aster" is deliberately absent: this OpenAI account's
// realtime access refuses that voice outright, so accepting it here would
// only trade a clean input-validation fallback for a live dial failure deep
// inside gptlive.Dial. dispatch metadata is untrusted input (an unknown
// voice is normal traffic, not an error), so anything not in this set simply
// falls back to gptlive.DefaultVoice below instead of failing the session.
var gptLiveVoices = map[string]bool{
	"beacon": true,
	"cinder": true,
	"marin":  true,
	"stone":  true,
	"vesper": true,
}

// gptLiveMemoTypeFor maps a character's display name to the MEMO type its
// QuizTracker writes (quiz_state.go's daily_quiz/daily_riddle/daily_math),
// mirroring the cascade's own per-character memo routing (agent_bridge.go's
// maybePersistQuizState). A character with no scored quiz (Cheeko, Nani, the
// content characters, ...) gets "" back, and buildGPTLiveSpec leaves Quiz nil
// for it — matching the cascade, which only tracks quiz state for
// Quizzy/Bujho/Ginti.
func gptLiveMemoTypeFor(character string) string {
	switch strings.ToLower(strings.TrimSpace(character)) {
	case "quizzy":
		return "daily_quiz"
	case "bujho":
		return "daily_riddle"
	case "ginti":
		return "daily_math"
	}
	return ""
}

// gptLiveSpecInput carries everything buildGPTLiveSpec needs to compose one
// session's livekit.GPTLiveSessionSpec: the dispatch metadata (untrusted —
// Voice/Accent/Rate are validated below, never passed through raw), the
// pieces bridgeFactory already resolved for the cascade path (persona
// greeting, language, tools, quiz batch/bank block) and the reporters the
// quiz tracker forwards verdicts to.
type gptLiveSpecInput struct {
	APIKey         string
	Metadata       roomMetadata
	Workspace      string
	CharacterName  string
	GreetingPrompt string
	LanguageName   string
	BaseTools      *tools.ToolRegistry
	QuizBatch      *livekit.QuizBatch        // nil when the persona has no quiz placeholder
	QuizReporters  livekit.QuizTrackerConfig // AnswerReporter, AttemptReporter, WonderReporter only; Batch/Workspace/MemoType are filled in below
	BankBlock      string
}

// buildGPTLiveSpec validates the untrusted gptlive dispatch-metadata block
// and composes the immutable per-session GPT-Live spec. Voice/accent/rate
// fall back to safe defaults for anything unrecognised — an unknown voice, a
// bogus rate, or a missing "gptlive" block entirely are all normal dispatch
// traffic, not errors. Only a missing OPENAI_API_KEY fails the session
// outright: once the gptlive pipeline is selected there is no cascade
// STT/TTS to fall back to.
func buildGPTLiveSpec(in gptLiveSpecInput) (*livekit.GPTLiveSessionSpec, error) {
	if strings.TrimSpace(in.APIKey) == "" {
		return nil, errors.New("OPENAI_API_KEY is required for the gptlive pipeline")
	}
	// Always 24kHz, like the livekit-agents GPT-Live plugin (SAMPLE_RATE = 24000): lkmedia
	// resamples the room's mic in and Opus carries the output, so clients never see this rate.
	voice, accent, rate := gptlive.DefaultVoice, "default", 24000
	if g := in.Metadata.GPTLive; g != nil {
		if v := strings.ToLower(strings.TrimSpace(g.Voice)); gptLiveVoices[v] {
			voice = v
		}
		if strings.EqualFold(strings.TrimSpace(g.Accent), "indian") {
			accent = "indian"
		}
	}
	var quiz *livekit.QuizTracker
	memoType := gptLiveMemoTypeFor(in.CharacterName)
	if in.QuizBatch != nil && memoType != "" {
		cfg := in.QuizReporters
		cfg.Batch, cfg.Workspace, cfg.MemoType = in.QuizBatch, in.Workspace, memoType
		quiz = livekit.NewQuizTracker(cfg)
	}
	persona := livekit.BuildGPTLivePersona(livekit.GPTLivePersonaInput{
		Workspace:      in.Workspace,
		CharacterName:  in.CharacterName,
		GreetingPrompt: in.GreetingPrompt,
		LanguageName:   in.LanguageName,
		Accent:         accent,
		BankBlock:      in.BankBlock,
		HasQuiz:        quiz != nil,
	})
	return &livekit.GPTLiveSessionSpec{
		APIKey:             in.APIKey,
		Voice:              voice,
		SampleRate:         rate,
		BackendModel:       gptlive.DefaultBackendModel,
		Persona:            persona,
		Tools:              livekit.BuildGPTLiveTools(in.BaseTools, in.Workspace, quiz),
		Quiz:               quiz,
		WebSearch:          true,
		MaxSessionDuration: 55 * time.Minute,
	}, nil
}
