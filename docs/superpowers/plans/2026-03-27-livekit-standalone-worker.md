# LiveKit Voice Agent Standalone Worker - Implementation Plan (Approach C)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone `picoclaw-livekit` binary that acts as a LiveKit named agent worker, auto-dispatched by LiveKit server, handling voice conversations via Deepgram STT + ElevenLabs TTS + PicoClaw's LLM providers.

**Architecture:** Go binary implements the LiveKit agent worker WebSocket protocol (same as Python/Node frameworks). Registers with `agent_name`, receives job assignments, joins rooms, runs a Deepgram→AgentBridge→ElevenLabs voice pipeline per participant. AgentBridge is a simplified agent turn loop calling `StreamingProvider.ChatStream()` directly.

**Tech Stack:** Go, `livekit/server-sdk-go/v2`, `livekit/protocol` (agent protobuf), Deepgram WebSocket API, ElevenLabs streaming TTS, `gorilla/websocket`

**Spec:** `docs/superpowers/specs/2026-03-27-livekit-standalone-service-design.md`

---

## File Structure

### New Files

```
pkg/voice/deepgram/
    types.go               # StreamOpts, TranscriptEvent, Deepgram API response types
    streaming.go           # DeepgramTranscriber + deepgramStream (WebSocket client)
    streaming_test.go      # Mock WebSocket tests

pkg/voice/elevenlabs_tts/
    types.go               # TTSConfig, AudioStream interface
    tts.go                 # ElevenLabsTTS provider (streaming HTTP)
    tts_test.go            # Mock HTTP tests

pkg/livekit/
    worker.go              # LiveKit agent worker (WebSocket dispatch protocol)
    worker_test.go         # Worker registration + job lifecycle tests
    agent_bridge.go        # Simplified agent turn loop with streaming
    agent_bridge_test.go   # Bridge unit tests
    room_session.go        # Per-job room lifecycle + participant state
    room_session_test.go   # Room session tests
    audio_pipeline.go      # Per-participant STT→Agent→TTS coordinator
    audio_pipeline_test.go # Pipeline + interruption tests

cmd/picoclaw-livekit/
    main.go                # CLI entrypoint with --agent-name flag
```

### Modified Files

```
pkg/config/config.go      # Add LiveKitServiceConfig struct + field on Config
pkg/config/security.go    # Add LiveKitServiceSecurity struct + field
Makefile                   # Add build-livekit target
go.mod / go.sum            # New dependencies
```

---

## Task 1: Deepgram Streaming STT - Types & Implementation

**Files:**
- Create: `pkg/voice/deepgram/types.go`
- Create: `pkg/voice/deepgram/streaming.go`
- Create: `pkg/voice/deepgram/streaming_test.go`

> **This task is identical to Approach A Tasks 1-2.** See `docs/superpowers/plans/2026-03-27-livekit-voice-agent.md` Tasks 1-2 for complete code. The package is shared between both approaches.

- [ ] **Step 1: Create `pkg/voice/deepgram/types.go`** with `StreamOpts`, `TranscriptEvent`, `TranscriptionStream`, `StreamingTranscriber`, `deepgramResponse` types. Include `SpeechStart bool` field on `TranscriptEvent`.

- [ ] **Step 2: Write failing tests in `pkg/voice/deepgram/streaming_test.go`** — `TestDeepgramStream_ReceivesTranscript` and `TestDeepgramStream_ContextCancellation` using mock WebSocket server.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd d:/picoclaw && go test ./pkg/voice/deepgram/ -v`
Expected: FAIL — `NewDeepgramTranscriber` not defined

- [ ] **Step 4: Implement `pkg/voice/deepgram/streaming.go`** — `DeepgramTranscriber`, `deepgramStream` with write mutex, `OpenStream()`, `SendAudio()`, `readLoop()`, `Close()`. Detect `SpeechStart` from first non-empty interim result.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/voice/deepgram/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/voice/deepgram/
git commit -m "feat(voice): implement Deepgram streaming STT"
```

---

## Task 2: ElevenLabs TTS - Types & Implementation

**Files:**
- Create: `pkg/voice/elevenlabs_tts/types.go`
- Create: `pkg/voice/elevenlabs_tts/tts.go`
- Create: `pkg/voice/elevenlabs_tts/tts_test.go`

> **This task is identical to Approach A Tasks 3-4.** See `docs/superpowers/plans/2026-03-27-livekit-voice-agent.md` Tasks 3-4 for complete code.

- [ ] **Step 1: Create `pkg/voice/elevenlabs_tts/types.go`** with `TTSConfig`, `AudioStream` interface, `TTSProvider` interface.

