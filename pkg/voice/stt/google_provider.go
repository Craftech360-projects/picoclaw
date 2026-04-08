package stt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	speech "cloud.google.com/go/speech/apiv2"
	speechpb "cloud.google.com/go/speech/apiv2/speechpb"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// googleProvider implements STT using Google Cloud Speech-to-Text API
type googleProvider struct {
	apiKey             string
	model              string
	enableDiarization bool
}

// NewGoogleProvider creates a new Google Cloud Speech provider
func NewGoogleProvider(apiKey, model, language string, enableDiarization bool) Provider {
	if model == "" {
		model = "latest_long"
	}
	return &googleProvider{
		apiKey:            apiKey,
		model:             model,
		enableDiarization: enableDiarization,
	}
}

func (p *googleProvider) Name() string { return "google" }

func (p *googleProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "hi", "it", "ja", "ko", "pt", "ru", "zh", "auto"},
		Models:               []string{"chirp_3", "latest_long", "latest_short", "phone_call", "video"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *googleProvider) WithConfig(apiKey, model string) Provider {
	return NewGoogleProvider(apiKey, model, "", false)
}

func (p *googleProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}
	model = normalizeGoogleModel(model)

	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_API_KEY"))
	}

	var httpClient *http.Client
	var projectID string
	var location string
	var speechClient *speech.Client
	if googleUsesV2OAuth(model) {
		var err error
		speechClient, projectID, location, err = newGoogleSpeechClient(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		if apiKey == "" {
			return nil, fmt.Errorf("google cloud: API key not configured")
		}
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	stream := &googleStreamAdapter{
		apiKey:            apiKey,
		model:             model,
		projectID:         projectID,
		location:          location,
		useV2:             googleUsesV2OAuth(model),
		enableDiarization: p.enableDiarization || opts.Channels > 1,
		sampleRate:        opts.SampleRate,
		channels:          maxGoogleChannels(opts.Channels),
		interimResults:    opts.InterimResults,
		endpointing:       time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer:       make([]byte, 0),
		resultChan:        make(chan TranscriptEvent, 10),
		ctx:               ctx,
		mu:                sync.Mutex{},
		httpClient:        httpClient,
		speechClient:      speechClient,
	}

	return stream, nil
}

// googleStreamAdapter handles Google Cloud Speech transcription
type googleStreamAdapter struct {
	apiKey            string
	model             string
	projectID         string
	location          string
	useV2             bool
	enableDiarization bool
	sampleRate        int
	channels          int
	interimResults    bool
	endpointing       time.Duration
	audioBuffer       []byte
	resultChan        chan TranscriptEvent
	ctx               context.Context
	mu                sync.Mutex
	closed            bool
	httpClient        *http.Client
	speechClient      *speech.Client
	stream            speechpb.Speech_StreamingRecognizeClient
	streamCancel      context.CancelFunc
	streamDone        chan error
}

func (s *googleStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}

	if s.useV2 {
		return s.sendAudioV2Locked(pcm)
	}

	s.audioBuffer = append(s.audioBuffer, pcm...)

	if s.endpointing > 0 && len(s.audioBuffer) > s.endpointingThreshold() {
		return s.transcribeBuffer()
	}

	return nil
}

func (s *googleStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *googleStreamAdapter) Finalize() error {
	if s.useV2 {
		return s.finalizeV2()
	}
	return s.transcribeBuffer()
}

func (s *googleStreamAdapter) Close() error {
	s.mu.Lock()
	s.closed = true
	stream := s.stream
	streamDone := s.streamDone
	cancel := s.streamCancel
	s.stream = nil
	s.streamDone = nil
	s.streamCancel = nil
	speechClient := s.speechClient
	s.speechClient = nil
	s.mu.Unlock()

	if stream != nil {
		_ = stream.CloseSend()
	}
	if cancel != nil {
		cancel()
	}
	if streamDone != nil {
		select {
		case <-streamDone:
		case <-time.After(1500 * time.Millisecond):
		}
	}
	if speechClient != nil {
		_ = speechClient.Close()
	}

	close(s.resultChan)
	return nil
}

