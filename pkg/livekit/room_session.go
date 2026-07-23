package livekit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/media-sdk"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/pion/webrtc/v4"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

// RoomSession manages one agent in one LiveKit room (one job).
type RoomSession struct {
	worker         *Worker
	jobID          string
	roomInfo       *livekit.Room
	bridge         *AgentBridge
	room           *lksdk.Room
	localTrack     *lkmedia.PCMLocalTrack
	localTrackSID  string
	participant    *ParticipantState
	activePipeline *AudioPipeline
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc

	serverURL           string
	token               string
	stt                 stt.Provider
	tts                 tts.Provider
	apiKey              string
	apiSecret           string
	agentName           string
	sampleRate          int
	fillerWords         []string
	primaryLanguage     string // from room metadata, used for language-aware fallbacks
	characterName       string // active character (e.g. "Bheem") for name-aware greeting fallback
	SessionLanguageName string
	SessionLanguageCode string
	sessionLanguageName string
	sessionLanguageCode string
	runtime             config.LiveKitServiceRuntimeConfig
	managerAPIURL       string
	managerAPISecret    string
	deviceMAC           string
	agentID             string
	closeOnce           sync.Once

	languageUpdateMu          sync.Mutex
	lastLanguageUpdateKey     string
	lastLanguageUpdateAt      time.Time
	languageReconnectInFlight bool
}

// ParticipantState tracks per-participant voice session state.
type ParticipantState struct {
	identity   string
	sessionKey string
	sttStream  stt.TranscriptionStream
	pcmTrack   *lkmedia.PCMRemoteTrack
	ttsCancel  context.CancelFunc
	// ttsCancelReason records why the current TTS context was cancelled so
	// synthesize/read errors can distinguish barge-in from provider failures.
	ttsCancelReason string
	speaking        atomic.Bool
	mu              sync.Mutex
}

// RoomSessionConfig configures a RoomSession.
type RoomSessionConfig struct {
	Worker              *Worker
	JobID               string
	RoomInfo            *livekit.Room
	Bridge              *AgentBridge
	ServerURL           string
	Token               string
	STT                 stt.Provider
	TTS                 tts.Provider
	APIKey              string
	APISecret           string
	AgentName           string
	SampleRate          int
	FillerWords         []string
	PrimaryLanguage     string // e.g. "Hindi", "English" — from room metadata
	CharacterName       string // e.g. "Bheem" — from room metadata, for name-aware greeting fallback
	SessionLanguageName string
	SessionLanguageCode string
	Runtime             config.LiveKitServiceRuntimeConfig
}

// NewRoomSession creates a new room session for a job.
func NewRoomSession(cfg RoomSessionConfig) (*RoomSession, error) {
	if cfg.RoomInfo == nil {
		return nil, errors.New("room info is nil")
	}
	if cfg.ServerURL == "" {
		return nil, errors.New("server url is empty")
	}
	managerAPIURL := strings.TrimSpace(os.Getenv("MANAGER_API_URL"))
	if managerAPIURL == "" {
		managerAPIURL = defaultManagerAPIURL
	}
	managerAPISecret := managerAPIServiceKeyFromEnv()
	deviceMAC, agentID := resolvePersistenceFields(cfg.RoomInfo.Name, cfg.RoomInfo.Metadata)
	policy := NormalizeSessionLanguagePolicy(cfg.SessionLanguageName, cfg.SessionLanguageCode)
	if strings.TrimSpace(cfg.SessionLanguageName) == "" && strings.TrimSpace(cfg.SessionLanguageCode) == "" {
		policy = NormalizeSessionLanguagePolicy(cfg.PrimaryLanguage, "")
	}

	return &RoomSession{
		worker:              cfg.Worker,
		jobID:               cfg.JobID,
		roomInfo:            cfg.RoomInfo,
		bridge:              cfg.Bridge,
		serverURL:           cfg.ServerURL,
		token:               cfg.Token,
		stt:                 cfg.STT,
		tts:                 cfg.TTS,
		apiKey:              cfg.APIKey,
		apiSecret:           cfg.APISecret,
		agentName:           cfg.AgentName,
		sampleRate:          cfg.SampleRate,
		fillerWords:         cfg.FillerWords,
		primaryLanguage:     policy.DisplayName,
		characterName:       cfg.CharacterName,
		sessionLanguageName: policy.DisplayName,
		sessionLanguageCode: policy.RawCode,
		runtime:             cfg.Runtime,
		managerAPIURL:       managerAPIURL,
		managerAPISecret:    managerAPISecret,
		deviceMAC:           deviceMAC,
		agentID:             agentID,
	}, nil
}