- [ ] **Step 2: Write failing tests in `pkg/voice/elevenlabs_tts/tts_test.go`** — `TestElevenLabsTTS_Synthesize` and `TestElevenLabsTTS_ContextCancellation` using mock HTTP server.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd d:/picoclaw && go test ./pkg/voice/elevenlabs_tts/ -v`
Expected: FAIL

- [ ] **Step 4: Implement `pkg/voice/elevenlabs_tts/tts.go`** — `ElevenLabsTTS`, `Synthesize()`, `elevenLabsAudioStream` with proper EOF vs error handling.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/voice/elevenlabs_tts/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/voice/elevenlabs_tts/
git commit -m "feat(voice): implement ElevenLabs streaming TTS"
```

---

## Task 3: Config - LiveKit Service Config Structs

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/security.go`

- [ ] **Step 1: Add LiveKitServiceConfig to `pkg/config/config.go`**

Add after existing config structs (around line 700):

```go
// LiveKitServiceTTSConfig configures TTS for the LiveKit voice agent.
type LiveKitServiceTTSConfig struct {
	VoiceID      string `json:"voice_id"      env:"PICOCLAW_LIVEKIT_TTS_VOICE_ID"`
	ModelID      string `json:"model_id"      env:"PICOCLAW_LIVEKIT_TTS_MODEL_ID"`
	OutputFormat string `json:"output_format" env:"PICOCLAW_LIVEKIT_TTS_OUTPUT_FORMAT"`
}

// LiveKitServiceConfig configures the standalone LiveKit voice agent service.
type LiveKitServiceConfig struct {
	ServerURL string                  `json:"server_url" env:"PICOCLAW_LIVEKIT_SERVER_URL"`
	TTS       LiveKitServiceTTSConfig `json:"tts"`

	apiKey         string
	apiSecret      string
	deepgramAPIKey string
	secDirty       bool
}

func (c *LiveKitServiceConfig) APIKey() string          { return c.apiKey }
func (c *LiveKitServiceConfig) APISecret() string       { return c.apiSecret }
func (c *LiveKitServiceConfig) DeepgramAPIKey() string  { return c.deepgramAPIKey }
func (c *LiveKitServiceConfig) SetAPIKey(k string)      { c.apiKey = k; c.secDirty = true }
func (c *LiveKitServiceConfig) SetAPISecret(s string)   { c.apiSecret = s; c.secDirty = true }
func (c *LiveKitServiceConfig) SetDeepgramAPIKey(k string) { c.deepgramAPIKey = k; c.secDirty = true }
```

- [ ] **Step 2: Add `LiveKitService` field to the main `Config` struct**

Find the `Config` struct in `pkg/config/config.go` and add:

```go
LiveKitService LiveKitServiceConfig `json:"livekit_service" envPrefix:"PICOCLAW_LIVEKIT_"`
```

- [ ] **Step 3: Add LiveKitServiceSecurity to `pkg/config/security.go`**

Add security struct:

```go
type LiveKitServiceSecurity struct {
	APIKey         string `yaml:"api_key,omitempty"          env:"PICOCLAW_LIVEKIT_API_KEY"`
	APISecret      string `yaml:"api_secret,omitempty"       env:"PICOCLAW_LIVEKIT_API_SECRET"`
	DeepgramAPIKey string `yaml:"deepgram_api_key,omitempty" env:"PICOCLAW_LIVEKIT_DEEPGRAM_API_KEY"`
}
```

Add to `SecurityConfig` struct:

```go
LiveKitService *LiveKitServiceSecurity `yaml:"livekit_service,omitempty"`
```

- [ ] **Step 4: Wire security in `applySecurityConfig`**

In `pkg/config/config.go`, find `applySecurityConfig` and add:

```go
if sec.LiveKitService != nil {
	if sec.LiveKitService.APIKey != "" {
		cfg.LiveKitService.SetAPIKey(sec.LiveKitService.APIKey)
	}
	if sec.LiveKitService.APISecret != "" {
		cfg.LiveKitService.SetAPISecret(sec.LiveKitService.APISecret)
	}
	if sec.LiveKitService.DeepgramAPIKey != "" {
		cfg.LiveKitService.SetDeepgramAPIKey(sec.LiveKitService.DeepgramAPIKey)
	}
}
```

- [ ] **Step 5: Verify it compiles**

Run: `cd d:/picoclaw && go build ./pkg/config/`
Expected: No errors

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/security.go
git commit -m "feat(config): add LiveKit service configuration"
```

---

## Task 4: Add Dependencies

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add LiveKit SDK + protocol**

```bash
cd d:/picoclaw
go get github.com/livekit/server-sdk-go/v2@latest
go get github.com/livekit/protocol@latest
go mod tidy
```

- [ ] **Step 2: Verify dependencies resolve**

Run: `cd d:/picoclaw && go build ./...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add LiveKit server-sdk-go and protocol"
```

---

## Task 5: AgentBridge - Simplified Agent Turn Loop

**Files:**
- Create: `pkg/livekit/agent_bridge.go`
- Create: `pkg/livekit/agent_bridge_test.go`

- [ ] **Step 1: Write failing test**

