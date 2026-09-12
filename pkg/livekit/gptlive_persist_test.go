package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	protocol "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// stubSummaryProvider is the smallest LLMProvider that can produce a session
// summary, so the gptlive post-session tail can be driven end to end without a
// real model.
type stubSummaryProvider struct {
	mu     sync.Mutex
	prompt string
}

func (p *stubSummaryProvider) Chat(
	_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	if len(messages) > 0 {
		p.prompt = messages[len(messages)-1].Content
	}
	p.mu.Unlock()
	return &providers.LLMResponse{Content: "the child answered the spider question"}, nil
}

func (p *stubSummaryProvider) GetDefaultModel() string { return "stub" }

func (p *stubSummaryProvider) summarizedPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prompt
}

// stubSessionStore is an in-memory session.SessionStore. It starts with no
// history for any key, which is the state a gptlive session's store is
// actually in: the bridge never sees a turn.
type stubSessionStore struct {
	mu        sync.Mutex
	history   map[string][]providers.Message
	summaries map[string]string
}

func newStubSessionStore() *stubSessionStore {
	return &stubSessionStore{history: map[string][]providers.Message{}, summaries: map[string]string{}}
}

func (s *stubSessionStore) AddMessage(key, role, content string) {
	s.AddFullMessage(key, providers.Message{Role: role, Content: content})
}
func (s *stubSessionStore) AddFullMessage(key string, msg providers.Message) {
	s.mu.Lock()
	s.history[key] = append(s.history[key], msg)
	s.mu.Unlock()
}
func (s *stubSessionStore) GetHistory(key string) []providers.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]providers.Message(nil), s.history[key]...)
}
func (s *stubSessionStore) GetSummary(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summaries[key]
}
func (s *stubSessionStore) SetSummary(key, summary string) {
	s.mu.Lock()
	s.summaries[key] = summary
	s.mu.Unlock()
}
func (s *stubSessionStore) SetHistory(key string, history []providers.Message) {
	s.mu.Lock()
	s.history[key] = append([]providers.Message(nil), history...)
	s.mu.Unlock()
}
func (s *stubSessionStore) TruncateHistory(string, int) {}
func (s *stubSessionStore) Save(string) error           { return nil }
func (s *stubSessionStore) Close() error                { return nil }