func managerAPIServiceKeyFromEnv() string {
	for _, key := range []string{
		"MANAGER_API_SECRET",
		"SERVICE_SECRET_KEY",
		"PICOCLAW_LIVEKIT_MANAGER_API_SERVICE_KEY",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// Join connects to the LiveKit room.
func (rs *RoomSession) Join(ctx context.Context) error {
	if rs == nil {
		return errors.New("room session is nil")
	}
	rs.ctx, rs.cancel = context.WithCancel(ctx)

	// Register the canonical full teardown so out-of-band signals (e.g. the
	// distributed workspace lock being preempted by a newer session) can end this
	// session gracefully: persist chat history, disconnect the room, close the
	// bridge. Leave() is idempotent (guarded by closeOnce).
	if rs.bridge != nil {
		rs.bridge.SetTeardownHook(rs.Leave)
	}

	cb := lksdk.NewRoomCallback()
	cb.OnParticipantDisconnected = func(rp *lksdk.RemoteParticipant) {
		rs.handleParticipantDisconnected(rp)
	}
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		rs.handleTrackSubscribed(track, rp)
	}
	cb.OnDisconnected = func() {
		logger.InfoCF("livekit", "Room disconnected callback triggered", map[string]any{
			"room": rs.roomInfo.Name,
		})
		rs.Leave()
	}
	cb.OnDataReceived = func(data []byte, params lksdk.DataReceiveParams) {
		rs.handleDataMessage(data)
	}

	var token string
	if rs.apiKey != "" && rs.apiSecret != "" {
		generated, err := rs.generateRoomToken()
		if err != nil {
			return err
		}
		token = generated
	} else {
		token = rs.token
		if token == "" {
			return errors.New("room token is empty and no api key/secret available")
		}
	}

	room, err := lksdk.ConnectToRoomWithToken(rs.serverURL, token, cb)
	if err != nil {
		return err
	}
	rs.room = room

	// ── Duplicate agent guard ──────────────────────────────────────────────────
	// If another agent (AGENT kind participant) is already in the room, this job
	// is a duplicate dispatch — bail out cleanly so there's only ever one agent.
	for _, rp := range room.GetRemoteParticipants() {
		if rp.Kind() == lksdk.ParticipantAgent {
			logger.WarnCF("livekit", "Duplicate agent detected — leaving room", map[string]any{
				"existing_agent": rp.Identity(),
				"room":           rs.roomInfo.Name,
			})
			room.Disconnect()
			return errors.New("duplicate agent already in room")
		}
	}

	logger.InfoCF("livekit", "Joined room", map[string]any{
		"room":      rs.roomInfo.Name,
		"job_id":    rs.jobID,
		"serverURL": rs.serverURL,
	})

	if rs.sampleRate == 0 {
		rs.sampleRate = 24000
	}
	localTrack, err := lkmedia.NewPCMLocalTrack(rs.sampleRate, 1, protoLogger.GetLogger())
	if err != nil {
		return err
	}
	rs.localTrack = localTrack

	pub, err := room.LocalParticipant.PublishTrack(localTrack, &lksdk.TrackPublicationOptions{
		Name: "picoclaw-tts",
	})
	if err != nil {
		return err
	}
	if pub != nil {
		rs.localTrackSID = pub.SID()
	}
	logger.InfoCF("livekit", "Published local TTS track", map[string]any{
		"room":      rs.roomInfo.Name,
		"job_id":    rs.jobID,
		"track_sid": rs.localTrackSID,
	})

	// Mid-session usage heartbeat + minute-cap cutoff (SUB-5).
	rs.startUsageHeartbeat()

	return nil
}

// PublishAgentState publishes a JSON agent state change to the LiveKit data channel
func (rs *RoomSession) PublishAgentState(oldState, newState string) error {
	if rs.room == nil || rs.room.LocalParticipant == nil {
		return errors.New("room local participant not ready")
	}

	payload := map[string]any{
		"type": "agent_state_changed",
		"data": map[string]string{
			"old_state": oldState,
			"new_state": newState,
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return rs.room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true))
}

// PublishSpeechCreated publishes the generated speech text back to the LiveKit data channel
func (rs *RoomSession) PublishSpeechCreated(text string) error {
	if rs.room == nil || rs.room.LocalParticipant == nil {
		return errors.New("room local participant not ready")
	}

	payload := map[string]any{
		"type": "speech_created",
		"data": map[string]string{
			"text": text,
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return rs.room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true))
}

// Leave disconnects from the room.
func (rs *RoomSession) Leave() {
	if rs == nil {
		return
	}
	rs.closeOnce.Do(func() {
		rs.leave()
	})
}

func (rs *RoomSession) leave() {
	rs.mu.Lock()
	if rs.cancel != nil {
		rs.cancel()
		rs.cancel = nil
	}
	participant := rs.participant
	rs.participant = nil
	rs.activePipeline = nil
	localTrack := rs.localTrack
	rs.localTrack = nil
	room := rs.room
	rs.room = nil
	bridge := rs.bridge
	rs.bridge = nil
	rs.mu.Unlock()

	if rs.worker != nil && strings.TrimSpace(rs.jobID) != "" && rs.worker.removeJob(rs.jobID, rs) {
		logger.InfoCF("livekit", "Room session removed from worker", map[string]any{
			"room":   rs.roomName(),
			"job_id": rs.jobID,
		})
	}

	// Persist usage + transcript before bridge/session teardown.
	rs.persistPostSessionData(bridge)

	if participant != nil {
		participant.mu.Lock()
		if participant.sttStream != nil {
			_ = participant.sttStream.Close()
		}
		if participant.pcmTrack != nil {
			participant.pcmTrack.Close()
		}
		if participant.ttsCancel != nil {
			participant.ttsCancel()
		}
		participant.mu.Unlock()
	}

	if localTrack != nil {
		_ = localTrack.Close()
	}
	if room != nil {
		room.Disconnect()
	}
	if bridge != nil {
		bridge.Close()
	}
}

// handleDataMessage processes data channel messages from the MQTT gateway.
func (rs *RoomSession) handleDataMessage(data []byte) {
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	msgType, _ := msg["type"].(string)
	switch msgType {
	case "ready_for_greeting":
		sessionID, _ := msg["session_id"].(string)
		logger.InfoCF("livekit", "Received ready_for_greeting from gateway", map[string]any{
			"room":       rs.roomInfo.Name,
			"session_id": strings.TrimSpace(sessionID),
		})
		// Greeting is triggered by runtime policy when STT+VAD are fully ready on
		// track subscription. This control message is accepted for observability.
		logger.InfoCF("livekit", "Processed ready_for_greeting (greeting controlled by runtime policy)", map[string]any{
			"room":                rs.roomInfo.Name,
			"greeting_mode":       strings.TrimSpace(rs.runtime.GreetingMode),
			"processing_strategy": "runtime_policy_track_subscribe",
		})
	case "end_prompt":
		prompt, _ := msg["prompt"].(string)
		if prompt == "" {
			prompt = "It was so much fun talking with you! Take care and see you next time!"
		}
		logger.InfoCF("livekit", "Received end_prompt from gateway", map[string]any{"room": rs.roomInfo.Name})
		go rs.handleEndPrompt(prompt)
	case "shutdown_request":
		sessionID, _ := msg["session_id"].(string)
		requireAck, _ := msg["require_ack"].(bool)
		logger.InfoCF("livekit", "Received shutdown_request from gateway", map[string]any{"room": rs.roomInfo.Name})
		go rs.handleShutdownRequest(sessionID, requireAck)
	case "abort":
		sessionID, _ := msg["session_id"].(string)
		logger.InfoCF("livekit", "Received abort from gateway", map[string]any{
			"room":       rs.roomInfo.Name,
			"session_id": strings.TrimSpace(sessionID),
		})
		rs.interruptActivePipeline("mqtt_abort")
	case "session_language_update":
		update, ok := parseSessionLanguageUpdate(data)
		if !ok {
			logger.WarnCF("livekit", "Ignoring invalid session_language_update payload", map[string]any{
				"room": rs.roomInfo.Name,
			})
			return
		}
		go rs.handleSessionLanguageUpdate(update)
	}
}

func (rs *RoomSession) interruptActivePipeline(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "external_abort"
	}
	rs.mu.Lock()
	pipeline := rs.activePipeline
	rs.mu.Unlock()
	if pipeline == nil {
		logger.InfoCF("livekit", "Abort received with no active audio pipeline", map[string]any{
			"room":   rs.roomName(),
			"reason": reason,
		})
		return
	}
	pipeline.interruptActiveSpeech(reason)
}