```go
package livekit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// mockStreamingProvider implements providers.StreamingProvider for testing.
type mockStreamingProvider struct {
	responses []string // each string is the full response for one turn
	turnIndex int
}

func (m *mockStreamingProvider) ChatStream(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
	cb func(chunk providers.StreamChunk),
) (*providers.LLMResponse, error) {
	if m.turnIndex >= len(m.responses) {
		return &providers.LLMResponse{Content: "", FinishReason: "end_turn"}, nil
	}
	resp := m.responses[m.turnIndex]
	m.turnIndex++

	// Stream word by word
	words := strings.Fields(resp)
	for i, word := range words {
		if i > 0 {
			word = " " + word
		}
		cb(providers.StreamChunk{Text: word})
	}

	return &providers.LLMResponse{
		Content:      resp,
		FinishReason: "end_turn",
	}, nil
}

func TestAgentBridge_ChatStream_Basic(t *testing.T) {
	provider := &mockStreamingProvider{
		responses: []string{"Hello, how can I help you today?"},
	}

	bridge := &AgentBridge{
		provider:      provider,
		maxIterations: 5,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var chunks []string
	err := bridge.ChatStream(ctx, "test-session", "Hi there", func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	fullResponse := strings.Join(chunks, "")
	if fullResponse != "Hello, how can I help you today?" {
		t.Fatalf("expected full response, got %q", fullResponse)
	}
}

func TestAgentBridge_ChatStream_ContextCancel(t *testing.T) {
	provider := &mockStreamingProvider{
		responses: []string{"This is a very long response that should be cancelled"},
	}

	bridge := &AgentBridge{
		provider:      provider,
		maxIterations: 5,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := bridge.ChatStream(ctx, "test-session", "Hi", func(chunk string) {})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd d:/picoclaw && go test ./pkg/livekit/ -v -run TestAgentBridge`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement AgentBridge**

```go
package livekit

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// AgentBridge provides a simplified agent execution path for voice conversations.
// One instance per participant to isolate state.
type AgentBridge struct {
	provider      providers.StreamingProvider
	maxIterations int
	systemPrompt  string
}

// NewAgentBridge creates a new bridge.
func NewAgentBridge(provider providers.StreamingProvider, systemPrompt string, maxIterations int) *AgentBridge {
	if maxIterations <= 0 {
		maxIterations = 10
	}
	return &AgentBridge{
		provider:      provider,
		maxIterations: maxIterations,
		systemPrompt:  systemPrompt,
	}
}

// ChatStream sends a user message through the LLM and streams the response.
// The callback fires for each text chunk as it arrives from the LLM.
// Handles tool call iterations internally.
func (ab *AgentBridge) ChatStream(ctx context.Context, sessionKey string, text string, cb func(chunk string)) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Build messages
	messages := []providers.Message{}
	if ab.systemPrompt != "" {
		messages = append(messages, providers.Message{
			Role:    "system",
			Content: ab.systemPrompt,
		})
	}
	messages = append(messages, providers.Message{
		Role:    "user",
		Content: text,
	})

	// Turn loop (handles tool calls)
	for iteration := 0; iteration < ab.maxIterations; iteration++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := ab.provider.ChatStream(ctx, messages, nil, "", nil, func(chunk providers.StreamChunk) {
			if chunk.Text != "" {
				cb(chunk.Text)
			}
		})
		if err != nil {
			return fmt.Errorf("LLM call failed: %w", err)
		}

		// Add assistant response to messages
		messages = append(messages, providers.Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Check for tool calls
		if len(resp.ToolCalls) == 0 {
			return nil // done, no tool calls
		}

		// Execute tool calls and add results
		for _, tc := range resp.ToolCalls {
			// TODO: execute tool via registry, add result message
			messages = append(messages, providers.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Tool %s not yet implemented", tc.Name),
				ToolCallID: tc.ID,
			})
		}
		// Loop back for next LLM call
	}

	return fmt.Errorf("max iterations (%d) reached", ab.maxIterations)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/livekit/ -v -run TestAgentBridge`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/agent_bridge.go pkg/livekit/agent_bridge_test.go
git commit -m "feat(livekit): implement AgentBridge with streaming turn loop"
```

---

## Task 6: Worker - LiveKit Agent Dispatch Protocol

**Files:**
- Create: `pkg/livekit/worker.go`
- Create: `pkg/livekit/worker_test.go`

- [ ] **Step 1: Write failing test for worker registration**

```go
package livekit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

func TestWorker_Registration(t *testing.T) {
	var receivedMsg *livekit.WorkerMessage

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Read registration message
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
			return
		}
		receivedMsg = &livekit.WorkerMessage{}
		if err := proto.Unmarshal(data, receivedMsg); err != nil {
			t.Fatalf("unmarshal: %v", err)
			return
		}

		// Send registration response
		resp := &livekit.ServerMessage{
			Message: &livekit.ServerMessage_Register{
				Register: &livekit.RegisterWorkerResponse{
					WorkerId: "worker-123",
				},
			},
		}
		respData, _ := proto.Marshal(resp)
		conn.WriteMessage(websocket.BinaryMessage, respData)

		// Hold connection
		select {}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	w := &Worker{
		agentName: "test-agent",
		serverURL: wsURL,
		jobs:      make(map[string]*RoomSession),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run in background
	go w.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// Verify registration was sent
	if receivedMsg == nil {
		t.Fatal("no registration message received")
	}
	reg := receivedMsg.GetRegister()
	if reg == nil {
		t.Fatal("expected RegisterWorkerRequest")
	}
	if reg.AgentName != "test-agent" {
		t.Fatalf("expected agent_name='test-agent', got %q", reg.AgentName)
	}
}

