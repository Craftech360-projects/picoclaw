# LiveKit Voice Agent Channel - Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a real-time voice agent channel to PicoClaw using LiveKit rooms, Deepgram streaming STT, and ElevenLabs TTS.

**Architecture:** New channel plugin (`pkg/channels/livekit/`) that joins LiveKit rooms as a participant, transcribes user speech via Deepgram WebSocket streaming, routes text through PicoClaw's existing agent loop, and speaks responses back via ElevenLabs TTS. Each room participant gets an isolated session.

**Tech Stack:** Go, `livekit/server-sdk-go/v2`, Deepgram WebSocket API, ElevenLabs streaming TTS API, `gorilla/websocket`

**Spec:** `docs/superpowers/specs/2026-03-26-livekit-voice-agent-design.md`

---

## File Structure

### New Files

```
pkg/voice/deepgram/
    types.go             # StreamOpts, TranscriptEvent, Deepgram API response types
    streaming.go         # StreamingTranscriber + deepgramStream (WebSocket client)
    streaming_test.go    # Mock WebSocket tests

pkg/voice/elevenlabs_tts/
    types.go             # TTSConfig, AudioStream interface
    tts.go               # ElevenLabsTTS provider (streaming HTTP)
    tts_test.go          # Mock HTTP tests

pkg/channels/livekit/
    config.go            # LiveKitConfig, LiveKitAgentConfig, LiveKitTTSConfig
    init.go              # Factory registration
    channel.go           # LiveKitChannel (embeds BaseChannel)
    channel_test.go      # Channel unit tests
    room_session.go      # RoomSession (per-room lifecycle)
    room_session_test.go # RoomSession unit tests
    audio_pipeline.go    # AudioPipeline (per-participant STT→Agent→TTS)
    audio_pipeline_test.go # Pipeline + interruption tests
```

### Modified Files

```
pkg/config/config.go       # Add LiveKitConfig struct + field in ChannelsConfig
pkg/config/security.go     # Add LiveKitSecurity struct + field in ChannelsSecurity
pkg/config/config.go       # Wire security in applySecurityConfig()
pkg/gateway/gateway.go     # Add blank import for livekit channel
go.mod / go.sum            # New dependencies
```

---

## Task 1: Deepgram Streaming STT - Types

**Files:**
- Create: `pkg/voice/deepgram/types.go`

- [ ] **Step 1: Create types file**

```go
package deepgram

import "context"

// StreamOpts configures a Deepgram streaming session.
type StreamOpts struct {
	SampleRate  int    // e.g., 48000
	Channels    int    // e.g., 1 (mono)
	Encoding    string // e.g., "linear16"
	Endpointing int    // silence ms before speech end, e.g., 300
	Language    string // e.g., "en"
}

// TranscriptEvent represents a transcription result from Deepgram.
type TranscriptEvent struct {
	Text        string // transcribed text
	IsFinal     bool   // final vs interim result
	SpeechEnd   bool   // endpointing detected (user stopped talking)
	SpeechStart bool   // user started speaking (interim with text after silence)
}

// TranscriptionStream is the interface for an active Deepgram streaming session.
type TranscriptionStream interface {
	SendAudio(pcm []byte) error
	Results() <-chan TranscriptEvent
	Close() error
}

// StreamingTranscriber opens persistent streaming connections to Deepgram.
type StreamingTranscriber interface {
	OpenStream(ctx context.Context, opts StreamOpts) (TranscriptionStream, error)
}

// deepgramResponse maps to Deepgram's WebSocket JSON response.
type deepgramResponse struct {
	Type    string `json:"type"`
	Channel struct {
		Alternatives []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
		} `json:"alternatives"`
	} `json:"channel"`
	IsFinal     bool    `json:"is_final"`
	SpeechFinal bool    `json:"speech_final"`
	Start       float64 `json:"start"`
	Duration    float64 `json:"duration"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd d:/picoclaw && go build ./pkg/voice/deepgram/`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add pkg/voice/deepgram/types.go
git commit -m "feat(voice): add Deepgram streaming STT types"
```

---

## Task 2: Deepgram Streaming STT - Implementation

**Files:**
- Create: `pkg/voice/deepgram/streaming.go`
- Create: `pkg/voice/deepgram/streaming_test.go`

- [ ] **Step 1: Write the failing test**

```go
package deepgram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDeepgramStream_ReceivesTranscript(t *testing.T) {
	// Mock Deepgram WebSocket server
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Read one audio message (discard)
		_, _, _ = conn.ReadMessage()

		// Send a final transcript response
		resp := deepgramResponse{
			Type:    "Results",
			IsFinal: true,
		}
		resp.Channel.Alternatives = []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
		}{{Transcript: "hello world", Confidence: 0.99}}

		data, _ := json.Marshal(resp)
		conn.WriteMessage(websocket.TextMessage, data)

		// Send speech_final
		resp2 := deepgramResponse{
			Type:        "Results",
			IsFinal:     true,
			SpeechFinal: true,
		}
		resp2.Channel.Alternatives = []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
		}{{Transcript: "", Confidence: 0}}
		data2, _ := json.Marshal(resp2)
		conn.WriteMessage(websocket.TextMessage, data2)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	transcriber := NewDeepgramTranscriber("test-key", WithBaseURL(wsURL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := transcriber.OpenStream(ctx, StreamOpts{
		SampleRate:  48000,
		Channels:    1,
		Encoding:    "linear16",
		Endpointing: 300,
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// Send dummy audio
	if err := stream.SendAudio(make([]byte, 640)); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}

	// Should receive transcript event
	select {
	case evt := <-stream.Results():
		if evt.Text != "hello world" {
			t.Fatalf("expected 'hello world', got %q", evt.Text)
		}
		if !evt.IsFinal {
			t.Fatal("expected IsFinal=true")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for transcript")
	}

	// Should receive speech end event
	select {
	case evt := <-stream.Results():
		if !evt.SpeechEnd {
			t.Fatal("expected SpeechEnd=true")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for speech end")
	}
}

func TestDeepgramStream_ContextCancellation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Just hold the connection open
		select {}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	transcriber := NewDeepgramTranscriber("test-key", WithBaseURL(wsURL))

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := transcriber.OpenStream(ctx, StreamOpts{
		SampleRate: 48000, Channels: 1, Encoding: "linear16", Endpointing: 300,
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	cancel()
	time.Sleep(100 * time.Millisecond)

	err = stream.SendAudio(make([]byte, 640))
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd d:/picoclaw && go test ./pkg/voice/deepgram/ -v -run TestDeepgramStream`
Expected: FAIL — `NewDeepgramTranscriber` not defined