type sessionLanguageUpdate struct {
	Name    string `json:"session_language_name"`
	Code    string `json:"session_language_code"`
	RFIDUID string `json:"rfid_uid"`
}

func parseSessionLanguageUpdate(payload []byte) (sessionLanguageUpdate, bool) {
	var envelope struct {
		Type string `json:"type"`
		sessionLanguageUpdate
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return sessionLanguageUpdate{}, false
	}
	if strings.TrimSpace(envelope.Type) != "session_language_update" {
		return sessionLanguageUpdate{}, false
	}
	update := sessionLanguageUpdate{
		Name:    strings.TrimSpace(envelope.Name),
		Code:    strings.TrimSpace(envelope.Code),
		RFIDUID: strings.TrimSpace(envelope.RFIDUID),
	}
	if update.Name == "" && update.Code == "" {
		return sessionLanguageUpdate{}, false
	}
	return update, true
}

func (rs *RoomSession) handleSessionLanguageUpdate(update sessionLanguageUpdate) {
	policy := NormalizeSessionLanguagePolicy(update.Name, update.Code)
	key := strings.ToLower(strings.TrimSpace(policy.DisplayName) + "|" + strings.TrimSpace(policy.RawCode) + "|" + strings.TrimSpace(update.RFIDUID))

	rs.languageUpdateMu.Lock()
	ignoredDuplicate := key != "" && key == rs.lastLanguageUpdateKey && time.Since(rs.lastLanguageUpdateAt) < 10*time.Second
	if ignoredDuplicate || rs.languageReconnectInFlight {
		rs.languageUpdateMu.Unlock()
		rs.publishSessionLanguageUpdateAck(update, policy, true)
		logger.InfoCF("livekit", "Ignoring duplicate session language update", map[string]any{
			"room":               rs.roomInfo.Name,
			"rfid_uid":           update.RFIDUID,
			"language_name":      policy.DisplayName,
			"language_code":      policy.RawCode,
			"ignored_duplicate":  ignoredDuplicate,
			"reconnect_inflight": true,
		})
		return
	}
	rs.lastLanguageUpdateKey = key
	rs.lastLanguageUpdateAt = time.Now()
	rs.languageReconnectInFlight = true
	rs.sessionLanguageName = policy.DisplayName
	rs.sessionLanguageCode = policy.RawCode
	rs.primaryLanguage = policy.DisplayName
	rs.languageUpdateMu.Unlock()

	if rs.bridge != nil {
		rs.bridge.UpdateSessionLanguage(update.Name, update.Code)
	}
	rs.publishSessionLanguageUpdateAck(update, policy, false)
	logger.InfoCF("livekit", "Applying session language update and triggering graceful reconnect", map[string]any{
		"room":               rs.roomInfo.Name,
		"rfid_uid":           update.RFIDUID,
		"language_name":      policy.DisplayName,
		"language_code":      policy.RawCode,
		"ignored_duplicate":  false,
		"reconnect_inflight": true,
	})
	rs.Leave()
}

