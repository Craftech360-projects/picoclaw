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
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultSarvamRESTURL = "https://api.sarvam.ai/speech-to-text"

// The sync REST endpoint rejects clips over 30 seconds; buffering past that
// only grows a request that is guaranteed a 400.
const sarvamRESTMaxSeconds = 30

// Overridable the same way the streaming URL is, which also lets a test point it
// at httptest instead of the real endpoint.
func sarvamRESTEndpoint() string {
	if override := strings.TrimSpace(os.Getenv("SARVAM_STT_REST_URL")); override != "" {
		return override
	}
	return defaultSarvamRESTURL
}

// SarvamRESTProvider transcribes one complete utterance per REST call instead of
// streaming. It exists for Manual Talk (ADR 0007): the device's tap gives an
// exact turn boundary, so audio is buffered until Finalize and sent as a single
// WAV. Verified against real child audio: the same clip whose streaming final
// was "Nalpak. Na" transcribes cleanly in ~0.6s (issue ptt-batch-stt/001).
//
// Distinct from Sarvam's async "Batch API" product — hence "rest", not "batch".
type SarvamRESTProvider struct {
	apiKey string
	model  string
}

// NewSarvamRESTProvider creates a Sarvam REST STT provider.
func NewSarvamRESTProvider(apiKey, model string) *SarvamRESTProvider {
	return &SarvamRESTProvider{apiKey: apiKey, model: model}
}

func (p *SarvamRESTProvider) Name() string { return "sarvam_rest" }

func (p *SarvamRESTProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"unknown", "en-IN", "hi-IN", "bn-IN", "kn-IN", "ml-IN", "mr-IN", "od-IN", "pa-IN", "ta-IN", "te-IN", "gu-IN"},
		Models:               []string{"saaras:v4", "saaras:v3"},
		SupportsStreaming:    false,
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

// WithConfig implements configurableProvider so the factory can hand this
// provider the active row's key and model.
func (p *SarvamRESTProvider) WithConfig(apiKey, model string) Provider {
	cfg := *p
	if strings.TrimSpace(apiKey) != "" {
		cfg.apiKey = apiKey
	}
	if strings.TrimSpace(model) != "" {
		cfg.model = model
	}
	return &cfg
}

func (p *SarvamRESTProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, fmt.Errorf("sarvam_rest: API key not configured")
	}
	model := strings.TrimSpace(p.model)
	if model == "" {
		model = "saaras:v4"
	}
	// saaras is the speech-translation family: without an explicit transcribe
	// mode it would English-ify a Hindi-speaking child. Accepted by v4 (001).
	mode := ""
	if strings.HasPrefix(model, "saaras") {
		mode = "transcribe"
	}
	language := strings.TrimSpace(opts.Language)
	if language == "" {
		// Auto-detect; the response carries the detected language back.
		language = "unknown"
	}
	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	return newSarvamRESTAdapter(ctx, p.apiKey, model, language, mode, sampleRate), nil
}

// sarvamRESTAdapter buffers the utterance, then POSTs it as a WAV on Finalize.
type sarvamRESTAdapter struct {
	apiKey     string
	model      string
	language   string
	mode       string
	sampleRate int
	maxBytes   int

	ctx        context.Context
	resultChan chan TranscriptEvent
	client     *http.Client

	mu          sync.Mutex
	audioBuffer []byte
	capWarned   bool
	onEmpty     func()
	inFlight    sync.WaitGroup

	closeOnce sync.Once
	closed    chan struct{}
}

func newSarvamRESTAdapter(ctx context.Context, apiKey, model, language, mode string, sampleRate int) *sarvamRESTAdapter {
	return &sarvamRESTAdapter{
		apiKey:     apiKey,
		model:      model,
		language:   language,
		mode:       mode,
		sampleRate: sampleRate,
		maxBytes:   sarvamRESTMaxSeconds * sampleRate * 2,
		ctx:        ctx,
		resultChan: make(chan TranscriptEvent, 8),
		// A hung request must not outlive the turn it belongs to — the streaming
		// transport's failure mode was waiting forever.
		client: &http.Client{Timeout: 10 * time.Second},
		closed: make(chan struct{}),
	}
}

func (s *sarvamRESTAdapter) SendAudio(pcm []byte) error {
	select {
	case <-s.closed:
		return fmt.Errorf("sarvam_rest stream closed")
	default:
	}
	if len(pcm) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.maxBytes - len(s.audioBuffer)
	if room <= 0 {
		if !s.capWarned {
			s.capWarned = true
			logger.WarnCF("livekit", "Sarvam REST buffer at cap, dropping audio", map[string]any{
				"provider": "sarvam_rest", "cap_seconds": sarvamRESTMaxSeconds,
			})
		}
		return nil
	}
	if len(pcm) > room {
		pcm = pcm[:room]
	}
	s.audioBuffer = append(s.audioBuffer, pcm...)
	return nil
}

func (s *sarvamRESTAdapter) Results() <-chan TranscriptEvent { return s.resultChan }

// ResetBuffer discards buffered audio. Called on a fresh press (a new utterance
// must not inherit the last one) and on Cancel Turn (discard, stay silent).
func (s *sarvamRESTAdapter) ResetBuffer() {
	s.mu.Lock()
	s.audioBuffer = nil
	s.capWarned = false
	s.mu.Unlock()
}

// SetEmptyResultHandler registers the callback fired when real audio produced no
// transcript — the empty-tap case the pipeline answers with "I didn't hear you".
// The callback runs on the transcription goroutine, NOT the pipeline event
// loop: hand off (channel/Schedule) before touching loop-owned state.
func (s *sarvamRESTAdapter) SetEmptyResultHandler(fn func()) {
	s.mu.Lock()
	s.onEmpty = fn
	s.mu.Unlock()
}