- [ ] **Step 3: Write implementation**

```go
package deepgram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

const defaultBaseURL = "wss://api.deepgram.com/v1/listen"

// Option configures a DeepgramTranscriber.
type Option func(*DeepgramTranscriber)

// WithBaseURL overrides the Deepgram WebSocket URL (for testing).
func WithBaseURL(url string) Option {
	return func(d *DeepgramTranscriber) { d.baseURL = url }
}

// DeepgramTranscriber implements StreamingTranscriber.
type DeepgramTranscriber struct {
	apiKey  string
	baseURL string
}

// NewDeepgramTranscriber creates a new Deepgram streaming transcriber.
func NewDeepgramTranscriber(apiKey string, opts ...Option) *DeepgramTranscriber {
	d := &DeepgramTranscriber{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// OpenStream opens a WebSocket connection to Deepgram for streaming STT.
func (d *DeepgramTranscriber) OpenStream(ctx context.Context, opts StreamOpts) (TranscriptionStream, error) {
	u, err := url.Parse(d.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	q := u.Query()
	q.Set("encoding", opts.Encoding)
	q.Set("sample_rate", fmt.Sprintf("%d", opts.SampleRate))
	q.Set("channels", fmt.Sprintf("%d", opts.Channels))
	if opts.Endpointing > 0 {
		q.Set("endpointing", fmt.Sprintf("%d", opts.Endpointing))
	}
	if opts.Language != "" {
		q.Set("language", opts.Language)
	}
	u.RawQuery = q.Encode()

	header := make(map[string][]string)
	if d.apiKey != "" {
		header["Authorization"] = []string{"Token " + d.apiKey}
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("deepgram dial: %w", err)
	}

	s := &deepgramStream{
		conn:    conn,
		results: make(chan TranscriptEvent, 64),
		done:    make(chan struct{}),
		ctx:     ctx,
	}

	go s.readLoop()

	return s, nil
}

// deepgramStream is the concrete TranscriptionStream implementation.
type deepgramStream struct {
	conn    *websocket.Conn
	results chan TranscriptEvent
	done    chan struct{}
	ctx     context.Context
	once    sync.Once
	writeMu sync.Mutex
}

func (s *deepgramStream) SendAudio(pcm []byte) error {
	select {
	case <-s.done:
		return fmt.Errorf("stream closed")
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (s *deepgramStream) Results() <-chan TranscriptEvent {
	return s.results
}

func (s *deepgramStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.writeMu.Lock()
		_ = s.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		s.writeMu.Unlock()
		s.conn.Close()
	})
	return nil
}

func (s *deepgramStream) readLoop() {
	defer close(s.results)
	for {
		select {
		case <-s.done:
			return
		case <-s.ctx.Done():
			return
		default:
		}

		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			return
		}

		var resp deepgramResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}

		if resp.Type != "Results" {
			continue
		}

		var text string
		if len(resp.Channel.Alternatives) > 0 {
			text = resp.Channel.Alternatives[0].Transcript
		}

		// Detect speech start: first non-empty interim/final result signals user is speaking
		speechStart := !resp.IsFinal && text != ""

		evt := TranscriptEvent{
			Text:        text,
			IsFinal:     resp.IsFinal,
			SpeechEnd:   resp.SpeechFinal,
			SpeechStart: speechStart,
		}

		select {
		case s.results <- evt:
		case <-s.done:
			return
		case <-s.ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/voice/deepgram/ -v -run TestDeepgramStream`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/voice/deepgram/
git commit -m "feat(voice): implement Deepgram streaming STT"
```

---

## Task 3: ElevenLabs TTS - Types

**Files:**
- Create: `pkg/voice/elevenlabs_tts/types.go`

- [ ] **Step 1: Create types file**

```go
package elevenlabs_tts

import "context"

// TTSConfig configures the ElevenLabs TTS provider.
type TTSConfig struct {
	APIKey       string
	VoiceID      string // ElevenLabs voice ID
	ModelID      string // e.g., "eleven_turbo_v2_5"
	OutputFormat string // e.g., "pcm_24000"
}

// AudioStream delivers PCM16 audio chunks from TTS.
type AudioStream interface {
	// Read returns the next PCM16 audio chunk. Returns io.EOF when done.
	Read() ([]byte, error)
	// Close cancels and cleans up the stream.
	Close() error
}