func TestWorker_JobAssignment(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Read registration
		_, _, _ = conn.ReadMessage()

		// Send registration response
		resp := &livekit.ServerMessage{
			Message: &livekit.ServerMessage_Register{
				Register: &livekit.RegisterWorkerResponse{WorkerId: "w-1"},
			},
		}
		data, _ := proto.Marshal(resp)
		conn.WriteMessage(websocket.BinaryMessage, data)

		// Send availability request
		avail := &livekit.ServerMessage{
			Message: &livekit.ServerMessage_Availability{
				Availability: &livekit.AvailabilityRequest{
					Job: &livekit.Job{
						Id:   "job-42",
						Type: livekit.JobType_JT_ROOM,
						Room: &livekit.Room{Name: "test-room"},
					},
				},
			},
		}
		data2, _ := proto.Marshal(avail)
		conn.WriteMessage(websocket.BinaryMessage, data2)

		// Read availability response
		_, respData, _ := conn.ReadMessage()
		workerMsg := &livekit.WorkerMessage{}
		proto.Unmarshal(respData, workerMsg)

		availResp := workerMsg.GetAvailability()
		if availResp == nil {
			t.Fatal("expected AvailabilityResponse")
		}
		if !availResp.Available {
			t.Fatal("expected available=true")
		}

		select {}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	w := &Worker{
		agentName:    "test-agent",
		serverURL:    wsURL,
		jobs:         make(map[string]*RoomSession),
		skipRoomJoin: true, // test mode: don't actually join LiveKit rooms
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go w.Run(ctx)
	time.Sleep(1 * time.Second)

	// Verify job was tracked
	w.mu.RLock()
	_, exists := w.jobs["job-42"]
	w.mu.RUnlock()

	// Job may or may not exist depending on timing, but no panic = success
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd d:/picoclaw && go test ./pkg/livekit/ -v -run TestWorker`
Expected: FAIL — `Worker` struct not complete

- [ ] **Step 3: Implement Worker**

```go
package livekit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// Worker implements the LiveKit agent worker dispatch protocol.
type Worker struct {
	agentName  string
	serverURL  string
	apiKey     string
	apiSecret  string
	conn       *websocket.Conn
	workerID   string
	jobs       map[string]*RoomSession
	mu         sync.RWMutex

	// Factory dependencies for creating per-job resources
	bridgeFactory func() *AgentBridge
	roomFactory   func(jobID string, room *livekit.Room, bridge *AgentBridge) (*RoomSession, error)

	// Test mode: skip actual LiveKit room joining
	skipRoomJoin bool
}

// WorkerConfig holds configuration for creating a Worker.
type WorkerConfig struct {
	AgentName     string
	ServerURL     string
	APIKey        string
	APISecret     string
	BridgeFactory func() *AgentBridge
	RoomFactory   func(jobID string, room *livekit.Room, bridge *AgentBridge) (*RoomSession, error)
}

// NewWorker creates a new LiveKit agent worker.
func NewWorker(cfg WorkerConfig) *Worker {
	return &Worker{
		agentName:     cfg.AgentName,
		serverURL:     cfg.ServerURL,
		apiKey:        cfg.APIKey,
		apiSecret:     cfg.APISecret,
		jobs:          make(map[string]*RoomSession),
		bridgeFactory: cfg.BridgeFactory,
		roomFactory:   cfg.RoomFactory,
	}
}

// Run connects to the LiveKit server and enters the dispatch loop.
// Blocks until ctx is cancelled or an unrecoverable error occurs.
func (w *Worker) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := w.connectAndServe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Reconnect with backoff
		_ = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (w *Worker) connectAndServe(ctx context.Context) error {
	header := make(map[string][]string)
	if w.apiKey != "" {
		header["Authorization"] = []string{"Bearer " + w.apiKey}
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, w.serverURL, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	w.conn = conn
	defer func() {
		conn.Close()
		w.conn = nil
	}()

	// Send registration
	regMsg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Register{
			Register: &livekit.RegisterWorkerRequest{
				AgentName: w.agentName,
			},
		},
	}
	if err := w.sendProto(regMsg); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Start ping loop
	go w.pingLoop(ctx)

	// Read loop
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		msg := &livekit.ServerMessage{}
		if err := proto.Unmarshal(data, msg); err != nil {
			continue
		}

		w.handleServerMessage(ctx, msg)
	}
}

func (w *Worker) handleServerMessage(ctx context.Context, msg *livekit.ServerMessage) {
	switch m := msg.Message.(type) {
	case *livekit.ServerMessage_Register:
		w.workerID = m.Register.WorkerId

	case *livekit.ServerMessage_Availability:
		w.handleAvailability(ctx, m.Availability)

	case *livekit.ServerMessage_Assignment:
		w.handleAssignment(ctx, m.Assignment)

	case *livekit.ServerMessage_Termination:
		w.handleTermination(m.Termination)

	case *livekit.ServerMessage_Pong:
		// heartbeat response, no action needed
	}
}

func (w *Worker) handleAvailability(ctx context.Context, req *livekit.AvailabilityRequest) {
	resp := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: &livekit.AvailabilityResponse{
				JobId:     req.Job.Id,
				Available: true,
			},
		},
	}
	w.sendProto(resp)
}