func (s *googleStreamAdapter) transcribeBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Encode audio to base64 (required by Google Cloud API)
	audioBase64 := base64.StdEncoding.EncodeToString(s.audioBuffer)

	// Build request
	requestBody := map[string]interface{}{
		"config": map[string]interface{}{
			"encoding":        "LINEAR16",
			"sampleRateHertz": s.sampleRate,
			"audioChannelCount": 1,
			"languageCode":    "en-US",
			"alternativeLanguageCodes": []string{"hi-IN"},
			"model":           s.model,
			"enableAutomaticPunctuation": true,
		},
		"audio": map[string]string{
			"content": audioBase64,
		},
	}

	// Add diarization config if enabled
	if s.enableDiarization {
		requestBody["config"].(map[string]interface{})["enableSpeakerDiarization"] = true
		requestBody["config"].(map[string]interface{})["diarizationSpeakerCount"] = 2
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Call Google Cloud Speech API
	url := fmt.Sprintf("https://speech.googleapis.com/v1/speech:recognize?key=%s", s.apiKey)
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transcription failed: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Results []struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
			LanguageCode string `json:"languageCode"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	// Extract best alternative
	if len(result.Results) > 0 && len(result.Results[0].Alternatives) > 0 {
		alt := result.Results[0].Alternatives[0]
		event := TranscriptEvent{
			Text:       alt.Transcript,
			IsFinal:    true,
			Confidence: alt.Confidence,
			Language:   result.Results[0].LanguageCode,
			Duration:   s.calculateDuration(),
		}

		select {
		case s.resultChan <- event:
		default:
		}
	}

	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *googleStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 32000
	}
	bytesPerSecond := s.sampleRate * 2
	return int(s.endpointing.Seconds() * float64(bytesPerSecond))
}

func (s *googleStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}

func normalizeGoogleModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "latest_long":
		return "latest_long"
	case "chirp3", "chirp-3", "chirp_3":
		return "chirp_3"
	default:
		return strings.TrimSpace(model)
	}
}

func googleUsesV2OAuth(model string) bool {
	return model == "chirp_3"
}

func googleChirp3Location() string {
	if location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION")); location != "" {
		return location
	}
	return "us"
}

func googleSpeechHostForLocation(location string) string {
	location = strings.TrimSpace(strings.ToLower(location))
	if location == "" || location == "global" {
		return "speech.googleapis.com"
	}
	return location + "-speech.googleapis.com"
}

func googleSpeechGRPCEndpoint(location string) string {
	return googleSpeechHostForLocation(location) + ":443"
}

func googleRecognizerPath(projectID, location string) string {
	return fmt.Sprintf("projects/%s/locations/%s/recognizers/_", projectID, location)
}

func googleChirp3LanguageCodes() []string {
	return []string{"auto"}
}

func maxGoogleChannels(channels int) int {
	if channels > 0 {
		return channels
	}
	return 1
}

func newGoogleOAuthClient(ctx context.Context) (*http.Client, string, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, "", fmt.Errorf("google cloud chirp_3 requires Application Default Credentials: %w", err)
	}

	projectID := strings.TrimSpace(creds.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	}
	if projectID == "" {
		return nil, "", fmt.Errorf("google cloud chirp_3 requires GOOGLE_CLOUD_PROJECT or a credential source with project id")
	}

	client := oauth2.NewClient(ctx, creds.TokenSource)
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	return client, projectID, nil
}

func newGoogleSpeechClient(ctx context.Context) (*speech.Client, string, string, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, "", "", fmt.Errorf("google cloud chirp_3 requires Application Default Credentials: %w", err)
	}

	projectID := strings.TrimSpace(creds.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	}
	if projectID == "" {
		return nil, "", "", fmt.Errorf("google cloud chirp_3 requires GOOGLE_CLOUD_PROJECT or a credential source with project id")
	}

	location := googleChirp3Location()
	client, err := speech.NewClient(
		ctx,
		option.WithTokenSource(creds.TokenSource),
		option.WithQuotaProject(projectID),
		option.WithEndpoint(googleSpeechGRPCEndpoint(location)),
	)
	if err != nil {
		return nil, "", "", fmt.Errorf("create google speech client: %w", err)
	}

	return client, projectID, location, nil
}

func (s *googleStreamAdapter) sendAudioV2Locked(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	if err := s.ensureV2StreamLocked(); err != nil {
		return err
	}

	for start := 0; start < len(pcm); start += 14000 {
		end := start + 14000
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := s.stream.Send(&speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_Audio{Audio: pcm[start:end]},
		}); err != nil {
			s.resetV2StreamLocked(false)
			return fmt.Errorf("google chirp_3 send audio: %w", err)
		}
	}

	return nil
}

func (s *googleStreamAdapter) ensureV2StreamLocked() error {
	if s.stream != nil {
		return nil
	}
	if s.speechClient == nil {
		return fmt.Errorf("google chirp_3 client is not initialized")
	}

	streamCtx, cancel := context.WithCancel(s.ctx)
	stream, err := s.speechClient.StreamingRecognize(streamCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("google chirp_3 open streaming recognize: %w", err)
	}

	features := &speechpb.RecognitionFeatures{
		EnableAutomaticPunctuation: true,
		MaxAlternatives:            1,
	}
	if s.enableDiarization {
		features.DiarizationConfig = &speechpb.SpeakerDiarizationConfig{}
	}

	config := &speechpb.RecognitionConfig{
		DecodingConfig: &speechpb.RecognitionConfig_ExplicitDecodingConfig{
			ExplicitDecodingConfig: &speechpb.ExplicitDecodingConfig{
				Encoding:          speechpb.ExplicitDecodingConfig_LINEAR16,
				SampleRateHertz:   int32(s.sampleRate),
				AudioChannelCount: int32(s.channels),
			},
		},
		LanguageCodes: googleChirp3LanguageCodes(),
		Model:         s.model,
		Features:      features,
	}

	configRequest := &speechpb.StreamingRecognizeRequest{
		Recognizer: googleRecognizerPath(s.projectID, s.location),
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: config,
				StreamingFeatures: &speechpb.StreamingRecognitionFeatures{
					InterimResults:            s.interimResults,
					EnableVoiceActivityEvents: true,
				},
			},
		},
	}
	if err := stream.Send(configRequest); err != nil {
		_ = stream.CloseSend()
		cancel()
		return fmt.Errorf("google chirp_3 send config: %w", err)
	}

	done := make(chan error, 1)
	s.stream = stream
	s.streamCancel = cancel
	s.streamDone = done
	go s.readV2Responses(stream, done)
	return nil
}

func (s *googleStreamAdapter) finalizeV2() error {
	s.mu.Lock()
	stream := s.stream
	done := s.streamDone
	cancel := s.streamCancel
	s.resetV2StreamLocked(true)
	s.mu.Unlock()

	if stream == nil {
		return nil
	}
	if err := stream.CloseSend(); err != nil && !errors.Is(err, io.EOF) {
		if cancel != nil {
			cancel()
		}
		return fmt.Errorf("google chirp_3 finalize close send: %w", err)
	}

	if done == nil {
		if cancel != nil {
			cancel()
		}
		return nil
	}

	select {
	case err := <-done:
		if cancel != nil {
			cancel()
		}
		if err != nil {
			return err
		}
	case <-time.After(1500 * time.Millisecond):
		if cancel != nil {
			cancel()
		}
	}
	return nil
}

func (s *googleStreamAdapter) resetV2StreamLocked(keepDone bool) {
	s.stream = nil
	if !keepDone {
		s.streamDone = nil
	}
	s.streamCancel = nil
}

func (s *googleStreamAdapter) readV2Responses(stream speechpb.Speech_StreamingRecognizeClient, done chan<- error) {
	var finalErr error
	defer func() {
		done <- finalErr
		close(done)
	}()

	for {
		resp, err := stream.Recv()
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				finalErr = nil
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				finalErr = nil
			default:
				finalErr = fmt.Errorf("google chirp_3 receive: %w", err)
			}
			return
		}

		s.emitV2SpeechEvent(resp.GetSpeechEventType())
		s.emitV2Results(resp.GetResults())
	}
}

func (s *googleStreamAdapter) emitV2SpeechEvent(eventType speechpb.StreamingRecognizeResponse_SpeechEventType) {
	switch eventType {
	case speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_BEGIN:
		s.emitEvent(TranscriptEvent{SpeechStart: true})
	case speechpb.StreamingRecognizeResponse_SPEECH_ACTIVITY_END, speechpb.StreamingRecognizeResponse_END_OF_SINGLE_UTTERANCE:
		s.emitEvent(TranscriptEvent{SpeechEnd: true})
	}
}

func (s *googleStreamAdapter) emitV2Results(results []*speechpb.StreamingRecognitionResult) {
	for _, result := range results {
		if result == nil || len(result.Alternatives) == 0 || result.Alternatives[0] == nil {
			continue
		}

		alt := result.Alternatives[0]
		text := strings.TrimSpace(alt.Transcript)
		if text == "" {
			continue
		}

		duration := 0.0
		if offset := result.GetResultEndOffset(); offset != nil {
			duration = offset.AsDuration().Seconds()
		}

		s.emitEvent(TranscriptEvent{
			Text:       text,
			IsFinal:    result.GetIsFinal(),
			SpeechEnd:  result.GetIsFinal(),
			Confidence: float64(alt.GetConfidence()),
			Language:   result.GetLanguageCode(),
			Duration:   duration,
		})
	}
}

func (s *googleStreamAdapter) emitEvent(event TranscriptEvent) {
	s.mu.Lock()
	closed := s.closed
	ch := s.resultChan
	s.mu.Unlock()

	if closed {
		return
	}

	defer func() {
		_ = recover()
	}()

	select {
	case ch <- event:
	default:
	}
}