func (rs *RoomSession) publishSessionLanguageUpdateAck(update sessionLanguageUpdate, policy SessionLanguagePolicy, ignored bool) {
	if rs == nil || rs.room == nil || rs.room.LocalParticipant == nil {
		return
	}
	ack, _ := json.Marshal(map[string]any{
		"type":                  "session_language_update_ack",
		"session_language_name": policy.DisplayName,
		"session_language_code": policy.RawCode,
		"rfid_uid":              strings.TrimSpace(update.RFIDUID),
		"ignored_duplicate":     ignored,
		"timestamp":             time.Now().UnixMilli(),
		"source":                "picoclaw_agent",
	})
	_ = rs.room.LocalParticipant.PublishData(ack, lksdk.WithDataPublishReliable(true))
}

// handleEndPrompt asks the LLM to generate and speak a farewell message,
// then disconnects. A 10-second deadline prevents hanging on a slow model.
func (rs *RoomSession) handleEndPrompt(prompt string) {
	// An end prompt (gateway end or minute-cap cutoff) can land mid-response;
	// stop the in-flight speech/generation first so the farewell is not
	// sandwiched inside it (live 2026-07-23: story audio streamed over the
	// goodbye and the session died mid-story).
	rs.interruptActivePipeline("end_prompt_farewell")
	if rs.bridge == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build a minimal pipeline just for TTS playback of the goodbye.
	// IMPORTANT: we do NOT use normal ChatStream here because that persists
	// the farewell control prompt into session history/memory.
	pipeline := NewAudioPipeline(rs, rs.bridge, rs.tts, nil)
	farewellText := rs.generateFarewellTextNoPersist(ctx, prompt)
	if strings.TrimSpace(farewellText) == "" {
		farewellText = "It was so much fun talking with you! Take care and see you next time!"
	}
	rs.PublishAgentState("listening", "thinking")
	rs.PublishAgentState("thinking", "speaking")
	pipeline.publishSpeechCreated("")
	pipeline.synthesizeAndPlay(ctx, farewellText)
	pipeline.flushSilenceForContext(ctx, liveKitFinalTransportTailMs)
	rs.PublishAgentState("speaking", "listening")

	// Brief pause so TTS audio finishes flushing before we disconnect.
	time.Sleep(500 * time.Millisecond)
	rs.Leave()
}

