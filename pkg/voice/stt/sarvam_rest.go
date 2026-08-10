package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultSarvamRESTURL = "https://api.sarvam.ai/speech-to-text"

// Overridable the same way the streaming URL is, which also lets a test point it
// at httptest instead of the real endpoint.
func sarvamRESTEndpoint() string {
	if override := strings.TrimSpace(os.Getenv("SARVAM_STT_REST_URL")); override != "" {
		return override
	}
	return defaultSarvamRESTURL
}

// Batch transport for Sarvam STT: buffer the utterance, then POST it as a WAV to
// the REST endpoint on Finalize.
//
// Exists because the streaming websocket transport went silent on real sessions —
// good audio in (12.7s, RMS -30.6 dBFS), flush sent, and nothing back for 38
// seconds until the device gave up, with no message reaching the parser at all.
// This path is simple enough to be diagnosable: one request, one response, one
// status code.
//
// Selected with SARVAM_STT_TRANSPORT=rest, defaulting to streaming, so both live
// in one build and switching is a restart rather than a deploy.
type sarvamRESTAdapter struct {
	apiKey     string
	model      string
	language   string
	mode       string
	sampleRate int

	ctx        context.Context
	resultChan chan TranscriptEvent
	client     *http.Client

	mu          sync.Mutex
	audioBuffer []byte
	inFlight    sync.WaitGroup

	closeOnce sync.Once
	closed    chan struct{}
}

func newSarvamRESTAdapter(ctx context.Context, apiKey, model, language, mode string, sampleRate int) *sarvamRESTAdapter {
	if strings.TrimSpace(model) == "" {
		model = "saaras:v4"
	}
	return &sarvamRESTAdapter{
		apiKey:     apiKey,
		model:      model,
		language:   language,
		mode:       mode,
		sampleRate: sampleRate,
		ctx:        ctx,
		resultChan: make(chan TranscriptEvent, 8),
		// Generous but bounded: a hung request must not outlive the turn it belongs
		// to, and the streaming transport's failure mode was waiting forever.
		client: &http.Client{Timeout: 20 * time.Second},
		closed: make(chan struct{}),
	}
}

func (s *sarvamRESTAdapter) SendAudio(pcm []byte) error {
	select {
	case <-s.closed:
		return fmt.Errorf("sarvam stream closed")
	default:
	}
	if len(pcm) == 0 {
		return nil
	}
	s.mu.Lock()
	s.audioBuffer = append(s.audioBuffer, pcm...)
	s.mu.Unlock()
	return nil
}

func (s *sarvamRESTAdapter) Results() <-chan TranscriptEvent { return s.resultChan }

// Finalize takes the buffered utterance and transcribes it off the caller's
// goroutine. Doing the POST inline would block the audio path for the length of
// the request, which is how the batch providers already in this package behave
// and is why they add latency to every turn.
func (s *sarvamRESTAdapter) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("sarvam stream closed")
	default:
	}

	s.mu.Lock()
	utterance := s.audioBuffer
	s.audioBuffer = nil
	s.mu.Unlock()

	if len(utterance) == 0 {
		logger.DebugCF("livekit", "Sarvam REST finalize with no audio buffered", map[string]any{
			"provider": "sarvam",
		})
		return nil
	}

	s.inFlight.Add(1)
	go func() {
		defer s.inFlight.Done()
		s.transcribe(utterance)
	}()
	return nil
}

