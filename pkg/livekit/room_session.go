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

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

// RoomSession manages one agent in one LiveKit room (one job).
type RoomSession struct {
	worker        *Worker
	jobID         string
	roomInfo      *livekit.Room
	bridge        *AgentBridge
	room          *lksdk.Room
	localTrack    *lkmedia.PCMLocalTrack
	localTrackSID string
	participant   *ParticipantState
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc

	serverURL        string
	token            string
	stt              stt.Provider
	tts              tts.Provider
	apiKey           string
	apiSecret        string
	agentName        string
	sampleRate       int
	fillerWords      []string
	primaryLanguage  string // from room metadata, used for language-aware fallbacks
	managerAPIURL    string
	managerAPISecret string
	deviceMAC        string
	agentID          string
	closeOnce        sync.Once
}

// ParticipantState tracks per-participant voice session state.
type ParticipantState struct {
	identity   string
	sessionKey string
	sttStream  stt.TranscriptionStream
	pcmTrack   *lkmedia.PCMRemoteTrack
	ttsCancel  context.CancelFunc
	speaking   atomic.Bool
	mu         sync.Mutex
}

// RoomSessionConfig configures a RoomSession.
type RoomSessionConfig struct {
	Worker          *Worker
	JobID           string
	RoomInfo        *livekit.Room
	Bridge          *AgentBridge
	ServerURL       string
	Token           string
	STT             stt.Provider
	TTS             tts.Provider
	APIKey          string
	APISecret       string
	AgentName       string
	SampleRate      int
	FillerWords     []string
	PrimaryLanguage string // e.g. "Hindi", "English" — from room metadata
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
	managerAPISecret := strings.TrimSpace(os.Getenv("MANAGER_API_SECRET"))
	deviceMAC, agentID := resolvePersistenceFields(cfg.RoomInfo.Name, cfg.RoomInfo.Metadata)

	return &RoomSession{
		worker:           cfg.Worker,
		jobID:            cfg.JobID,
		roomInfo:         cfg.RoomInfo,
		bridge:           cfg.Bridge,
		serverURL:        cfg.ServerURL,
		token:            cfg.Token,
		stt:              cfg.STT,
		tts:              cfg.TTS,
		apiKey:           cfg.APIKey,
		apiSecret:        cfg.APISecret,
		agentName:        cfg.AgentName,
		sampleRate:       cfg.SampleRate,
		fillerWords:      cfg.FillerWords,
		primaryLanguage:  cfg.PrimaryLanguage,
		managerAPIURL:    managerAPIURL,
		managerAPISecret: managerAPISecret,
		deviceMAC:        deviceMAC,
		agentID:          agentID,
	}, nil
}

// Join connects to the LiveKit room.
func (rs *RoomSession) Join(ctx context.Context) error {
	if rs == nil {
		return errors.New("room session is nil")
	}
	rs.ctx, rs.cancel = context.WithCancel(ctx)

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
	localTrack := rs.localTrack
	rs.localTrack = nil
	room := rs.room
	rs.room = nil
	bridge := rs.bridge
	rs.bridge = nil
	rs.mu.Unlock()

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
	}
}

// handleEndPrompt asks the LLM to generate and speak a farewell message,
// then disconnects. A 10-second deadline prevents hanging on a slow model.
func (rs *RoomSession) handleEndPrompt(prompt string) {
	if rs.bridge == nil {
		return
	}
	rs.mu.Lock()
	participant := rs.participant
	rs.mu.Unlock()
	sessionKey := ""
	if participant != nil {
		sessionKey = participant.sessionKey
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build a minimal pipeline just for TTS playback of the goodbye.
	pipeline := NewAudioPipeline(rs, rs.bridge, rs.tts, nil)
	_, _ = pipeline.HandleUtterance(ctx, sessionKey, prompt, nil)

	// Brief pause so TTS audio finishes flushing before we disconnect.
	time.Sleep(500 * time.Millisecond)
	rs.Leave()
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
		return
	}

	// Get provider capabilities and configure stream options
	caps := rs.stt.Capabilities()

	// Determine model and language
	model := ""
	language := rs.primaryLanguage

	// Validate language support if provider declares languages
	if len(caps.Languages) > 0 && language != "" {
		supported := false
		for _, lang := range caps.Languages {
			if lang == language || lang == "auto" || lang == "multi" {
				supported = true
				break
			}
		}
		if !supported {
			logger.WarnCF("livekit", "Language not supported, using auto", map[string]any{
				"language": language,
				"provider": rs.stt.Name(),
			})
			language = "auto"
		}
	}

	// Open transcription stream with provider-specific options
	stream, err := rs.stt.OpenStream(rs.ctx, stt.StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       language,
		Model:          model,
		InterimResults: true,
		EndpointingMS:  800,
	})
	if err != nil {
		logger.ErrorCF("livekit", "Failed to open STT stream", map[string]any{
			"provider": rs.stt.Name(),
			"error":    err.Error(),
		})
		return
	}

	logger.InfoCF("livekit", "STT stream opened", map[string]any{
		"provider": rs.stt.Name(),
		"language": language,
	})

	var vadPipe *vad.VADPipeline
	var vadEventChan chan vad.VADEvent
	vadThreshold := envFloat("PICOCLAW_VAD_THRESHOLD", 0.7)
	vadEndpointMS := envInt("PICOCLAW_VAD_ENDPOINT_MS", 1000)
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
	go pipeline.RunInbound(rs.ctx, stream)

	// Fire proactive LLM greeting precisely when the deepgram and VAD systems
	// are confirmed fully open and actively listening to the subscribed track.
	go pipeline.TriggerGreeting(rs.ctx, pipeline.sessionKey())
}

func (rs *RoomSession) handleParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	rs.mu.Lock()
	participant := rs.participant
	if participant == nil || participant.identity != rp.Identity() {
		rs.mu.Unlock()
		return
	}
	rs.participant = nil
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