func (rs *RoomSession) generateFarewellTextNoPersist(ctx context.Context, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if rs == nil || rs.bridge == nil || rs.bridge.provider == nil {
		return prompt
	}

	messages := []providers.Message{
		{
			Role: "system",
			Content: "Voice farewell mode. Reply with one short child-friendly goodbye only. " +
				"Do not include reasoning, analysis, or tool calls.",
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	resp, err := rs.bridge.callLLM(
		ctx,
		"livekit:system:end_prompt",
		messages,
		nil,
		rs.bridge.optionsForProfile("conversation"),
		nil,
	)
	if err != nil {
		logger.WarnCF("livekit", "Failed to generate farewell via LLM, falling back to prompt text", map[string]any{
			"room":  rs.roomInfo.Name,
			"error": err.Error(),
		})
		return prompt
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return prompt
	}
	return text
}

// handleShutdownRequest sends an ACK back to the gateway (if requested)
// then triggers a clean Leave sequence.
func (rs *RoomSession) handleShutdownRequest(sessionID string, requireAck bool) {
	if requireAck && rs.room != nil && rs.room.LocalParticipant != nil {
		ack, _ := json.Marshal(map[string]any{
			"type":       "shutdown_ack",
			"session_id": sessionID,
			"timestamp":  time.Now().UnixMilli(),
			"source":     "picoclaw_agent",
		})
		_ = rs.room.LocalParticipant.PublishData(ack, lksdk.WithDataPublishReliable(true))
		logger.InfoCF("livekit", "Sent shutdown_ack to gateway", map[string]any{
			"session_id": sessionID,
			"room":       rs.roomInfo.Name,
		})
	}
	rs.Leave()
}

func (rs *RoomSession) handleTrackSubscribed(track *webrtc.TrackRemote, rp *lksdk.RemoteParticipant) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}
	logger.InfoCF("livekit", "Audio track subscribed", map[string]any{
		"room":        rs.roomInfo.Name,
		"participant": rp.Identity(),
	})

	rs.mu.Lock()
	if rs.participant != nil {
		rs.mu.Unlock()
		return
	}

	ps := &ParticipantState{
		identity:   rp.Identity(),
		sessionKey: rs.sessionKeyForParticipant(rp.Identity()),
	}
	rs.participant = ps
	rs.mu.Unlock()

	if rs.stt == nil {
		logger.WarnC("livekit", "STT provider not configured")
		rs.speakSTTUnavailableFallback(errors.New("stt provider not configured"))
		return
	}

	// Get provider capabilities and configure stream options
	caps := rs.stt.Capabilities()

	// Determine model and language
	model := ""
	policy := NormalizeSessionLanguagePolicy(rs.sessionLanguageName, rs.sessionLanguageCode)
	language := ResolveSTTHintWithCapabilities(policy, caps.Languages)
	if strings.TrimSpace(language) == "" {
		language = "auto"
	}
	rs.primaryLanguage = policy.DisplayName
	sttEndpointMS := rs.runtime.VADEndpointMS
	if sttEndpointMS <= 0 {
		sttEndpointMS = envInt("PICOCLAW_VAD_ENDPOINT_MS", 1000)
	}

	// Open transcription stream with provider-specific options. The
	// auto-reconnect wrapper redials if the provider drops the stream
	// mid-session (e.g. transient ASR failures) so the session never goes deaf.
	stream, err := stt.NewAutoReconnectStream(rs.ctx, rs.stt, stt.StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       language,
		Model:          model,
		InterimResults: true,
		EndpointingMS:  sttEndpointMS,
	})
	if err != nil {
		logger.ErrorCF("livekit", "Failed to open STT stream", map[string]any{
			"provider": rs.stt.Name(),
			"error":    err.Error(),
		})
		rs.speakSTTUnavailableFallback(err)
		return
	}

	logger.InfoCF("livekit", "STT stream opened", map[string]any{
		"provider":              rs.stt.Name(),
		"language":              language,
		"session_language_name": policy.DisplayName,
		"session_language_code": policy.RawCode,
	})

	var vadPipe *vad.VADPipeline
	var vadEventChan chan vad.VADEvent
	vadThreshold := float32(rs.runtime.VADThreshold)
	if vadThreshold <= 0 {
		vadThreshold = envFloat("PICOCLAW_VAD_THRESHOLD", 0.7)
	}
	vadEndpointMS := sttEndpointMS
	engine, err := vad.NewTenVAD(256, vadThreshold)
	if err == nil {
		vadPipe = vad.NewVADPipeline(engine, vadThreshold, vadEndpointMS)
		vadEventChan = make(chan vad.VADEvent, 10)
		logger.InfoCF("livekit", "TEN VAD initialized", map[string]any{
			"room":        rs.roomInfo.Name,
			"threshold":   vadThreshold,
			"endpoint_ms": vadEndpointMS,
		})
	} else {
		logger.ErrorCF("livekit", "Failed to init TEN VAD", map[string]any{"error": err.Error()})
	}

	writer := &sttStreamWriter{
		stream:       stream,
		vad:          vadPipe,
		vadEvent:     vadEventChan,
		providerName: rs.stt.Name(),
		sessionKey:   ps.sessionKey,
		room:         rs.roomName(),
	}
	pcmTrack, err := lkmedia.NewPCMRemoteTrack(track, writer, lkmedia.WithTargetSampleRate(16000), lkmedia.WithTargetChannels(1))
	if err != nil {
		logger.ErrorCF("livekit", "PCM remote track error", map[string]any{"error": err.Error()})
		_ = stream.Close()
		return
	}

	ps.mu.Lock()
	ps.sttStream = stream
	ps.pcmTrack = pcmTrack
	ps.mu.Unlock()

	var vadEventInterface <-chan interface{}
	if vadEventChan != nil {
		ch := make(chan interface{}, 10)
		go func() {
			for evt := range vadEventChan {
				ch <- evt
			}
			close(ch)
		}()
		vadEventInterface = ch
	}

	pipeline := NewAudioPipeline(rs, rs.bridge, rs.tts, vadEventInterface)
	rs.mu.Lock()
	rs.activePipeline = pipeline
	rs.mu.Unlock()
	go pipeline.RunInbound(rs.ctx, stream)

	// Fire proactive LLM greeting precisely when the deepgram and VAD systems
	// are confirmed fully open and actively listening to the subscribed track.
	switch strings.ToLower(strings.TrimSpace(rs.runtime.GreetingMode)) {
	case "disabled":
		logger.InfoCF("livekit", "Greeting disabled by runtime policy", map[string]any{
			"room": rs.roomInfo.Name,
		})
	case "fallback":
		go pipeline.TriggerGreetingFallbackOnly(rs.ctx, pipeline.sessionKey(), "runtime_policy")
	default:
		go pipeline.TriggerGreeting(rs.ctx, pipeline.sessionKey())
	}
}