func (w *Worker) handleAssignment(ctx context.Context, assignment *livekit.JobAssignment) {
	job := assignment.Job
	if job == nil {
		return
	}

	if w.skipRoomJoin {
		// Test mode: just track the job
		w.mu.Lock()
		w.jobs[job.Id] = nil
		w.mu.Unlock()
		return
	}

	// Create per-job AgentBridge
	var bridge *AgentBridge
	if w.bridgeFactory != nil {
		bridge = w.bridgeFactory()
	}

	// Create and start room session
	if w.roomFactory != nil {
		session, err := w.roomFactory(job.Id, job.Room, bridge)
		if err != nil {
			w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED)
			return
		}

		w.mu.Lock()
		w.jobs[job.Id] = session
		w.mu.Unlock()

		go func() {
			if err := session.Join(ctx); err != nil {
				w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED)
				return
			}
			w.updateJobStatus(job.Id, livekit.JobStatus_JS_RUNNING)
		}()
	}
}

func (w *Worker) handleTermination(term *livekit.JobTermination) {
	w.mu.Lock()
	session, ok := w.jobs[term.JobId]
	delete(w.jobs, term.JobId)
	w.mu.Unlock()

	if ok && session != nil {
		session.Leave()
	}
}

func (w *Worker) updateJobStatus(jobID string, status livekit.JobStatus) {
	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateJob{
			UpdateJob: &livekit.UpdateJobStatus{
				JobId:  jobID,
				Status: status,
			},
		},
	}
	w.sendProto(msg)
}

func (w *Worker) sendProto(msg *livekit.WorkerMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if w.conn == nil {
		return fmt.Errorf("not connected")
	}
	return w.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (w *Worker) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg := &livekit.WorkerMessage{
				Message: &livekit.WorkerMessage_Ping{
					Ping: &livekit.WorkerPing{
						Timestamp: time.Now().Unix(),
					},
				},
			}
			if err := w.sendProto(msg); err != nil {
				return
			}
		}
	}
}

// Shutdown gracefully stops all jobs and disconnects.
func (w *Worker) Shutdown() {
	w.mu.RLock()
	sessions := make([]*RoomSession, 0, len(w.jobs))
	for _, s := range w.jobs {
		if s != nil {
			sessions = append(sessions, s)
		}
	}
	w.mu.RUnlock()

	for _, s := range sessions {
		s.Leave()
	}

	if w.conn != nil {
		w.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		w.conn.Close()
	}
}
```

- [ ] **Step 4: Create stub RoomSession** (needed for Worker to compile)

```go
package livekit

import (
	"context"
	"sync"
	"sync/atomic"

	lksdk "github.com/livekit/server-sdk-go/v2"
	livekitproto "github.com/livekit/protocol/livekit"
)

// RoomSession manages one agent in one LiveKit room (one job).
type RoomSession struct {
	jobID       string
	roomInfo    *livekitproto.Room
	bridge      *AgentBridge
	room        *lksdk.Room
	participant *ParticipantState
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
}

// ParticipantState tracks per-participant voice session state.
type ParticipantState struct {
	identity   string
	sessionKey string
	ttsCancel  context.CancelFunc
	speaking   atomic.Bool
	mu         sync.Mutex
}

// NewRoomSession creates a new room session for a job.
func NewRoomSession(jobID string, roomInfo *livekitproto.Room, bridge *AgentBridge) (*RoomSession, error) {
	return &RoomSession{
		jobID:    jobID,
		roomInfo: roomInfo,
		bridge:   bridge,
	}, nil
}

// Join connects to the LiveKit room.
func (rs *RoomSession) Join(ctx context.Context) error {
	rs.ctx, rs.cancel = context.WithCancel(ctx)
	// TODO: implement room join with LiveKit SDK
	return nil
}

