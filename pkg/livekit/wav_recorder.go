package livekit

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type wavRecorder struct {
	enabled       bool
	outputDir     string
	label         string
	sessionKey    string
	sampleRate    int
	channels      int
	bitsPerSample int
	maxPCMBytes   int64
	file          *os.File
	filePath      string
	recordedBytes int64
	fileIndex     int
}

func newTTSAudioRecorder(sessionKey string, sampleRate int) *wavRecorder {
	envEnabled := firstEnv("SAVE_TTS_AUDIO_WAV", "SAVE_ELEVENLABS_AUDIO_WAV")
	if !truthyEnv(envEnabled) {
		return nil
	}

	maxSeconds := parseEnvInt(firstEnvName("SAVE_TTS_AUDIO_MAX_SECONDS", "SAVE_ELEVENLABS_AUDIO_MAX_SECONDS"), 30)
	if maxSeconds < 1 {
		maxSeconds = 1
	}
	if maxSeconds > 300 {
		maxSeconds = 300
	}

	if sampleRate <= 0 {
		sampleRate = 24000
	}

	outputDir := strings.TrimSpace(firstEnv("SAVE_TTS_AUDIO_DIR", "SAVE_ELEVENLABS_AUDIO_DIR"))
	if outputDir == "" {
		outputDir = filepath.Join(os.Getenv("HOME"), ".picoclaw", "logs", "tts-audio")
	}

	recorder := &wavRecorder{
		enabled:       true,
		outputDir:     outputDir,
		label:         "tts-audio",
		sessionKey:    sanitizeWavPathSegment(sessionKey),
		sampleRate:    sampleRate,
		channels:      1,
		bitsPerSample: 16,
		maxPCMBytes:   int64(sampleRate * 2 * maxSeconds),
	}

	logger.InfoCF("livekit", "TTS WAV capture enabled", map[string]any{
		"session":     sessionKey,
		"sample_rate": sampleRate,
		"max_seconds": maxSeconds,
		"dir":         outputDir,
	})

	return recorder
}

func newElevenLabsAudioRecorder(sessionKey string, sampleRate int) *wavRecorder {
	return newTTSAudioRecorder(sessionKey, sampleRate)
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func firstEnvName(names ...string) string {
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return name
		}
	}
	return names[len(names)-1]
}

func truthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseEnvInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}
	return parsed
}

var wavPathSegmentRE = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeWavPathSegment(value string) string {
	sanitized := wavPathSegmentRE.ReplaceAllString(value, "_")
	sanitized = strings.Trim(sanitized, "_")
	if len(sanitized) > 96 {
		sanitized = sanitized[:96]
	}
	if sanitized == "" {
		return "unknown"
	}
	return sanitized
}

func (r *wavRecorder) AppendPCM(chunk []byte) {
	if r == nil || !r.enabled || len(chunk) == 0 {
		return
	}

	if len(chunk)%2 != 0 {
		chunk = chunk[:len(chunk)-1]
	}
	if len(chunk) == 0 {
		return
	}

	if err := r.ensureFile(); err != nil {
		logger.WarnCF("livekit", "Failed to initialize TTS WAV capture", map[string]any{
			"error": err.Error(),
			"dir":   r.outputDir,
		})
		r.enabled = false
		return
	}

	remaining := r.maxPCMBytes - r.recordedBytes
	if remaining <= 0 {
		r.Finalize("segment_full")
		r.AppendPCM(chunk)
		return
	}

	writeLen := int64(len(chunk))
	if writeLen > remaining {
		writeLen = remaining
	}
	if _, err := r.file.Write(chunk[:writeLen]); err != nil {
		logger.WarnCF("livekit", "Failed to write TTS WAV capture", map[string]any{
			"error": err.Error(),
			"path":  r.filePath,
		})
		r.enabled = false
		return
	}

	r.recordedBytes += writeLen
	if r.recordedBytes >= r.maxPCMBytes {
		r.Finalize("segment_full")
		if writeLen < int64(len(chunk)) {
			r.AppendPCM(chunk[writeLen:])
		}
	}
}

func (r *wavRecorder) ensureFile() error {
	if r.file != nil {
		return nil
	}
	if err := os.MkdirAll(r.outputDir, 0755); err != nil {
		return err
	}

	r.fileIndex++
	timestamp := strings.ReplaceAll(time.Now().UTC().Format("2006-01-02T15-04-05.000000000Z"), ".", "-")
	filename := fmt.Sprintf("%s_%s_%s_part%04d.wav", timestamp, r.label, r.sessionKey, r.fileIndex)
	r.filePath = filepath.Join(r.outputDir, filename)

	file, err := os.Create(r.filePath)
	if err != nil {
		return err
	}
	r.file = file
	_, err = r.file.Write(buildWavHeader(0, r.sampleRate, r.channels, r.bitsPerSample))
	return err
}

func (r *wavRecorder) Finalize(reason string) {
	if r == nil || r.file == nil {
		return
	}

	if _, err := r.file.Seek(0, 0); err == nil {
		_, _ = r.file.Write(buildWavHeader(uint32(r.recordedBytes), r.sampleRate, r.channels, r.bitsPerSample))
	}
	_ = r.file.Close()

	durationSeconds := float64(r.recordedBytes) / float64(r.sampleRate*r.channels*(r.bitsPerSample/8))
	logger.InfoCF("livekit", "TTS WAV capture saved", map[string]any{
		"reason":           reason,
		"path":             r.filePath,
		"recorded_bytes":   r.recordedBytes,
		"duration_seconds": durationSeconds,
	})

	r.file = nil
	r.filePath = ""
	r.recordedBytes = 0
}

func buildWavHeader(dataSize uint32, sampleRate, channels, bitsPerSample int) []byte {
	header := make([]byte, 44)
	bytesPerSample := bitsPerSample / 8
	blockAlign := channels * bytesPerSample
	byteRate := sampleRate * blockAlign

	copy(header[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(header[4:8], 36+dataSize)
	copy(header[8:12], []byte("WAVE"))
	copy(header[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(header[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(header[34:36], uint16(bitsPerSample))
	copy(header[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(header[40:44], dataSize)

	return header
}