// TTSProvider synthesizes text to streaming audio.
type TTSProvider interface {
	Synthesize(ctx context.Context, text string) (AudioStream, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd d:/picoclaw && go build ./pkg/voice/elevenlabs_tts/`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add pkg/voice/elevenlabs_tts/types.go
git commit -m "feat(voice): add ElevenLabs TTS types"
```

---

## Task 4: ElevenLabs TTS - Implementation

**Files:**
- Create: `pkg/voice/elevenlabs_tts/tts.go`
- Create: `pkg/voice/elevenlabs_tts/tts_test.go`

- [ ] **Step 1: Write the failing test**

```go
package elevenlabs_tts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestElevenLabsTTS_Synthesize(t *testing.T) {
	// Mock ElevenLabs streaming endpoint
	pcmData := make([]byte, 4800) // 100ms of 24kHz mono PCM16
	for i := range pcmData {
		pcmData[i] = byte(i % 256)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("xi-api-key") != "test-key" {
			t.Fatal("missing api key header")
		}

		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
		// Stream in two chunks
		w.Write(pcmData[:2400])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		w.Write(pcmData[2400:])
	}))
	defer server.Close()

	tts := NewElevenLabsTTS(TTSConfig{
		APIKey:       "test-key",
		VoiceID:      "test-voice",
		ModelID:      "eleven_turbo_v2_5",
		OutputFormat: "pcm_24000",
	}, WithBaseURL(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := tts.Synthesize(ctx, "hello world")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	defer stream.Close()

	var totalBytes int
	for {
		chunk, err := stream.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		totalBytes += len(chunk)
	}

	if totalBytes != len(pcmData) {
		t.Fatalf("expected %d bytes, got %d", len(pcmData), totalBytes)
	}
}

func TestElevenLabsTTS_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
		// Hold connection open
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer server.Close()

	tts := NewElevenLabsTTS(TTSConfig{
		APIKey: "test-key", VoiceID: "test-voice",
		ModelID: "eleven_turbo_v2_5", OutputFormat: "pcm_24000",
	}, WithBaseURL(server.URL))

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := tts.Synthesize(ctx, "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	cancel()
	time.Sleep(100 * time.Millisecond)

	_, err = stream.Read()
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd d:/picoclaw && go test ./pkg/voice/elevenlabs_tts/ -v -run TestElevenLabsTTS`
Expected: FAIL — `NewElevenLabsTTS` not defined

- [ ] **Step 3: Write implementation**

```go
package elevenlabs_tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const defaultElevenLabsURL = "https://api.elevenlabs.io"

// TTSOption configures an ElevenLabsTTS.
type TTSOption func(*ElevenLabsTTS)

// WithBaseURL overrides the ElevenLabs API URL (for testing).
func WithBaseURL(url string) TTSOption {
	return func(e *ElevenLabsTTS) { e.baseURL = url }
}

// ElevenLabsTTS implements TTSProvider using ElevenLabs streaming API.
type ElevenLabsTTS struct {
	config  TTSConfig
	baseURL string
	client  *http.Client
}

// NewElevenLabsTTS creates a new ElevenLabs TTS provider.
func NewElevenLabsTTS(cfg TTSConfig, opts ...TTSOption) *ElevenLabsTTS {
	e := &ElevenLabsTTS{
		config:  cfg,
		baseURL: defaultElevenLabsURL,
		client:  &http.Client{},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Synthesize sends text to ElevenLabs and returns a streaming AudioStream.
func (e *ElevenLabsTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	url := fmt.Sprintf("%s/v1/text-to-speech/%s/stream", e.baseURL, e.config.VoiceID)

	body := map[string]any{
		"text":     text,
		"model_id": e.config.ModelID,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", e.config.APIKey)
	if e.config.OutputFormat != "" {
		q := req.URL.Query()
		q.Set("output_format", e.config.OutputFormat)
		req.URL.RawQuery = q.Encode()
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("elevenlabs TTS: status %d", resp.StatusCode)
	}

	return &elevenLabsAudioStream{
		body: resp.Body,
		ctx:  ctx,
		buf:  make([]byte, 4096),
	}, nil
}

// elevenLabsAudioStream reads PCM chunks from the HTTP response body.
type elevenLabsAudioStream struct {
	body io.ReadCloser
	ctx  context.Context
	buf  []byte
	once sync.Once
}

func (s *elevenLabsAudioStream) Read() ([]byte, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	default:
	}

	n, err := s.body.Read(s.buf)
	if n > 0 {
		chunk := make([]byte, n)
		copy(chunk, s.buf[:n])
		return chunk, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (s *elevenLabsAudioStream) Close() error {
	var err error
	s.once.Do(func() {
		err = s.body.Close()
	})
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd d:/picoclaw && go test ./pkg/voice/elevenlabs_tts/ -v -run TestElevenLabsTTS`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/voice/elevenlabs_tts/
git commit -m "feat(voice): implement ElevenLabs streaming TTS"
```

---

## Task 5: Config - LiveKit Channel Config Structs

**Files:**
- Modify: `pkg/config/config.go` (add after ChannelsConfig struct, ~line 371)
- Modify: `pkg/config/security.go` (add after ChannelsSecurity struct, ~line 85)

- [ ] **Step 1: Write failing test**

```go
// In a temporary test or existing config_test.go
// Verify LiveKitConfig can be unmarshaled from JSON
func TestLiveKitConfigUnmarshal(t *testing.T) {
	data := `{"enabled": true, "server_url": "wss://test.livekit.cloud", "allow_from": ["user1"]}`
	var cfg LiveKitConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("expected enabled=true")
	}
	if cfg.ServerURL != "wss://test.livekit.cloud" {
		t.Fatalf("expected server_url, got %q", cfg.ServerURL)
	}
}
```

- [ ] **Step 2: Add LiveKitConfig to config.go**

Add after the existing channel config structs (around line 700, after IRCConfig):

```go
// LiveKitTTSConfig configures TTS for LiveKit voice agents.
type LiveKitTTSConfig struct {
	Provider     string `json:"provider"      env:"PICOCLAW_CHANNELS_LIVEKIT_TTS_PROVIDER"`
	VoiceID      string `json:"voice_id"      env:"PICOCLAW_CHANNELS_LIVEKIT_TTS_VOICE_ID"`
	ModelID      string `json:"model_id"      env:"PICOCLAW_CHANNELS_LIVEKIT_TTS_MODEL_ID"`
	OutputFormat string `json:"output_format" env:"PICOCLAW_CHANNELS_LIVEKIT_TTS_OUTPUT_FORMAT"`
}

// LiveKitAgentConfig configures a named LiveKit agent.
type LiveKitAgentConfig struct {
	Name     string `json:"name"`
	Room     string `json:"room"`
	Identity string `json:"identity"`
	AutoJoin bool   `json:"auto_join"`
}

// LiveKitConfig configures the LiveKit voice channel.
type LiveKitConfig struct {
	Enabled   bool                 `json:"enabled"    env:"PICOCLAW_CHANNELS_LIVEKIT_ENABLED"`
	ServerURL string               `json:"server_url" env:"PICOCLAW_CHANNELS_LIVEKIT_SERVER_URL"`
	AllowFrom FlexibleStringSlice  `json:"allow_from" env:"PICOCLAW_CHANNELS_LIVEKIT_ALLOW_FROM"`
	TTS       LiveKitTTSConfig     `json:"tts"`
	Agents    []LiveKitAgentConfig `json:"agents"`

	apiKey         string
	apiSecret      string
	deepgramAPIKey string
	secDirty       bool
}

func (c *LiveKitConfig) APIKey() string         { return c.apiKey }
func (c *LiveKitConfig) APISecret() string       { return c.apiSecret }
func (c *LiveKitConfig) DeepgramAPIKey() string  { return c.deepgramAPIKey }

func (c *LiveKitConfig) SetAPIKey(k string)        { c.apiKey = k; c.secDirty = true }
func (c *LiveKitConfig) SetAPISecret(s string)     { c.apiSecret = s; c.secDirty = true }
func (c *LiveKitConfig) SetDeepgramAPIKey(k string) { c.deepgramAPIKey = k; c.secDirty = true }
```

- [ ] **Step 3: Add LiveKit field to ChannelsConfig**

In `pkg/config/config.go`, add to `ChannelsConfig` struct (around line 370, before the closing brace):

```go
LiveKit    LiveKitConfig    `json:"livekit"    envPrefix:"PICOCLAW_CHANNELS_LIVEKIT_"`
```

- [ ] **Step 4: Add LiveKitSecurity to security.go**

In `pkg/config/security.go`, add the security struct (after existing security structs):

```go
type LiveKitSecurity struct {
	APIKey        string `yaml:"api_key,omitempty"         env:"PICOCLAW_CHANNELS_LIVEKIT_API_KEY"`
	APISecret     string `yaml:"api_secret,omitempty"      env:"PICOCLAW_CHANNELS_LIVEKIT_API_SECRET"`
	DeepgramAPIKey string `yaml:"deepgram_api_key,omitempty" env:"PICOCLAW_CHANNELS_LIVEKIT_DEEPGRAM_API_KEY"`
}
```

Add to `ChannelsSecurity` struct (around line 84):

```go
LiveKit  *LiveKitSecurity  `yaml:"livekit,omitempty"`
```

- [ ] **Step 5: Wire security in applySecurityConfig**

In `pkg/config/config.go`, find `applySecurityConfig` function and add LiveKit wiring (follow the pattern of other channels):

```go
if sec.Channels.LiveKit != nil {
	if sec.Channels.LiveKit.APIKey != "" {
		cfg.Channels.LiveKit.SetAPIKey(sec.Channels.LiveKit.APIKey)
	}
	if sec.Channels.LiveKit.APISecret != "" {
		cfg.Channels.LiveKit.SetAPISecret(sec.Channels.LiveKit.APISecret)
	}
	if sec.Channels.LiveKit.DeepgramAPIKey != "" {
		cfg.Channels.LiveKit.SetDeepgramAPIKey(sec.Channels.LiveKit.DeepgramAPIKey)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `cd d:/picoclaw && go test ./pkg/config/ -v -run TestLiveKit`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add pkg/config/config.go pkg/config/security.go
git commit -m "feat(config): add LiveKit channel configuration"
```

---

## Task 6: LiveKit Channel - Factory Registration + Skeleton

**Files:**
- Create: `pkg/channels/livekit/config.go`
- Create: `pkg/channels/livekit/init.go`
- Create: `pkg/channels/livekit/channel.go`
- Modify: `pkg/gateway/gateway.go` (add blank import, line ~22)

- [ ] **Step 1: Create config.go** (re-exports from pkg/config for convenience)

```go
package livekit

import "github.com/sipeed/picoclaw/pkg/config"

type Config = config.LiveKitConfig
```

- [ ] **Step 2: Create channel.go skeleton**

```go
package livekit

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// LiveKitChannel implements channels.Channel for LiveKit voice rooms.
type LiveKitChannel struct {
	*channels.BaseChannel
	cfg      config.LiveKitConfig
	bus      *bus.MessageBus
	sessions map[string]*RoomSession // keyed by room name
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewLiveKitChannel creates a new LiveKit voice channel.
func NewLiveKitChannel(cfg *config.Config, b *bus.MessageBus) (*LiveKitChannel, error) {
	lkCfg := cfg.Channels.LiveKit
	if !lkCfg.Enabled {
		return nil, fmt.Errorf("livekit channel is disabled")
	}

	base := channels.NewBaseChannel("livekit", lkCfg, b, lkCfg.AllowFrom)

	return &LiveKitChannel{
		BaseChannel: base,
		cfg:         lkCfg,
		bus:         b,
		sessions:    make(map[string]*RoomSession),
	}, nil
}

func (c *LiveKitChannel) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	for _, agentCfg := range c.cfg.Agents {
		if !agentCfg.AutoJoin {
			continue
		}
		session, err := NewRoomSession(c, agentCfg)
		if err != nil {
			return fmt.Errorf("create room session %q: %w", agentCfg.Room, err)
		}
		c.mu.Lock()
		c.sessions[agentCfg.Room] = session
		c.mu.Unlock()

		if err := session.Join(c.ctx); err != nil {
			return fmt.Errorf("join room %q: %w", agentCfg.Room, err)
		}
	}

	c.SetRunning(true)
	return nil
}

func (c *LiveKitChannel) Stop(ctx context.Context) error {
	c.mu.RLock()
	sessions := make([]*RoomSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.mu.RUnlock()

	for _, s := range sessions {
		s.Leave()
	}

	if c.cancel != nil {
		c.cancel()
	}
	c.SetRunning(false)
	return nil
}

func (c *LiveKitChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	// Parse ChatID format: "<room>:<participant_identity>"
	room, identity, err := parseChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("parse chat ID: %w", err)
	}

	c.mu.RLock()
	session, ok := c.sessions[room]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no session for room %q", room)
	}

	return session.SendTTS(ctx, identity, msg.Content)
}

func parseChatID(chatID string) (room, identity string, err error) {
	parts := splitChatID(chatID)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid livekit chatID format %q, expected room:identity", chatID)
	}
	return parts[0], parts[1], nil
}

func splitChatID(chatID string) []string {
	for i, c := range chatID {
		if c == ':' {
			return []string{chatID[:i], chatID[i+1:]}
		}
	}
	return []string{chatID}
}
```

- [ ] **Step 3: Create init.go**

```go
package livekit

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory("livekit", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewLiveKitChannel(cfg, b)
	})
}
```

- [ ] **Step 4: Add blank import to gateway.go**

In `pkg/gateway/gateway.go`, add after line 21 (between `line` and `maixcam` alphabetically):

```go
_ "github.com/sipeed/picoclaw/pkg/channels/livekit"
```

- [ ] **Step 5: Create stub RoomSession** (to compile)

```go
package livekit

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/config"
)