// TestPersistGPTLiveSessionSendsUsageAndTranscript is the test Task 13's
// review (Important 4) says should have existed from the start: it is what
// would have caught Critical 1 (persistGPTLiveSession populating only
// TotalTokens, leaving InputTokens/OutputTokens at zero, which made
// sendUsageSummary's own guard — "if usage.InputTokens == 0 &&
// usage.OutputTokens == 0 { return nil }" — skip the POST to
// /device/token-usage for every single gptlive session, silently). This
// drives a real pipeline through onEvent exactly the way a live session would
// (a user turn, an agent turn, a BackendUsage report, a VoiceUsage report),
// then calls persistGPTLiveSession against a fake manager API and asserts all
// three endpoints were actually hit with the expected payload contents.
func TestPersistGPTLiveSessionSendsUsageAndTranscript(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hello", Final: true})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "hi there", Text: "hi there"})
	p.onBurstClose()
	p.onEvent(gptlive.BackendUsage{Total: 120, Input: 80, Output: 40})
	p.onEvent(gptlive.VoiceUsage{Seconds: 12.5})

	var chatHistoryHit, sessionEndHit, usageHit bool
	var chatHistoryPayload, sessionEndPayload, usagePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat-history/session":
			chatHistoryHit = true
			if err := json.NewDecoder(r.Body).Decode(&chatHistoryPayload); err != nil {
				t.Fatalf("decode chat-history payload: %v", err)
			}
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/end":
			sessionEndHit = true
			if err := json.NewDecoder(r.Body).Decode(&sessionEndPayload); err != nil {
				t.Fatalf("decode session-end payload: %v", err)
			}
		case "/device/token-usage":
			usageHit = true
			if err := json.NewDecoder(r.Body).Decode(&usagePayload); err != nil {
				t.Fatalf("decode usage payload: %v", err)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	// bridge is nil here: this test is only about the manager-API payloads, and
	// the summary/MEMORY.md/trace tail that a real bridge drives has its own
	// test below.
	rs.persistGPTLiveSession(p, nil)

	if !chatHistoryHit {
		t.Error("chat-history endpoint was never hit")
	}
	if !sessionEndHit {
		t.Error("session-end endpoint was never hit")
	}
	if !usageHit {
		t.Fatal("usage endpoint was never hit — this is exactly Critical 1: InputTokens/OutputTokens must be non-zero for sendUsageSummary to actually POST")
	}
	if got := chatHistoryPayload["messageCount"]; got != float64(2) {
		t.Errorf("chat-history messageCount = %#v, want 2", got)
	}
	if got := sessionEndPayload["messageCount"]; got != float64(2) {
		t.Errorf("session-end messageCount = %#v, want 2", got)
	}
	if got := usagePayload["inputTokens"]; got != float64(80) {
		t.Errorf("usage inputTokens = %#v, want 80", got)
	}
	if got := usagePayload["outputTokens"]; got != float64(40) {
		t.Errorf("usage outputTokens = %#v, want 40", got)
	}
	if got := usagePayload["totalTokens"]; got != float64(120) {
		t.Errorf("usage totalTokens = %#v, want 120", got)
	}
	if got := usagePayload["sessionDurationSeconds"]; got != float64(12.5) {
		t.Errorf("usage sessionDurationSeconds = %#v, want 12.5", got)
	}
}

// TestPersistGPTLiveSessionWithNoUsageSkipsUsagePost covers the mirror case:
// a pipeline that never produced a transcript or any backend/voice usage (a
// session that connected and was torn down before any turn completed) must
// not post an empty chat-history payload, still reports the session end (so
// the manager API's session record closes out), and correctly skips the
// usage endpoint — zero tokens really is "nothing to report" here, not a
// dropped update, since sendChatHistory/sendSessionEnd/sendUsageSummary's own
// guards are what decide this, not persistGPTLiveSession.
func TestPersistGPTLiveSessionWithNoUsageSkipsUsagePost(t *testing.T) {
	p := &gptLivePipeline{}

	var chatHistoryHit, sessionEndHit, usageHit bool
	var sessionEndPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat-history/session":
			chatHistoryHit = true
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/end":
			sessionEndHit = true
			if err := json.NewDecoder(r.Body).Decode(&sessionEndPayload); err != nil {
				t.Fatalf("decode session-end payload: %v", err)
			}
		case "/device/token-usage":
			usageHit = true
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	rs.persistGPTLiveSession(p, nil)

	if chatHistoryHit {
		t.Error("chat-history endpoint was hit for an empty transcript; sendChatHistory should have no-op'd")
	}
	if !sessionEndHit {
		t.Error("session-end endpoint was never hit; it should still close out the session record even with no usage")
	}
	if got := sessionEndPayload["messageCount"]; got != float64(0) {
		t.Errorf("session-end messageCount = %#v, want 0", got)
	}
	if usageHit {
		t.Error("usage endpoint was hit for a session with zero tokens; sendUsageSummary's own guard should have skipped it")
	}
}

// TestPersistGPTLiveSessionRunsTheFullPostSessionTail covers the final
// whole-branch review's Important 2 and Important 5 together, because they
// share one call site.
//
// leave() used to run ONLY persistGPTLiveSession for a gptlive session — chat
// history, session end, usage — and skip persistPostSessionData outright. Four
// things went with it: sendCharacterProgress (so kid_character_state froze for
// Quizzy/Bujho/Ginti), finalizeAndPersistSessionSummary and its
// persistSummaryToMemoryFile (so memory/MEMORY.md, which pkg/agent/context.go
// reads at every session start, was never written again — cross-session
// continuity degrading for every character from day one on a box where they
// all run gptlive), sendSessionSummary (what the parent app shows), and
// exportSessionTraceBundle (the only thing that could diagnose any of it).
// Nothing invoked QuizTrackerConfig.AttemptReporter either, though main.go had
// been wiring it up the whole time.
//
// Every one of those is asserted here, through the real call leave() makes.
func TestPersistGPTLiveSessionRunsTheFullPostSessionTail(t *testing.T) {
	workspace := t.TempDir()

	attempts := make(chan int64, 4)
	tracker := NewQuizTracker(QuizTrackerConfig{
		Batch: testBatch(), Workspace: workspace, MemoType: "daily_quiz",
		AttemptReporter: func(questionID int64, _ []QuizAttempt) { attempts <- questionID },
	})
	// Question 11 is scored (so the tracker writes memory/state/daily_quiz.md
	// and StateTypesWritten names it); question 12 is left mid-try, which is
	// the case AttemptReporter exists for.
	if _, err := tracker.Score("11", "correct", "eight"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Score("12", "miss", "purple"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	hits := map[string]bool{}
	var progressPayload map[string]any
	var summaryPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path] = true
		switch r.URL.Path {
		case "/progress/session":
			_ = json.NewDecoder(r.Body).Decode(&progressPayload)
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-tail/summary":
			_ = json.NewDecoder(r.Body).Decode(&summaryPayload)
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	provider := &stubSummaryProvider{}
	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{Workspace: workspace},
		characterName: "Quizzy",
		sessions:      newStubSessionStore(),
		provider:      provider,
		modelID:       "stub",
	}

	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "eight legs", Final: true})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "that's right!", Text: "that's right!"})
	p.onBurstClose()
	p.onEvent(gptlive.BackendUsage{Total: 30, Input: 20, Output: 10})

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		characterName:    "Quizzy",
		roomInfo:         &protocol.Room{Name: "session-tail"},
		gptLiveSpec:      &GPTLiveSessionSpec{Quiz: tracker},
	}

	rs.persistGPTLiveSession(p, bridge)

	// Important 5: the unfinished question's tries were reported.
	select {
	case id := <-attempts:
		if id != 12 {
			t.Errorf("AttemptReporter got question %d, want 12 (the one the session ended mid-try on)", id)
		}
	default:
		t.Error("AttemptReporter was never invoked: a child who never finishes a question produces no attempt rows at all")
	}

	mu.Lock()
	defer mu.Unlock()

	// Important 2, part 1: character progress, with the memo type the TRACKER
	// wrote (the bridge wrote none, so bridge.StateTypesWritten() is empty and
	// CollectStateMemos would otherwise collect nothing).
	if !hits["/progress/session"] {
		t.Fatal("character progress was never posted: kid_character_state stops updating for the quiz characters")
	}
	memos, _ := progressPayload["memos"].([]any)
	if len(memos) != 1 {
		t.Fatalf("progress payload carried %d memos, want the session's daily_quiz MEMO: %#v", len(memos), progressPayload["memos"])
	}
	if got, _ := memos[0].(map[string]any)["type"].(string); got != "daily_quiz" {
		t.Errorf("progress memo type = %q, want daily_quiz", got)
	}

	// Important 2, part 2: the summary, built from the PIPELINE's transcript —
	// the bridge holds none of the session's turns, so its own snapshot (the
	// fallback FinalizeSessionSummary uses on the cascade) is empty here.
	if !strings.Contains(provider.summarizedPrompt(), "eight legs") {
		t.Errorf("the summarizer was not given the gptlive transcript; prompt was: %q", provider.summarizedPrompt())
	}
	if !hits["/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-tail/summary"] {
		t.Error("the session summary was never uploaded, so the parent app has nothing to show")
	}
	if got, _ := summaryPayload["summary"].(string); got != "the child answered the spider question" {
		t.Errorf("uploaded summary = %q", got)
	}

	// Important 2, part 3: MEMORY.md, the only cross-session continuity a
	// character has, and the one thing that works with no manager API at all.
	memory, err := os.ReadFile(filepath.Join(workspace, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("MEMORY.md was never written: %v", err)
	}
	if !strings.Contains(string(memory), "the child answered the spider question") {
		t.Errorf("MEMORY.md is missing this session's summary:\n%s", memory)
	}
	if !strings.Contains(string(memory), "[Quizzy]") {
		t.Errorf("MEMORY.md entry is not labelled with the character:\n%s", memory)
	}

	// Important 2, part 4: the trace bundle.
	traces, err := filepath.Glob(filepath.Join(workspace, "trace", "session-trace-*.json"))
	if err != nil || len(traces) != 1 {
		t.Fatalf("expected exactly one exported trace bundle, got %v (err %v)", traces, err)
	}
}

// TestPersistGPTLiveSessionWritesMemoryWithNoManagerAPI is the file-memory-mode
// half of Important 2: persistGPTLiveSession used to return immediately when
// the manager API was not configured, which would have taken the MEMORY.md
// write down with it once the tail moved in here. The manager POSTs are
// optional; the local continuity write is not.
func TestPersistGPTLiveSessionWritesMemoryWithNoManagerAPI(t *testing.T) {
	workspace := t.TempDir()
	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{Workspace: workspace},
		characterName: "Cheeko",
		sessions:      newStubSessionStore(),
		provider:      &stubSummaryProvider{},
		modelID:       "stub",
	}
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "tell me about trains", Final: true})

	rs := &RoomSession{roomInfo: &protocol.Room{Name: "session-nomanager"}}
	rs.persistGPTLiveSession(p, bridge)

	memory, err := os.ReadFile(filepath.Join(workspace, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("MEMORY.md was never written in file-memory mode: %v", err)
	}
	if !strings.Contains(string(memory), "the child answered the spider question") {
		t.Errorf("MEMORY.md is missing the summary:\n%s", memory)
	}
}
