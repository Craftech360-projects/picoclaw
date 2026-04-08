package deepgram

// StreamOpts configures a Deepgram streaming transcription session.
type StreamOpts struct {
	SampleRate     int
	Encoding       string
	Channels       int
	Model          string
	Language       string
	InterimResults bool
	Punctuate      bool
	SmartFormat    bool
	EndpointingMS  int
}

// TranscriptEvent represents a transcription update from Deepgram.
type TranscriptEvent struct {
	Text        string
	IsFinal     bool
	SpeechStart bool
	SpeechEnd   bool
}

// TranscriptionStream provides streaming transcription results.
type TranscriptionStream interface {
	Results() <-chan TranscriptEvent
	SendAudio(pcm []byte) error
	Finalize() error
	Close() error
}

// StreamingTranscriber opens streaming transcription sessions.
type StreamingTranscriber interface {
	OpenStream(opts StreamOpts) (TranscriptionStream, error)
}

type deepgramResponse struct {
	Type         string  `json:"type,omitempty"`
	IsFinal      bool    `json:"is_final"`
	SpeechFinal  bool    `json:"speech_final"`
	FromFinalize bool    `json:"from_finalize,omitempty"`
	LastWordEnd  float64 `json:"last_word_end,omitempty"`
	Channel      struct {
		Alternatives []struct {
			Transcript string `json:"transcript"`
		} `json:"alternatives"`
	} `json:"channel"`
}