// RoomSession manages one agent in one LiveKit room.
type RoomSession struct {
	channel  *LiveKitChannel
	agentCfg config.LiveKitAgentConfig
}

// NewRoomSession creates a new room session.
func NewRoomSession(ch *LiveKitChannel, agentCfg config.LiveKitAgentConfig) (*RoomSession, error) {
	return &RoomSession{
		channel:  ch,
		agentCfg: agentCfg,
	}, nil
}

// Join connects to the LiveKit room.
func (rs *RoomSession) Join(ctx context.Context) error {
	// TODO: implement LiveKit room join
	return nil
}

// Leave disconnects from the LiveKit room.
func (rs *RoomSession) Leave() {
	// TODO: implement LiveKit room leave
}

// SendTTS sends text to TTS and plays audio for a participant.
func (rs *RoomSession) SendTTS(ctx context.Context, identity string, text string) error {
	// TODO: implement TTS pipeline
	return nil
}
```

- [ ] **Step 6: Verify it compiles**

Run: `cd d:/picoclaw && go build ./pkg/channels/livekit/`
Expected: No errors

- [ ] **Step 7: Write channel test**

Create `pkg/channels/livekit/channel_test.go`:

```go
package livekit

import "testing"

func TestParseChatID(t *testing.T) {
	tests := []struct {
		input    string
		room     string
		identity string
		wantErr  bool
	}{
		{"my-room:user-123", "my-room", "user-123", false},
		{"room:identity:extra", "room", "identity:extra", false},
		{"invalid", "", "", true},
	}

	for _, tt := range tests {
		room, identity, err := parseChatID(tt.input)
		if (err != nil) != tt.wantErr {
			t.Fatalf("parseChatID(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
		}
		if !tt.wantErr {
			if room != tt.room {
				t.Fatalf("parseChatID(%q) room=%q, want %q", tt.input, room, tt.room)
			}
			if identity != tt.identity {
				t.Fatalf("parseChatID(%q) identity=%q, want %q", tt.input, identity, tt.identity)
			}
		}
	}
}
```

- [ ] **Step 8: Run tests**

Run: `cd d:/picoclaw && go test ./pkg/channels/livekit/ -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add pkg/channels/livekit/ pkg/gateway/gateway.go
git commit -m "feat(channels): add LiveKit channel skeleton with factory registration"
```

---

## Task 7: Add LiveKit SDK Dependencies

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add LiveKit SDK**

Run:
```bash
cd d:/picoclaw
go get github.com/livekit/server-sdk-go/v2@latest
go get github.com/livekit/protocol@latest
go mod tidy
```

- [ ] **Step 2: Verify dependencies resolve**

Run: `cd d:/picoclaw && go build ./...`
Expected: No errors (or only unrelated warnings)

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add LiveKit server-sdk-go and protocol"
```

---

## Task 8: RoomSession - LiveKit Room Join + Participant Tracking

**Files:**
- Modify: `pkg/channels/livekit/room_session.go`
- Create: `pkg/channels/livekit/room_session_test.go`

- [ ] **Step 1: Write failing test for participant tracking**

```go
package livekit

import (
	"testing"
)

func TestRoomSession_ParticipantTracking(t *testing.T) {
	rs := &RoomSession{
		participants: make(map[string]*ParticipantState),
	}

	// Add participant
	ps := rs.getOrCreateParticipant("test-room", "user-1")
	if ps == nil {
		t.Fatal("expected non-nil ParticipantState")
	}
	if ps.sessionKey != "livekit:test-room:user-1" {
		t.Fatalf("unexpected session key: %s", ps.sessionKey)
	}

	// Same participant returns same state
	ps2 := rs.getOrCreateParticipant("test-room", "user-1")
	if ps != ps2 {
		t.Fatal("expected same ParticipantState instance")
	}

	// Different participant
	ps3 := rs.getOrCreateParticipant("test-room", "user-2")
	if ps3.sessionKey != "livekit:test-room:user-2" {
		t.Fatalf("unexpected session key: %s", ps3.sessionKey)
	}

	// Remove participant
	rs.removeParticipant("user-1")
	if _, ok := rs.participants["user-1"]; ok {
		t.Fatal("participant should be removed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd d:/picoclaw && go test ./pkg/channels/livekit/ -v -run TestRoomSession_Participant`
Expected: FAIL

- [ ] **Step 3: Implement full RoomSession**

Replace the stub `room_session.go` with:

```go
package livekit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/config"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/protocol/auth"
	"github.com/pion/webrtc/v4"
)

// ParticipantState tracks per-participant voice session state.
type ParticipantState struct {
	identity       string
	sessionKey     string
	ttsCancel      context.CancelFunc
	speaking       atomic.Bool
	deepgramStream deepgram.TranscriptionStream
	mu             sync.Mutex // protects deepgramStream and ttsCancel
}

// RoomSession manages one agent in one LiveKit room.
type RoomSession struct {
	channel      *LiveKitChannel
	agentCfg     config.LiveKitAgentConfig
	room         *lksdk.Room
	participants map[string]*ParticipantState
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewRoomSession creates a new room session.
func NewRoomSession(ch *LiveKitChannel, agentCfg config.LiveKitAgentConfig) (*RoomSession, error) {
	return &RoomSession{
		channel:      ch,
		agentCfg:     agentCfg,
		participants: make(map[string]*ParticipantState),
	}, nil
}

// Join connects to the LiveKit room as a participant.
func (rs *RoomSession) Join(ctx context.Context) error {
	rs.ctx, rs.cancel = context.WithCancel(ctx)

	// Generate access token
	token, err := rs.generateToken()
	if err != nil {
		return fmt.Errorf("generate token: %w", err)
	}

	// Create room callbacks
	roomCB := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: rs.onTrackSubscribed,
		},
		OnParticipantConnected:    rs.onParticipantConnected,
		OnParticipantDisconnected: rs.onParticipantDisconnected,
	}

	// Connect to room
	room, err := lksdk.ConnectToRoom(rs.channel.cfg.ServerURL, token, roomCB,
		lksdk.WithAutoSubscribe(true),
	)
	if err != nil {
		return fmt.Errorf("connect to room %q: %w", rs.agentCfg.Room, err)
	}

	rs.room = room
	return nil
}

// Leave disconnects from the LiveKit room.
func (rs *RoomSession) Leave() {
	if rs.cancel != nil {
		rs.cancel()
	}
	if rs.room != nil {
		rs.room.Disconnect()
	}
}

// SendTTS routes agent text to the TTS pipeline for a specific participant.
func (rs *RoomSession) SendTTS(ctx context.Context, identity string, text string) error {
	rs.mu.RLock()
	ps, ok := rs.participants[identity]
	rs.mu.RUnlock()
	if !ok {
		return fmt.Errorf("participant %q not found in room %q", identity, rs.agentCfg.Room)
	}

	// Cancel any in-progress TTS for this participant
	if ps.ttsCancel != nil {
		ps.ttsCancel()
	}

	// TODO: pipe text through sentence splitter → ElevenLabs TTS → audio track
	_ = ps
	return nil
}

func (rs *RoomSession) generateToken() (string, error) {
	at := auth.NewAccessToken(rs.channel.cfg.APIKey(), rs.channel.cfg.APISecret())
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     rs.agentCfg.Room,
	}
	at.SetVideoGrant(grant).
		SetIdentity(rs.agentCfg.Identity).
		SetName(rs.agentCfg.Name)

	return at.ToJWT()
}

func (rs *RoomSession) getOrCreateParticipant(room, identity string) *ParticipantState {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if ps, ok := rs.participants[identity]; ok {
		return ps
	}

	ps := &ParticipantState{
		identity:   identity,
		sessionKey: fmt.Sprintf("livekit:%s:%s", room, identity),
	}
	rs.participants[identity] = ps
	return ps
}

func (rs *RoomSession) removeParticipant(identity string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if ps, ok := rs.participants[identity]; ok {
		ps.mu.Lock()
		if ps.ttsCancel != nil {
			ps.ttsCancel()
		}
		if ps.deepgramStream != nil {
			ps.deepgramStream.Close()
		}
		ps.mu.Unlock()
		delete(rs.participants, identity)
	}
}

func (rs *RoomSession) onParticipantConnected(rp *lksdk.RemoteParticipant) {
	rs.getOrCreateParticipant(rs.agentCfg.Room, rp.Identity())
}

func (rs *RoomSession) onParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	rs.removeParticipant(rp.Identity())
}

func (rs *RoomSession) onTrackSubscribed(
	track *webrtc.TrackRemote,
	pub *lksdk.RemoteTrackPublication,
	rp *lksdk.RemoteParticipant,
) {
	// Only handle audio tracks
	if pub.Kind() != lksdk.TrackKindAudio {
		return
	}

	ps := rs.getOrCreateParticipant(rs.agentCfg.Room, rp.Identity())

	// TODO: start audio pipeline goroutine for this participant
	_ = ps
	_ = pub
}
```

- [ ] **Step 4: Run tests**

Run: `cd d:/picoclaw && go test ./pkg/channels/livekit/ -v -run TestRoomSession`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/channels/livekit/room_session.go pkg/channels/livekit/room_session_test.go
git commit -m "feat(livekit): implement RoomSession with participant tracking"
```

---

## Task 9: Audio Pipeline - STT→Agent→TTS Coordinator

**Files:**
- Create: `pkg/channels/livekit/audio_pipeline.go`
- Create: `pkg/channels/livekit/audio_pipeline_test.go`

- [ ] **Step 1: Write failing test for sentence splitter**

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
		{"No punctuation", nil}, // no complete sentence
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

Run: `cd d:/picoclaw && go test ./pkg/channels/livekit/ -v -run TestSentenceSplitter`
Expected: FAIL

- [ ] **Step 3: Implement audio pipeline**

```go
package livekit

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// sentenceSplitter accumulates text and emits complete sentences.
type sentenceSplitter struct {
	buf strings.Builder
}

func newSentenceSplitter() *sentenceSplitter {
	return &sentenceSplitter{}
}

// Feed adds a rune and returns a complete sentence if a boundary is found.
func (s *sentenceSplitter) Feed(r rune) string {
	s.buf.WriteRune(r)
	if r == '.' || r == '!' || r == '?' {
		sentence := s.buf.String()
		s.buf.Reset()
		return sentence
	}
	return ""
}

// Flush returns any remaining buffered text.
func (s *sentenceSplitter) Flush() string {
	remaining := s.buf.String()
	s.buf.Reset()
	return remaining
}

// AudioPipeline coordinates the STT→Agent→TTS flow for one participant.
type AudioPipeline struct {
	session     *RoomSession
	participant *ParticipantState
	bus         *bus.MessageBus
}

// NewAudioPipeline creates a pipeline for a participant.
func NewAudioPipeline(session *RoomSession, ps *ParticipantState, b *bus.MessageBus) *AudioPipeline {
	return &AudioPipeline{
		session:     session,
		participant: ps,
		bus:         b,
	}
}

// PublishTranscript sends transcribed text to the agent via the message bus.
func (ap *AudioPipeline) PublishTranscript(ctx context.Context, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}

	chatID := fmt.Sprintf("%s:%s", ap.session.agentCfg.Room, ap.participant.identity)
	peer := bus.Peer{
		Kind: "direct",
		ID:   ap.participant.identity,
	}
	sender := bus.SenderInfo{
		Platform:    "livekit",
		PlatformID:  ap.participant.identity,
		CanonicalID: fmt.Sprintf("livekit:%s", ap.participant.identity),
		DisplayName: ap.participant.identity,
	}

	ap.session.channel.HandleMessage(
		ctx,
		peer,
		"",    // messageID
		ap.participant.identity,
		chatID,
		text,
		nil,   // media
		nil,   // metadata
		sender,
	)
}
```

- [ ] **Step 4: Run tests**

Run: `cd d:/picoclaw && go test ./pkg/channels/livekit/ -v -run TestSentenceSplitter`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/channels/livekit/audio_pipeline.go pkg/channels/livekit/audio_pipeline_test.go
git commit -m "feat(livekit): implement audio pipeline with sentence splitting"
```