func (s *sarvamRESTAdapter) transcribe(pcm []byte) {
	started := time.Now()
	seconds := float64(len(pcm)/2) / float64(s.sampleRate)

	wav, err := createWAVFromPCM(pcm, s.sampleRate)
	if err != nil {
		logger.WarnCF("livekit", "Sarvam REST could not build WAV", map[string]any{
			"provider": "sarvam", "error": err.Error(),
		})
		return
	}

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("file", "utterance.wav")
	if err == nil {
		_, err = part.Write(wav)
	}
	if err == nil {
		fields := map[string]string{
			"model":       s.model,
			"mode":        s.mode,
			"sample_rate": strconv.Itoa(s.sampleRate),
		}
		// Omitted rather than sent blank: an empty language_code is not one of the
		// accepted values, and "unknown" is what auto-detect looks like here.
		if lang := strings.TrimSpace(s.language); lang != "" && !strings.EqualFold(lang, "auto") {
			fields["language_code"] = lang
		}
		for k, v := range fields {
			if v == "" {
				continue
			}
			if err = form.WriteField(k, v); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = form.Close()
	}
	if err != nil {
		logger.WarnCF("livekit", "Sarvam REST could not build request", map[string]any{
			"provider": "sarvam", "error": err.Error(),
		})
		return
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, sarvamRESTEndpoint(), body)
	if err != nil {
		logger.WarnCF("livekit", "Sarvam REST request build failed", map[string]any{
			"provider": "sarvam", "error": err.Error(),
		})
		return
	}
	req.Header.Set("api-subscription-key", s.apiKey)
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		logger.WarnCF("livekit", "Sarvam REST request failed", map[string]any{
			"provider": "sarvam", "error": err.Error(),
			"audio_seconds": roundTo2(seconds), "elapsed_ms": time.Since(started).Milliseconds(),
		})
		return
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		// The status and body are the whole point of moving to REST: a rejection is
		// now visible instead of arriving as silence.
		logger.WarnCF("livekit", "Sarvam REST returned an error status", map[string]any{
			"provider": "sarvam", "status": resp.StatusCode,
			"body":          truncateForLog(string(raw), 400),
			"audio_seconds": roundTo2(seconds), "elapsed_ms": time.Since(started).Milliseconds(),
		})
		return
	}

	var decoded struct {
		Transcript   string `json:"transcript"`
		LanguageCode string `json:"language_code"`
		RequestID    string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		logger.WarnCF("livekit", "Sarvam REST response was not JSON", map[string]any{
			"provider": "sarvam", "error": err.Error(),
			"body": truncateForLog(string(raw), 400),
		})
		return
	}

	text := strings.TrimSpace(decoded.Transcript)
	if text == "" {
		// Logged rather than dropped: an empty transcript is Sarvam saying it heard
		// nothing, and swallowing that is what made the streaming path look hung.
		logger.WarnCF("livekit", "Sarvam REST returned an empty transcript", map[string]any{
			"provider": "sarvam", "request_id": decoded.RequestID,
			"audio_seconds": roundTo2(seconds), "elapsed_ms": time.Since(started).Milliseconds(),
		})
		return
	}

	language := strings.TrimSpace(decoded.LanguageCode)
	if language == "" {
		language = s.language
	}

	logger.InfoCF("livekit", "Sarvam REST transcript received", map[string]any{
		"provider": "sarvam", "chars": len(text), "language": language,
		"audio_seconds": roundTo2(seconds), "elapsed_ms": time.Since(started).Milliseconds(),
	})

	select {
	case s.resultChan <- TranscriptEvent{
		Text:        text,
		IsFinal:     true,
		SpeechStart: true,
		SpeechEnd:   true,
		Language:    language,
		Duration:    seconds,
	}:
	case <-s.closed:
	case <-s.ctx.Done():
	}
}

// Close waits for an in-flight transcription before closing resultChan, so a
// reply that is already on its way cannot race a send onto a closed channel.
func (s *sarvamRESTAdapter) Close() error {
	s.closeOnce.Do(func() {
		logger.WarnCF("livekit", "Sarvam REST stream closing", map[string]any{
			"provider": "sarvam", "called_from": closeCallerOutsideAdapter(),
		})
		close(s.closed)
		s.inFlight.Wait()
		close(s.resultChan)
	})
	return nil
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func roundTo2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// sarvamTransportIsREST reports whether this build should use the batch endpoint.
func sarvamTransportIsREST() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SARVAM_STT_TRANSPORT"))) {
	case "rest", "batch", "http":
		return true
	default:
		return false
	}
}