func sttUnavailableFallbackPhrase() string {
	return "I'm having trouble hearing you right now. Please reconnect and try again."
}

func (rs *RoomSession) speakSTTUnavailableFallback(cause error) {
	if rs == nil || rs.tts == nil || rs.localTrack == nil || rs.participant == nil {
		return
	}
	parent := rs.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, liveKitTimeoutFallbackMax)
	defer cancel()
	select {
	case <-ctx.Done():
		return
	default:
	}

	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	pipeline := NewAudioPipeline(rs, rs.bridge, rs.tts, nil)
	pipeline.emitRuntimeEvent("fallback_used", pipeline.sessionKey(), "stt_unavailable", errText, nil)
	logger.WarnCF("livekit", "STT unavailable; playing fallback", map[string]any{
		"room":    rs.roomName(),
		"session": pipeline.sessionKey(),
		"error":   errText,
	})
	pipeline.setTTSCancel(cancel)
	pipeline.publishState("listening", "speaking")
	pipeline.publishSpeechCreated("")
	pipeline.synthesizeAndPlay(ctx, sttUnavailableFallbackPhrase())
	pipeline.publishState("speaking", "listening")
}

func (rs *RoomSession) handleParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	rs.mu.Lock()
	participant := rs.participant
	if participant == nil || participant.identity != rp.Identity() {
		rs.mu.Unlock()
		return
	}
	rs.participant = nil
	rs.activePipeline = nil
	rs.mu.Unlock()

	participant.mu.Lock()
	if participant.sttStream != nil {
		_ = participant.sttStream.Close()
	}
	if participant.pcmTrack != nil {
		participant.pcmTrack.Close()
	}
	if participant.ttsCancel != nil {
		participant.ttsCancel()
	}
	participant.mu.Unlock()
	logger.InfoCF("livekit", "Participant disconnected", map[string]any{
		"room":        rs.roomInfo.Name,
		"participant": rp.Identity(),
	})

	// When the user hangs up or leaves, the agent should leave the room to trigger ephemeral cleanup.
	go rs.Leave()
}