---

## Task 10: Wire Audio Pipeline into RoomSession

**Files:**
- Modify: `pkg/channels/livekit/room_session.go`
- Modify: `pkg/channels/livekit/audio_pipeline.go`

- [ ] **Step 1: Wire Deepgram STT into onTrackSubscribed**

Update `onTrackSubscribed` in `room_session.go` to start the audio pipeline:

```go
func (rs *RoomSession) onTrackSubscribed(
	track *livekit.TrackInfo,
	pub *lksdk.RemoteTrackPublication,
	rp *lksdk.RemoteParticipant,
) {
	if track.Type != livekit.TrackType_AUDIO {
		return
	}

	ps := rs.getOrCreateParticipant(rs.agentCfg.Room, rp.Identity())
	pipeline := NewAudioPipeline(rs, ps, rs.channel.bus)

	go pipeline.RunInbound(rs.ctx, pub)
}
```

- [ ] **Step 2: Implement RunInbound in audio_pipeline.go**

Add to `audio_pipeline.go`:

```go
// RunInbound reads audio from a LiveKit track, feeds Deepgram, and publishes transcripts.
func (ap *AudioPipeline) RunInbound(ctx context.Context, pub *lksdk.RemoteTrackPublication) {
	// Open Deepgram stream
	dgTranscriber := ap.session.channel.deepgramTranscriber
	if dgTranscriber == nil {
		return
	}

	stream, err := dgTranscriber.OpenStream(ctx, deepgram.StreamOpts{
		SampleRate:  48000,
		Channels:    1,
		Encoding:    "linear16",
		Endpointing: 300,
	})
	if err != nil {
		return
	}
	defer stream.Close()

	// Read transcription results in background
	go ap.readTranscripts(ctx, stream)

	// Read audio from LiveKit track and feed to Deepgram
	// TODO: use pub to get PCM reader when LiveKit SDK is fully wired
}

func (ap *AudioPipeline) readTranscripts(ctx context.Context, stream deepgram.TranscriptionStream) {
	var utterance strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-stream.Results():
			if !ok {
				return
			}

			// Speech start: immediately cancel TTS (barge-in interruption)
			if evt.SpeechStart && !ap.participant.speaking.Load() {
				ap.participant.speaking.Store(true)
				ap.participant.mu.Lock()
				if ap.participant.ttsCancel != nil {
					ap.participant.ttsCancel()
				}
				ap.participant.mu.Unlock()
			}

			if evt.IsFinal && evt.Text != "" {
				utterance.WriteString(evt.Text)
				utterance.WriteString(" ")
			}

			if evt.SpeechEnd {
				text := strings.TrimSpace(utterance.String())
				utterance.Reset()
				ap.participant.speaking.Store(false)

				if text != "" {
					ap.PublishTranscript(ctx, text)
				}
			}
		}
	}
}
```

