package livekit

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/livekit/media-sdk"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/pion/webrtc/v4"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
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

	serverURL   string
	token       string
	deepgram    *deepgram.DeepgramTranscriber
	tts         tts.Provider
	apiKey      string
	apiSecret   string
	agentName   string
	sampleRate  int
	fillerWords []string
}

// ParticipantState tracks per-participant voice session state.
type ParticipantState struct {
	identity       string
	sessionKey     string
	deepgramStream deepgram.TranscriptionStream
	pcmTrack       *lkmedia.PCMRemoteTrack
	ttsCancel      context.CancelFunc
	speaking       atomic.Bool
	mu             sync.Mutex
}

// RoomSessionConfig configures a RoomSession.
type RoomSessionConfig struct {
	Worker      *Worker
	JobID       string
	RoomInfo    *livekit.Room
	Bridge      *AgentBridge
	ServerURL   string
	Token       string
	Deepgram    *deepgram.DeepgramTranscriber
	TTS         tts.Provider
	APIKey      string
	APISecret   string
	AgentName   string
	SampleRate  int
	FillerWords []string
}

// NewRoomSession creates a new room session for a job.
func NewRoomSession(cfg RoomSessionConfig) (*RoomSession, error) {
	if cfg.RoomInfo == nil {
		return nil, errors.New("room info is nil")
	}
	if cfg.ServerURL == "" {
		return nil, errors.New("server url is empty")
	}
	return &RoomSession{
		worker:      cfg.Worker,
		jobID:       cfg.JobID,
		roomInfo:    cfg.RoomInfo,
		bridge:      cfg.Bridge,
		serverURL:   cfg.ServerURL,
		token:       cfg.Token,
		deepgram:    cfg.Deepgram,
		tts:         cfg.TTS,
		apiKey:      cfg.APIKey,
		apiSecret:   cfg.APISecret,
		agentName:   cfg.AgentName,
		sampleRate:  cfg.SampleRate,
		fillerWords: cfg.FillerWords,
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

// Leave disconnects from the room.
func (rs *RoomSession) Leave() {
	rs.mu.Lock()
	if rs.cancel != nil {
		rs.cancel()
	}
	participant := rs.participant
	rs.participant = nil
	rs.mu.Unlock()

	if participant != nil {
		participant.mu.Lock()
		if participant.deepgramStream != nil {
			_ = participant.deepgramStream.Close()
		}
		if participant.pcmTrack != nil {
			participant.pcmTrack.Close()
		}
		if participant.ttsCancel != nil {
			participant.ttsCancel()
		}
		participant.mu.Unlock()
	}

	if rs.localTrack != nil {
		_ = rs.localTrack.Close()
	}
	if rs.room != nil {
		rs.room.Disconnect()
	}
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
		sessionKey: "",
	}
	rs.participant = ps
	rs.mu.Unlock()

	if rs.deepgram == nil {
		logger.WarnC("livekit", "Deepgram transcriber not configured")
		return
	}

	stream, err := rs.deepgram.OpenStream(deepgram.StreamOpts{})
	if err != nil {
		logger.ErrorCF("livekit", "Deepgram stream error", map[string]any{"error": err.Error()})
		return
	}
	logger.InfoCF("livekit", "Deepgram stream opened", map[string]any{
		"room":        rs.roomInfo.Name,
		"participant": rp.Identity(),
	})

	writer := &deepgramWriter{stream: stream}
	pcmTrack, err := lkmedia.NewPCMRemoteTrack(track, writer, lkmedia.WithTargetSampleRate(16000), lkmedia.WithTargetChannels(1))
	if err != nil {
		logger.ErrorCF("livekit", "PCM remote track error", map[string]any{"error": err.Error()})
		_ = stream.Close()
		return
	}

	ps.mu.Lock()
	ps.deepgramStream = stream
	ps.pcmTrack = pcmTrack
	ps.mu.Unlock()

	pipeline := NewAudioPipeline(rs, rs.bridge, rs.tts)
	go pipeline.RunInbound(rs.ctx, stream)
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
	if participant.deepgramStream != nil {
		_ = participant.deepgramStream.Close()
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

func sanitizeIdentity(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, ":", "-")
	return value
}

type deepgramWriter struct {
	stream deepgram.TranscriptionStream
}

func (w *deepgramWriter) WriteSample(sample media.PCM16Sample) error {
	if w.stream == nil {
		return nil
	}
	if len(sample) == 0 {
		return nil
	}
	buf := make([]byte, len(sample)*2)
	for i, v := range sample {
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(v))
	}
	return w.stream.SendAudio(buf)
}

func (w *deepgramWriter) Close() error {
	if w.stream == nil {
		return nil
	}
	return w.stream.Close()
}