func (rs *RoomSession) generateRoomToken() (string, error) {
	if rs.apiKey == "" || rs.apiSecret == "" {
		return "", errors.New("livekit api key/secret missing")
	}
	identity := "picoclaw"
	if rs.agentName != "" {
		identity = sanitizeIdentity(rs.agentName)
	}

	at := auth.NewAccessToken(rs.apiKey, rs.apiSecret)
	grant := &auth.VideoGrant{RoomJoin: true, Room: rs.roomInfo.Name}
	at.SetVideoGrant(grant)
	at.SetIdentity(identity)
	at.SetKind(livekit.ParticipantInfo_AGENT)
	at.SetAttributes(map[string]string{
		"lk.agent.state": "listening",
	})
	return at.ToJWT()
}

func (rs *RoomSession) roomName() string {
	if rs == nil || rs.roomInfo == nil {
		return ""
	}
	return rs.roomInfo.Name
}

func (rs *RoomSession) sessionKeyForParticipant(identity string) string {
	if rs == nil {
		return ""
	}
	if rs.deviceMAC != "" {
		return "livekit:device:" + strings.ReplaceAll(rs.deviceMAC, ":", "")
	}
	if rs.agentID != "" {
		return "livekit:agent:" + sanitizeIdentity(rs.agentID)
	}
	return fmt.Sprintf("livekit:%s:%s", rs.roomName(), identity)
}