- [ ] **Step 3: Add deepgramTranscriber field to LiveKitChannel**

In `channel.go`, add to the struct and constructor:

```go
// In LiveKitChannel struct:
deepgramTranscriber *deepgram.DeepgramTranscriber

// In NewLiveKitChannel, after creating base:
var dgTranscriber *deepgram.DeepgramTranscriber
if lkCfg.DeepgramAPIKey() != "" {
	dgTranscriber = deepgram.NewDeepgramTranscriber(lkCfg.DeepgramAPIKey())
}
```

- [ ] **Step 4: Wire TTS into SendTTS**

Update `SendTTS` in `room_session.go`:

```go
func (rs *RoomSession) SendTTS(ctx context.Context, identity string, text string) error {
	rs.mu.RLock()
	ps, ok := rs.participants[identity]
	rs.mu.RUnlock()
	if !ok {
		return fmt.Errorf("participant %q not found", identity)
	}

	// Cancel any in-progress TTS
	if ps.ttsCancel != nil {
		ps.ttsCancel()
	}

	ttsCtx, ttsCancel := context.WithCancel(ctx)
	ps.ttsCancel = ttsCancel

	tts := rs.channel.ttsProvider
	if tts == nil {
		return fmt.Errorf("no TTS provider configured")
	}

	// Sentence-chunk and synthesize
	go func() {
		defer ttsCancel()
		splitter := newSentenceSplitter()
		for _, r := range text {
			if sentence := splitter.Feed(r); sentence != "" {
				if err := rs.synthesizeAndPlay(ttsCtx, sentence); err != nil {
					return
				}
			}
		}
		if remainder := splitter.Flush(); remainder != "" {
			rs.synthesizeAndPlay(ttsCtx, remainder)
		}
	}()

	return nil
}

func (rs *RoomSession) synthesizeAndPlay(ctx context.Context, text string) error {
	tts := rs.channel.ttsProvider
	stream, err := tts.Synthesize(ctx, text)
	if err != nil {
		return err
	}
	defer stream.Close()

	// Read PCM chunks from stream and write to LiveKit local audio track
	// Full track wiring completed in Task 12 after spike validation
	for {
		chunk, err := stream.Read()
		if err == io.EOF {
			return nil // finished normally
		}
		if err != nil {
			return fmt.Errorf("TTS read: %w", err)
		}
		// TODO (Task 12): write chunk to local PCM track
		_ = chunk
	}
}
```