// Finalize takes the buffered utterance and transcribes it off the caller's
// goroutine, so the pipeline event loop never waits on HTTP.
//
// ponytail: two Finalize calls with the first POST still in flight (25s
// hard-cap racing a speech_end) emit in HTTP-completion order, not speech
// order — add an ordered queue only if that collision shows up in real logs.
func (s *sarvamRESTAdapter) Finalize() error {
	s.mu.Lock()
	// Checked under the same mutex Close holds while closing s.closed, so the
	// inFlight.Add below is ordered strictly before Close's Wait — a late
	// transcribe goroutine can never outlive resultChan.
	select {
	case <-s.closed:
		s.mu.Unlock()
		return fmt.Errorf("sarvam_rest stream closed")
	default:
	}
	utterance := s.audioBuffer
	s.audioBuffer = nil
	s.capWarned = false
	if len(utterance) > 0 {
		s.inFlight.Add(1)
	}
	s.mu.Unlock()

	if len(utterance) == 0 {
		// Cancel Turn lands here: nothing buffered, nothing to do, no callback.
		logger.DebugCF("livekit", "Sarvam REST finalize with no audio buffered", map[string]any{
			"provider": "sarvam_rest",
		})
		return nil
	}

	go func() {
		defer s.inFlight.Done()
		s.transcribe(utterance)
	}()
	return nil
}

func (s *sarvamRESTAdapter) buildRequest(wav []byte) (*http.Request, error) {
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("file", "utterance.wav")
	if err == nil {
		_, err = part.Write(wav)
	}
	if err == nil {
		fields := map[string]string{
			"model": s.model,
			"mode":  s.mode,
		}
		// Omitted rather than sent blank: an empty language_code is not an
		// accepted value ("unknown" is what auto-detect looks like here).
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
		return nil, err
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, sarvamRESTEndpoint(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-subscription-key", s.apiKey)
	req.Header.Set("Content-Type", form.FormDataContentType())
	return req, nil
}

func (s *sarvamRESTAdapter) transcribe(pcm []byte) {
	started := time.Now()
	seconds := float64(len(pcm)/2) / float64(s.sampleRate)

	wav, err := createWAVFromPCM(pcm, s.sampleRate)
	if err != nil {
		logger.WarnCF("livekit", "Sarvam REST could not build WAV", map[string]any{
			"provider": "sarvam_rest", "error": err.Error(),
		})
		return
	}

	var resp *http.Response
	for attempt := 1; attempt <= 2; attempt++ {
		req, buildErr := s.buildRequest(wav)
		if buildErr != nil {
			logger.WarnCF("livekit", "Sarvam REST could not build request", map[string]any{
				"provider": "sarvam_rest", "error": buildErr.Error(),
			})
			return
		}
		resp, err = s.client.Do(req)
		// Retry transport errors and 5xx once; a 4xx is deterministic (bad key,
		// over-limit clip) and will not improve on a second attempt.
		if err == nil && resp.StatusCode < 500 {
			break
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}
		if attempt == 1 {
			logger.WarnCF("livekit", "Sarvam REST attempt failed, retrying once", map[string]any{
				"provider": "sarvam_rest", "attempt": attempt,
				"error": fmt.Sprintf("%v", err), "status": statusOrZero(resp),
			})
			resp = nil
			continue
		}
		logger.WarnCF("livekit", "Sarvam REST failed after retry", map[string]any{
			"provider": "sarvam_rest", "error": fmt.Sprintf("%v", err),
			"status": statusOrZero(resp), "audio_seconds": roundTo2(seconds),
			"elapsed_ms": time.Since(started).Milliseconds(),
		})
		return
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		// The status and body are the whole point of moving to REST: a rejection
		// is visible instead of arriving as silence.
		logger.WarnCF("livekit", "Sarvam REST returned an error status", map[string]any{
			"provider": "sarvam_rest", "status": resp.StatusCode,
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
			"provider": "sarvam_rest", "error": err.Error(),
			"body": truncateForLog(string(raw), 400),
		})
		return
	}

	text := strings.TrimSpace(decoded.Transcript)
	if text == "" {
		// Sarvam heard nothing in real audio — the empty-tap case. The pipeline
		// answers it ("I didn't hear you") through the registered handler.
		logger.WarnCF("livekit", "Sarvam REST returned an empty transcript", map[string]any{
			"provider": "sarvam_rest", "request_id": decoded.RequestID,
			"audio_seconds": roundTo2(seconds), "elapsed_ms": time.Since(started).Milliseconds(),
		})
		s.mu.Lock()
		onEmpty := s.onEmpty
		s.mu.Unlock()
		if onEmpty != nil {
			onEmpty()
		}
		return
	}

	language := strings.TrimSpace(decoded.LanguageCode)
	if language == "" {
		language = s.language
	}

	logger.InfoCF("livekit", "Sarvam REST transcript received", map[string]any{
		"provider": "sarvam_rest", "chars": len(text), "language": language,
		"request_id":    decoded.RequestID,
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
// closed is closed under s.mu — paired with Finalize's mutex-guarded check —
// so every transcribe goroutine is either registered in inFlight before the
// Wait, or never spawned.
func (s *sarvamRESTAdapter) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		close(s.closed)
		s.mu.Unlock()
		s.inFlight.Wait()
		close(s.resultChan)
	})
	return nil
}

func statusOrZero(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func roundTo2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
