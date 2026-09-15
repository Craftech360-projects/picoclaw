package main

import (
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/tools"
)

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
	APIKey          string
	Realtime        realtimeChoice    // vendor, key and model chosen for this session (chooseRealtime)
	CharacterVoices map[string]string // vendor -> the character's voice for it
	Metadata        roomMetadata
	Workspace       string
	CharacterName   string
	GreetingPrompt  string
	LanguageName    string
	BaseTools       *tools.ToolRegistry
	QuizBatch       *livekit.QuizBatch        // nil when the persona has no quiz placeholder
	QuizReporters   livekit.QuizTrackerConfig // AnswerReporter, AttemptReporter, WonderReporter only; Batch/Workspace/MemoType are filled in below
	BankBlock       string
}

// buildGPTLiveSpec validates the untrusted gptlive dispatch-metadata block
// and composes the immutable per-session GPT-Live spec. Voice/accent/rate
// fall back to safe defaults for anything unrecognised — an unknown voice, a
// bogus rate, or a missing "gptlive" block entirely are all normal dispatch
// traffic, not errors. Only a missing API key (none in the manager realtime
// provider row, see chooseRealtime) fails the session
// outright: once the gptlive pipeline is selected there is no cascade
// STT/TTS to fall back to.
func buildGPTLiveSpec(in gptLiveSpecInput) (*livekit.GPTLiveSessionSpec, error) {
	choice := in.Realtime
	if choice.Vendor == "" { // callers that predate vendor choice (tests) pass only APIKey
		choice = realtimeChoice{Vendor: livekit.VendorOpenAI, APIKey: in.APIKey}
	}
	if strings.TrimSpace(choice.APIKey) == "" {
		return nil, errors.New("no API key in the manager realtime provider row for the realtime pipeline")
	}
	metaVoice, accent := "", "default"
	if g := in.Metadata.GPTLive; g != nil {
		metaVoice = g.Voice
		if strings.EqualFold(strings.TrimSpace(g.Accent), "indian") {
			accent = "indian"
		}
	}
	voice := chooseRealtimeVoice(choice.Vendor, metaVoice, in.CharacterVoices[choice.Vendor], choice.Voice)
	// Output is always 24kHz (all three vendors); Gemini's Live API only takes 16kHz mic input.
	rate, inRate := 24000, 24000
	if choice.Vendor == livekit.VendorGoogle {
		inRate = 16000
	}
	singleModel := choice.Vendor != livekit.VendorOpenAI
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
		SingleModel:    singleModel,
	})
	backendModel := choice.BackendModel
	if backendModel == "" {
		backendModel = gptlive.DefaultBackendModel
	}
	return &livekit.GPTLiveSessionSpec{
		APIKey:             choice.APIKey,
		Vendor:             choice.Vendor,
		Model:              choice.Model,
		BaseURL:            choice.BaseURL,
		Voice:              voice,
		SampleRate:         rate,
		InRate:             inRate,
		BackendModel:       backendModel,
		Persona:            persona,
		Tools:              livekit.BuildGPTLiveTools(in.BaseTools, in.Workspace, quiz),
		Quiz:               quiz,
		WebSearch:          true,
		MaxSessionDuration: 55 * time.Minute,
	}, nil
}

// gptLiveDeclaredToolCount is how many tools the session declares to the vendor (function
// tools plus hosted web search), for the "gptlive: session spec selected" log line.
func gptLiveDeclaredToolCount(spec *livekit.GPTLiveSessionSpec) int {
	if spec == nil || spec.Tools == nil {
		return 0
	}
	return len(livekit.GPTLiveToolDefs(spec.Tools, spec.WebSearch))
}