- [ ] **Step 5: Add ttsProvider field to LiveKitChannel**

In `channel.go`, add:

```go
// In LiveKitChannel struct:
ttsProvider *elevenlabs_tts.ElevenLabsTTS

// In NewLiveKitChannel:
var ttsProvider *elevenlabs_tts.ElevenLabsTTS
if lkCfg.TTS.VoiceID != "" {
	ttsProvider = elevenlabs_tts.NewElevenLabsTTS(elevenlabs_tts.TTSConfig{
		APIKey:       cfg.Voice.ElevenLabsAPIKey,
		VoiceID:      lkCfg.TTS.VoiceID,
		ModelID:      lkCfg.TTS.ModelID,
		OutputFormat: lkCfg.TTS.OutputFormat,
	})
}
```

- [ ] **Step 6: Verify it compiles**

Run: `cd d:/picoclaw && go build ./pkg/channels/livekit/`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add pkg/channels/livekit/
git commit -m "feat(livekit): wire Deepgram STT and ElevenLabs TTS into audio pipeline"
```

---

## Task 11: Integration Validation Spike

This task validates the LiveKit PCM audio round-trip before fully wiring it.

**Files:**
- Create: `pkg/channels/livekit/spike_test.go` (build-tagged, only runs manually)

- [ ] **Step 1: Write integration test (manual only)**

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
)

// Run with: go test ./pkg/channels/livekit/ -tags livekit_spike -run TestLiveKitSpike -v
// Requires: LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET env vars
// And a running LiveKit server
func TestLiveKitSpike(t *testing.T) {
	url := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")
	if url == "" || apiKey == "" || apiSecret == "" {
		t.Skip("LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET required")
	}

	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{RoomJoin: true, Room: "test-spike"}
	at.SetVideoGrant(grant).SetIdentity("picoclaw-spike")
	token, err := at.ToJWT()
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	room, err := lksdk.ConnectToRoom(url, token, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				fmt.Printf("Track subscribed: %s from %s\n", pub.Name(), rp.Identity())
			},
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer room.Disconnect()

	fmt.Printf("Connected to room: %s as %s\n", room.Name(), room.LocalParticipant.Identity())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	<-ctx.Done()
}
```