// Leave disconnects from the room.
func (rs *RoomSession) Leave() {
	if rs.cancel != nil {
		rs.cancel()
	}
	if rs.room != nil {
		rs.room.Disconnect()
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/livekit/ -v -run TestWorker`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/livekit/worker.go pkg/livekit/worker_test.go pkg/livekit/room_session.go
git commit -m "feat(livekit): implement Worker with LiveKit agent dispatch protocol"
```

---

## Task 7: Audio Pipeline - Sentence Splitter + Pipeline Coordinator

**Files:**
- Create: `pkg/livekit/audio_pipeline.go`
- Create: `pkg/livekit/audio_pipeline_test.go`

- [ ] **Step 1: Write failing tests**

```go
package livekit

import "testing"

func TestSentenceSplitter(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"Hello world.", []string{"Hello world."}},
		{"Hello. How are you?", []string{"Hello.", " How are you?"}},
		{"Hi! Great. Thanks?", []string{"Hi!", " Great.", " Thanks?"}},
		{"No punctuation", nil},
	}

	for _, tt := range tests {
		splitter := newSentenceSplitter()
		var results []string
		for _, r := range tt.input {
			if sentence := splitter.Feed(r); sentence != "" {
				results = append(results, sentence)
			}
		}
		if len(results) != len(tt.expected) {
			t.Fatalf("input=%q: got %v, want %v", tt.input, results, tt.expected)
		}
		for i, s := range results {
			if s != tt.expected[i] {
				t.Fatalf("input=%q: sentence[%d]=%q, want %q", tt.input, i, s, tt.expected[i])
			}
		}
	}
}

func TestSentenceSplitter_Flush(t *testing.T) {
	splitter := newSentenceSplitter()
	for _, r := range "No punctuation here" {
		splitter.Feed(r)
	}
	remainder := splitter.Flush()
	if remainder != "No punctuation here" {
		t.Fatalf("Flush()=%q, want %q", remainder, "No punctuation here")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd d:/picoclaw && go test ./pkg/livekit/ -v -run TestSentenceSplitter`
Expected: FAIL

- [ ] **Step 3: Implement audio pipeline**

```go
package livekit

import (
	"context"
	"fmt"
	"strings"
)

// sentenceSplitter accumulates text and emits complete sentences.
type sentenceSplitter struct {
	buf strings.Builder
}

func newSentenceSplitter() *sentenceSplitter {
	return &sentenceSplitter{}
}

func (s *sentenceSplitter) Feed(r rune) string {
	s.buf.WriteRune(r)
	if r == '.' || r == '!' || r == '?' {
		sentence := s.buf.String()
		s.buf.Reset()
		return sentence
	}
	return ""
}

func (s *sentenceSplitter) Flush() string {
	remaining := s.buf.String()
	s.buf.Reset()
	return remaining
}

// AudioPipeline coordinates STT→Agent→TTS for one participant in a room.
type AudioPipeline struct {
	session *RoomSession
	bridge  *AgentBridge
}

func NewAudioPipeline(session *RoomSession, bridge *AgentBridge) *AudioPipeline {
	return &AudioPipeline{
		session: session,
		bridge:  bridge,
	}
}

// HandleUtterance processes a complete user utterance: calls the agent and speaks the response.
func (ap *AudioPipeline) HandleUtterance(ctx context.Context, sessionKey string, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// ChatStream callback feeds sentences to TTS
	splitter := newSentenceSplitter()

	err := ap.bridge.ChatStream(ctx, sessionKey, text, func(chunk string) {
		for _, r := range chunk {
			if sentence := splitter.Feed(r); sentence != "" {
				// TODO (Task 9): synthesize sentence via TTS and play to audio track
				_ = sentence
			}
		}
	})
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	// Flush remaining text
	if remainder := splitter.Flush(); remainder != "" {
		// TODO (Task 9): synthesize remainder via TTS
		_ = remainder
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/livekit/ -v -run TestSentenceSplitter`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/audio_pipeline.go pkg/livekit/audio_pipeline_test.go
git commit -m "feat(livekit): implement audio pipeline with sentence splitting"
```

---

## Task 8: CLI Binary - `cmd/picoclaw-livekit/`

**Files:**
- Create: `cmd/picoclaw-livekit/main.go`
- Modify: `Makefile`

- [ ] **Step 1: Create main.go**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	lk "github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
	"github.com/sipeed/picoclaw/pkg/voice/elevenlabs_tts"
)

func main() {
	agentName := flag.String("agent-name", "", "LiveKit named agent identifier (required)")
	configPath := flag.String("config", "", "Path to config.json (default: ~/.picoclaw/config.json)")
	flag.Parse()

	if *agentName == "" {
		fmt.Fprintf(os.Stderr, "Error: --agent-name is required\n")
		flag.Usage()
		os.Exit(1)
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	lkCfg := cfg.LiveKitService

	// Create Deepgram transcriber
	var dgTranscriber *deepgram.DeepgramTranscriber
	if lkCfg.DeepgramAPIKey() != "" {
		dgTranscriber = deepgram.NewDeepgramTranscriber(lkCfg.DeepgramAPIKey())
	}

	// Create TTS config
	ttsCfg := elevenlabs_tts.TTSConfig{
		APIKey:       cfg.Voice.ElevenLabsAPIKey,
		VoiceID:      lkCfg.TTS.VoiceID,
		ModelID:      lkCfg.TTS.ModelID,
		OutputFormat: lkCfg.TTS.OutputFormat,
	}

	// TODO: Create LLM provider from config using providers.NewFactory()
	// TODO: Create session store

	fmt.Printf("🦞 picoclaw-livekit starting\n")
	fmt.Printf("   Agent: %s\n", *agentName)
	fmt.Printf("   Server: %s\n", lkCfg.ServerURL)
	fmt.Printf("   Deepgram: %v\n", dgTranscriber != nil)
	fmt.Printf("   TTS Voice: %s\n", ttsCfg.VoiceID)

	worker := lk.NewWorker(lk.WorkerConfig{
		AgentName: *agentName,
		ServerURL: lkCfg.ServerURL,
		APIKey:    lkCfg.APIKey(),
		APISecret: lkCfg.APISecret(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		worker.Shutdown()
		cancel()
	}()

	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Add build-livekit target to Makefile**

Add after the `build-launcher-tui` target:

```makefile
## build-livekit: Build the picoclaw-livekit (LiveKit voice agent worker) binary
build-livekit:
	@echo "Building picoclaw-livekit for $(PLATFORM)/$(ARCH)..."
	@mkdir -p $(BUILD_DIR)
	@$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/picoclaw-livekit-$(PLATFORM)-$(ARCH) ./cmd/picoclaw-livekit
	@ln -sf picoclaw-livekit-$(PLATFORM)-$(ARCH) $(BUILD_DIR)/picoclaw-livekit
	@echo "Build complete: $(BUILD_DIR)/picoclaw-livekit"
```

- [ ] **Step 3: Verify it builds**

Run: `cd d:/picoclaw && go build -tags goolm,stdjson -o build/picoclaw-livekit.exe ./cmd/picoclaw-livekit`
Expected: Compiles (may have unused import warnings to fix)

- [ ] **Step 4: Test CLI help**

Run: `./build/picoclaw-livekit.exe --help`
Expected: Shows usage with `--agent-name` flag

- [ ] **Step 5: Commit**

```bash
git add cmd/picoclaw-livekit/ Makefile
git commit -m "feat(livekit): add picoclaw-livekit CLI binary with --agent-name flag"
```

---

## Task 9: Wire TTS into Audio Pipeline

**Files:**
- Modify: `pkg/livekit/audio_pipeline.go`
- Modify: `pkg/livekit/room_session.go`

- [ ] **Step 1: Add TTS to AudioPipeline**

Update `AudioPipeline` struct and `HandleUtterance`:

```go
// Add to AudioPipeline struct:
tts *elevenlabs_tts.ElevenLabsTTS

// Update HandleUtterance to synthesize sentences:
func (ap *AudioPipeline) HandleUtterance(ctx context.Context, sessionKey string, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	splitter := newSentenceSplitter()

	err := ap.bridge.ChatStream(ctx, sessionKey, text, func(chunk string) {
		for _, r := range chunk {
			if sentence := splitter.Feed(r); sentence != "" {
				ap.synthesizeAndPlay(ctx, sentence)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	if remainder := splitter.Flush(); remainder != "" {
		ap.synthesizeAndPlay(ctx, remainder)
	}
	return nil
}

func (ap *AudioPipeline) synthesizeAndPlay(ctx context.Context, text string) {
	if ap.tts == nil {
		return
	}
	stream, err := ap.tts.Synthesize(ctx, text)
	if err != nil {
		return
	}
	defer stream.Close()

	for {
		chunk, err := stream.Read()
		if err == io.EOF {
			return
		}
		if err != nil {
			return // context cancelled or TTS error
		}
		// TODO (Task 10): write chunk to LiveKit local audio track
		_ = chunk
	}
}
```

- [ ] **Step 2: Add Deepgram inbound processing**

Add to `AudioPipeline`:

```go
// RunInbound reads Deepgram transcription events and calls the agent on speech end.
func (ap *AudioPipeline) RunInbound(ctx context.Context, dgStream deepgram.TranscriptionStream) {
	var utterance strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-dgStream.Results():
			if !ok {
				return
			}

			// Speech start: cancel active TTS (barge-in)
			if evt.SpeechStart && ap.session.participant != nil {
				ps := ap.session.participant
				if !ps.speaking.Load() {
					ps.speaking.Store(true)
					ps.mu.Lock()
					if ps.ttsCancel != nil {
						ps.ttsCancel()
					}
					ps.mu.Unlock()
				}
			}

			if evt.IsFinal && evt.Text != "" {
				utterance.WriteString(evt.Text)
				utterance.WriteString(" ")
			}

			if evt.SpeechEnd {
				text := strings.TrimSpace(utterance.String())
				utterance.Reset()
				if ap.session.participant != nil {
					ap.session.participant.speaking.Store(false)
				}

				if text != "" {
					sessionKey := fmt.Sprintf("livekit:%s:%s",
						ap.session.roomInfo.Name,
						ap.session.participant.identity)

					// Run in new goroutine so we don't block transcript reading
					ttsCtx, ttsCancel := context.WithCancel(ctx)
					ap.session.participant.mu.Lock()
					ap.session.participant.ttsCancel = ttsCancel
					ap.session.participant.mu.Unlock()

					go func() {
						defer ttsCancel()
						ap.HandleUtterance(ttsCtx, sessionKey, text)
					}()
				}
			}
		}
	}
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd d:/picoclaw && go build ./pkg/livekit/`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add pkg/livekit/audio_pipeline.go pkg/livekit/room_session.go
git commit -m "feat(livekit): wire Deepgram STT and ElevenLabs TTS into pipeline"
```

---

## Task 10: Integration Spike - LiveKit Room + PCM Audio

**Files:**
- Create: `pkg/livekit/spike_test.go`

- [ ] **Step 1: Write integration spike test (manual only)**

```go
//go:build livekit_spike

package livekit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/protocol/auth"
	"github.com/pion/webrtc/v4"
)

// Run with: go test ./pkg/livekit/ -tags livekit_spike -run TestSpike -v
// Requires: LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET env vars
func TestSpike_RoomJoinAndAudio(t *testing.T) {
	url := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")
	if url == "" || apiKey == "" || apiSecret == "" {
		t.Skip("LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET required")
	}

	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{RoomJoin: true, Room: "spike-test"}
	at.SetVideoGrant(grant).SetIdentity("picoclaw-spike")
	token, err := at.ToJWT()
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	room, err := lksdk.ConnectToRoom(url, token, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				fmt.Printf("Track: %s from %s\n", pub.Name(), rp.Identity())
			},
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer room.Disconnect()

	fmt.Printf("Connected to room %s as %s\n", room.Name(), room.LocalParticipant.Identity())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	<-ctx.Done()
}

// Test worker registration with real LiveKit server
func TestSpike_WorkerRegistration(t *testing.T) {
	url := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")
	if url == "" || apiKey == "" || apiSecret == "" {
		t.Skip("LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET required")
	}

	w := NewWorker(WorkerConfig{
		AgentName: "spike-test-agent",
		ServerURL: url,
		APIKey:    apiKey,
		APISecret: apiSecret,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := w.Run(ctx)
	fmt.Printf("Worker stopped: %v\n", err)
}
```

- [ ] **Step 2: Document spike findings** after running with real LiveKit server

- [ ] **Step 3: Commit**

```bash
git add pkg/livekit/spike_test.go
git commit -m "test(livekit): add integration spike for room join and worker registration"
```

---

## Task 11: Full Room Session Wiring

**Files:**
- Modify: `pkg/livekit/room_session.go`

This task depends on spike results from Task 10.

- [ ] **Step 1: Implement full RoomSession.Join()**

Wire up:
- Generate participant token with `auth.NewAccessToken()`
- Connect to room via `lksdk.ConnectToRoom()`
- Create local PCM audio track for TTS output
- Publish the track
- Set up `OnTrackSubscribed` callback to start audio pipeline
- Set up `OnParticipantDisconnected` to clean up

- [ ] **Step 2: Wire participant audio pipeline**

In `OnTrackSubscribed`:
- Create `ParticipantState`
- Open Deepgram stream
- Start `AudioPipeline.RunInbound()` goroutine
- Read PCM from LiveKit track, feed to Deepgram

- [ ] **Step 3: Wire TTS output to local track**

In `AudioPipeline.synthesizeAndPlay()`:
- Write PCM chunks from TTS to the local audio track

- [ ] **Step 4: End-to-end test**

Manual test:
1. Start LiveKit server
2. Run `./build/picoclaw-livekit --agent-name "test-bot"`
3. Dispatch agent: `lk dispatch create --agent-name test-bot --room test-room`
4. Join room from LiveKit Playground
5. Speak → verify transcription → verify agent response spoken back
6. Test interruption

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/
git commit -m "feat(livekit): wire full room session with PCM audio I/O"
```

---

## Summary

| Task | Component | New to Approach C |
|------|-----------|-------------------|
| 1 | Deepgram STT | No (shared with A) |
| 2 | ElevenLabs TTS | No (shared with A) |
| 3 | Config structs | Yes (top-level, not under channels) |
| 4 | Dependencies | Same |
| 5 | **AgentBridge** | **Yes (core new component)** |
| 6 | **Worker (dispatch protocol)** | **Yes (core new component)** |
| 7 | Audio pipeline | Mostly shared, slight differences |
| 8 | **CLI binary** | **Yes (new entrypoint)** |
| 9 | Wire TTS + STT | Similar to A |
| 10 | Integration spike | Yes (includes worker registration spike) |
| 11 | Full room session | Similar to A |

**Components unique to Approach C:** AgentBridge, Worker (dispatch protocol), CLI binary
**Components shared with Approach A:** Deepgram STT, ElevenLabs TTS, sentence splitter, audio pipeline core
