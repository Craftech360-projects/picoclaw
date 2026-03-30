package vad

// VAD is the interface for Voice Activity Detection engines
type VAD interface {
	// Process processes a frame of PCM audio and returns the probability of voice [0, 1]
	// The audio data must be 16-bit linearly-encoded PCM, single-channel.
	// The length of the frame must match FrameLength().
	Process(pcm []int16) (float32, error)

	// SampleRate returns the required sample rate for the engine (e.g. 16000)
	SampleRate() int

	// FrameLength returns the required number of samples per input frame (e.g. 512)
	FrameLength() int

	// Close releases any resources held by the VAD engine
	Close() error
}