- [ ] **Step 2: Document findings**

After running the spike with a real LiveKit server, document:
- Whether `ConnectToRoom` works for joining as a participant
- Whether `OnTrackSubscribed` fires for audio tracks
- Whether PCM local track publishing works
- Any API changes needed

- [ ] **Step 3: Commit**

```bash
git add pkg/channels/livekit/spike_test.go
git commit -m "test(livekit): add integration validation spike"
```

---

## Task 12: Final Wiring - PCM Audio Track I/O

**Files:**
- Modify: `pkg/channels/livekit/audio_pipeline.go`
- Modify: `pkg/channels/livekit/room_session.go`

This task depends on spike results from Task 11. The implementation will use the LiveKit SDK's PCM track APIs:

- [ ] **Step 1: Wire inbound PCM reader**

In `RunInbound`, add PCM reading from the LiveKit track using the SDK's track reader API. The exact implementation depends on spike findings.

- [ ] **Step 2: Wire outbound PCM writer**

In `synthesizeAndPlay`, write PCM chunks from TTS to a `lksdk.LocalSampleTrack` published to the room.

- [ ] **Step 3: Add audio track to RoomSession**

Create a local audio track in `Join()` and publish it. This track is shared for TTS output.

- [ ] **Step 4: End-to-end test**

Manual test with LiveKit Playground:
1. Start PicoClaw gateway with LiveKit channel enabled
2. Open LiveKit Playground, join the configured room
3. Speak — verify transcription arrives at agent
4. Verify agent response is spoken back via TTS
5. Test interruption — speak while agent is responding

- [ ] **Step 5: Commit**

```bash
git add pkg/channels/livekit/
git commit -m "feat(livekit): wire PCM audio track I/O for full voice pipeline"
```

---

## Summary

| Task | Component | Estimated Complexity |
|------|-----------|---------------------|
| 1 | Deepgram STT types | Low |
| 2 | Deepgram STT implementation | Medium |
| 3 | ElevenLabs TTS types | Low |
| 4 | ElevenLabs TTS implementation | Medium |
| 5 | Config structs | Low |
| 6 | Channel skeleton + factory | Medium |
| 7 | LiveKit SDK dependencies | Low |
| 8 | RoomSession + participant tracking | Medium |
| 9 | Audio pipeline + sentence splitter | Medium |
| 10 | Wire STT→Agent→TTS | High |
| 11 | Integration spike | Medium |
| 12 | Final PCM audio wiring | High |
