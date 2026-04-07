package stt

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// awsProvider implements STT using AWS Transcribe
type awsProvider struct {
	accessKey string
	secretKey string
	region    string
	model     string
	language  string
}

// NewAWSProvider creates a new AWS Transcribe provider
func NewAWSProvider(accessKey, secretKey, region, model, language string) Provider {
	if model == "" {
		model = "Conversational"
	}
	return &awsProvider{
		accessKey: accessKey,
		secretKey: secretKey,
		region:    region,
		model:     model,
		language:  language,
	}
}

func (p *awsProvider) Name() string { return "aws" }

func (p *awsProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "it", "pt", "auto"},
		Models:               []string{"Conversational", "Voicemail", "PhoneCall"},
		SupportsStreaming:    false,
		SupportsDiarization:  true,
		SupportsMultilingual: false,
	}
}

func (p *awsProvider) WithConfig(apiKey, model string) Provider {
	// For AWS, apiKey could be formatted as "accessKey:secretKey"
	return NewAWSProvider("", "", "", model, "")
}

func (p *awsProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	accessKey := p.accessKey
	secretKey := p.secretKey
	region := p.region

	if accessKey == "" {
		accessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if secretKey == "" {
		secretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if region == "" {
		region = os.Getenv("AWS_REGION")
		if region == "" {
			region = "us-east-1" // Default region
		}
	}

	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("aws transcribe: credentials not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	language := opts.Language
	if language == "" && p.language != "" {
		language = p.language
	}

	stream := &awsStreamAdapter{
		accessKey:   accessKey,
		secretKey:   secretKey,
		region:      region,
		model:       model,
		language:    language,
		sampleRate:  opts.SampleRate,
		audioBuffer: make([]byte, 0),
		resultChan:  make(chan TranscriptEvent, 10),
		ctx:         ctx,
		mu:          sync.Mutex{},
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}

	return stream, nil
}

// awsStreamAdapter handles AWS Transcribe API calls
type awsStreamAdapter struct {
	accessKey   string
	secretKey   string
	region      string
	model       string
	language    string
	sampleRate  int
	audioBuffer []byte
	resultChan  chan TranscriptEvent
	ctx         context.Context
	mu          sync.Mutex
	closed      bool
	httpClient  *http.Client
}

func (s *awsStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}

	s.audioBuffer = append(s.audioBuffer, pcm...)

	// AWS Transcribe has minimum audio requirements
	// Transcribe in chunks of ~1 second
	bytesPerSecond := s.sampleRate * 2
	if len(s.audioBuffer) >= bytesPerSecond {
		return s.transcribeBuffer()
	}

	return nil
}

func (s *awsStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *awsStreamAdapter) Finalize() error {
	return s.transcribeBuffer()
}

func (s *awsStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *awsStreamAdapter) transcribeBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// AWS Transcribe Streaming API requires signing
	// For simplicity, we'll use the batch API with presigned URL approach
	// In production, consider using AWS SDK for Go

	// Create WAV file
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create WAV: %w", err)
	}

	// Build the request URL
	url := fmt.Sprintf("https://transcribe.%s.amazonaws.com/transcribe", s.region)

	// Prepare headers
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	dateStamp := timestamp[:8]
	serviceName := "transcribe"
	algorithm := "AWS4-HMAC-SHA256"

	// Canonical request
	canonicalURI := "/transcribe"
	canonicalQueryString := ""
	canonicalHeaders := fmt.Sprintf("content-type:audio/wav\nhost:transcribe.%s.amazonaws.com\nx-amz-date:%s\n",
		s.region, timestamp)
	signedHeaders := "content-type;host;x-amz-date"

	// Hash the payload
	payloadHash := sha256.Sum256(wavData)
	payloadHashHex := hex.EncodeToString(payloadHash[:])

	_ = fmt.Sprintf("POST\n%s\n%s\n%s\n%s\n%s",
		canonicalURI, canonicalQueryString, canonicalHeaders, signedHeaders, payloadHashHex)

	// Create signature
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, s.region, serviceName)
	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s",
		algorithm, timestamp, credentialScope,
		hex.EncodeToString(hmacSHA256([]byte(dateStamp), []byte("AWS4"))))

	signingKey := hmacSHA256([]byte("transcribe"), []byte(s.secretKey))
	signature := hmacSHA256(signingKey, []byte(stringToSign))

	signatureHex := hex.EncodeToString(signature)
	authorizationHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, s.accessKey, credentialScope, signedHeaders, signatureHex)

	// Create HTTP request
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(wavData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("X-Amz-Date", timestamp)
	req.Header.Set("Authorization", authorizationHeader)
	req.Header.Set("x-amz-target", "Transcribe.StartStreamTranscription")

	// Add optional headers for language and model
	if s.language != "" && s.language != "auto" {
		req.Header.Set("x-amz-language-code", s.language)
	}
	req.Header.Set("x-amz-media-encoding", "pcm")
	req.Header.Set("x-amz-sample-rate", fmt.Sprintf("%d", s.sampleRate))
	req.Header.Set("x-amz-model", s.model)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transcription failed: %s - %s", resp.Status, string(body))
	}

	// Parse response (AWS Transcribe streaming returns events)
	// For simplicity, reading as JSON - production would handle streaming events
	var result struct {
		Transcript struct {
			Results []struct {
				Alternatives []struct {
					Transcript string `json:"transcript"`
				} `json:"alternatives"`
				IsPartial bool `json:"is_partial"`
			} `json:"results"`
		} `json:"transcript"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// If not JSON, it might be streaming format - just log
		return nil
	}

	// Extract transcript
	if len(result.Transcript.Results) > 0 {
		alt := result.Transcript.Results[0].Alternatives[0]
		if alt.Transcript != "" {
			event := TranscriptEvent{
				Text:     alt.Transcript,
				IsFinal:  !result.Transcript.Results[0].IsPartial,
				Duration: s.calculateDuration(),
			}

			select {
			case s.resultChan <- event:
			default:
			}
		}
	}

	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *awsStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}

func hmacSHA256(key []byte, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
