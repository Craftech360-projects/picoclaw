package deepgram

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const defaultDeepgramURL = "wss://api.deepgram.com/v1/listen"

// DeepgramTranscriber opens streaming transcription sessions against Deepgram.
type DeepgramTranscriber struct {
	apiKey  string
	baseURL string
	dialer  *websocket.Dialer
}

// NewDeepgramTranscriber creates a Deepgram streaming transcriber.
func NewDeepgramTranscriber(apiKey string) *DeepgramTranscriber {
	return &DeepgramTranscriber{
		apiKey:  apiKey,
		baseURL: defaultDeepgramURL,
		dialer:  websocket.DefaultDialer,
	}
}

// OpenStream starts a streaming transcription session.
func (d *DeepgramTranscriber) OpenStream(opts StreamOpts) (TranscriptionStream, error) {
	if d == nil {
		return nil, errors.New("deepgram transcriber is nil")
	}
	if d.apiKey == "" {
		return nil, errors.New("deepgram api key is empty")
	}
	cfg := normalizeStreamOpts(opts)

	u, err := url.Parse(d.baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("encoding", cfg.Encoding)
	q.Set("sample_rate", intToString(cfg.SampleRate))
	q.Set("channels", intToString(cfg.Channels))
	q.Set("interim_results", boolToString(cfg.InterimResults))
	q.Set("punctuate", boolToString(cfg.Punctuate))
	q.Set("smart_format", boolToString(cfg.SmartFormat))
	if cfg.Model != "" {
		q.Set("model", cfg.Model)
	}
	if cfg.Language != "" {
		q.Set("language", cfg.Language)
	}
	if cfg.EndpointingMS > 0 {
		q.Set("endpointing", intToString(cfg.EndpointingMS))
	}
	u.RawQuery = q.Encode()

	headers := map[string][]string{
		"Authorization": {"Token " + d.apiKey},
	}

	conn, _, err := d.dialer.Dial(u.String(), headers)
	if err != nil {
		return nil, err
	}

	stream := &deepgramStream{
		conn:    conn,
		results: make(chan TranscriptEvent, 32),
		closed:  make(chan struct{}),
	}

	go stream.readLoop()
	return stream, nil
}

type deepgramStream struct {
	conn    *websocket.Conn
	results chan TranscriptEvent
	closed  chan struct{}
	mu      sync.Mutex
	once    sync.Once

	speaking bool
}

func (s *deepgramStream) Results() <-chan TranscriptEvent {
	return s.results
}

func (s *deepgramStream) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return errors.New("deepgram stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (s *deepgramStream) Close() error {
	var err error
	s.once.Do(func() {
		close(s.closed)
		s.mu.Lock()
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		err = s.conn.Close()
		s.mu.Unlock()
		close(s.results)
	})
	return err
}

func (s *deepgramStream) readLoop() {
	defer func() {
		_ = s.Close()
	}()

	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}

		var resp deepgramResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}

		text := ""
		if len(resp.Channel.Alternatives) > 0 {
			text = strings.TrimSpace(resp.Channel.Alternatives[0].Transcript)
		}

		if text == "" && !resp.SpeechFinal {
			continue
		}

		evt := TranscriptEvent{
			Text:    text,
			IsFinal: resp.IsFinal,
		}

		if text != "" && !s.speaking {
			evt.SpeechStart = true
			s.speaking = true
		}

		if resp.SpeechFinal || (resp.IsFinal && text != "") {
			// If we get SpeechFinal, OR if we get an IsFinal message with text (which Deepgram sometimes
			// sends instead of or before SpeechFinal depending on endpointing/VAD), we should treat
			// it as the end of a speech segment.
			evt.SpeechEnd = true
			s.speaking = false
		}

		select {
		case s.results <- evt:
		case <-s.closed:
			return
		}
	}
}

func normalizeStreamOpts(opts StreamOpts) StreamOpts {
	if opts.SampleRate == 0 {
		opts.SampleRate = 16000
	}
	if opts.Encoding == "" {
		opts.Encoding = "linear16"
	}
	if opts.Channels == 0 {
		opts.Channels = 1
	}
	if !opts.InterimResults {
		// default to true for streaming
		opts.InterimResults = true
	}
	if !opts.Punctuate {
		opts.Punctuate = true
	}
	if !opts.SmartFormat {
		opts.SmartFormat = true
	}
	if opts.EndpointingMS == 0 {
		// Deepgram's endpointing parameter determines how much silence to wait for
		// before sending a SpeechFinal event. A default of 300ms is usually too fast
		// and can lead to cut-off sentences. 500ms or more is generally better for natural pauses.
		opts.EndpointingMS = 500
	}
	return opts
}

func intToString(v int) string {
	return strconv.Itoa(v)
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

var _ TranscriptionStream = (*deepgramStream)(nil)

var _ StreamingTranscriber = (*DeepgramTranscriber)(nil)