func sanitizeIdentity(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, ":", "-")
	return value
}

type sttStreamWriter struct {
	stream   stt.TranscriptionStream
	vad      *vad.VADPipeline
	vadEvent chan vad.VADEvent

	providerName string
	sessionKey   string
	room         string
	sendErrCount int
}

func (w *sttStreamWriter) WriteSample(sample media.PCM16Sample) error {
	if w.stream == nil {
		return nil
	}
	if len(sample) == 0 {
		return nil
	}

	// Feed audio to VAD pipeline if available
	if w.vad != nil {
		int16Samples := make([]int16, len(sample))
		copy(int16Samples, sample)

		events := w.vad.Push(int16Samples)
		for _, evt := range events {
			if evt.SpeechStart {
				logger.InfoCF("livekit", "TEN VAD speech start detected", map[string]any{
					"provider":    w.providerName,
					"session":     w.sessionKey,
					"room":        w.room,
					"probability": evt.Probability,
				})
			}
			if evt.SpeechEnd {
				logger.InfoCF("livekit", "TEN VAD speech end detected", map[string]any{
					"provider":    w.providerName,
					"session":     w.sessionKey,
					"room":        w.room,
					"probability": evt.Probability,
				})
			}
			if w.vadEvent != nil {
				select {
				case w.vadEvent <- evt:
				default:
				}
			}
		}
	}

	// Convert sample to PCM bytes (16-bit little-endian)
	buf := make([]byte, len(sample)*2)
	for i, v := range sample {
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(v))
	}

	if err := w.stream.SendAudio(buf); err != nil {
		w.sendErrCount++
		if w.sendErrCount == 1 || w.sendErrCount%200 == 0 {
			logger.WarnCF("livekit", "STT SendAudio failed", map[string]any{
				"provider":       w.providerName,
				"send_err_count": w.sendErrCount,
				"error":          err.Error(),
			})
		}
		// Keep ingesting audio/VAD even if STT backend has transient issues.
		return nil
	}
	w.sendErrCount = 0
	return nil
}

func (w *sttStreamWriter) Close() error {
	if w.vad != nil {
		w.vad.Close()
	}
	if w.stream == nil {
		return nil
	}
	return w.stream.Close()
}

func envFloat(key string, def float32) float32 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f >= 1 {
		return def
	}
	return float32(f)
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
