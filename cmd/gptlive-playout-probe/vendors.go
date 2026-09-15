package main

import (
	"context"
	"log"
	"time"

	"github.com/sipeed/picoclaw/pkg/geminilive"
	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/grokvoice"
)

// voiceSession is what the probe needs from a realtime voice vendor.
type voiceSession interface {
	PushAudio(pcm []byte)
	Audio() <-chan []byte
	Interrupted() <-chan struct{}
	Greet(text string)
	Close()
}

const outRate = 24000 // all three vendors speak 24kHz PCM16

type vendorSpec struct {
	inRate       int
	defaultVoice string
	defaultModel string
	envKey       string
}

var vendors = map[string]vendorSpec{
	"openai": {24000, "marin", gptlive.DefaultModel, "OPENAI_API_KEY"},
	"xai":    {grokvoice.SampleRate, grokvoice.DefaultVoice, grokvoice.DefaultModel, "XAI_API_KEY"},
	"google": {geminilive.InputSampleRate, geminilive.DefaultVoice, geminilive.DefaultModel, "GOOGLE_API_KEY"},
}

type dialOptions struct {
	key, model, voice, instructions string
	silence                         time.Duration
}

// realtime is the method set every vendor package's session shares.
type realtime interface {
	PushAudio(pcm []byte)
	Audio() <-chan []byte
	Events() <-chan gptlive.Event
	AppendCommentary(text string)
	Close(ctx context.Context) error
}

type session struct {
	realtime
	interrupted chan struct{}
}

func (s session) Interrupted() <-chan struct{} { return s.interrupted }
func (s session) Greet(text string)            { s.AppendCommentary(text) }
func (s session) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.realtime.Close(ctx)
}

func dialVendor(ctx context.Context, vendor string, o dialOptions) (voiceSession, error) {
	// each branch assigns r only after checking the error, so r never holds a typed nil pointer
	var r realtime
	switch vendor {
	case "xai":
		g, err := grokvoice.Dial(ctx, grokvoice.Config{APIKey: o.key, Model: o.model, Voice: o.voice, Instructions: o.instructions, Silence: o.silence})
		if err != nil {
			return nil, err
		}
		r = g
	case "google":
		g, err := geminilive.Dial(ctx, geminilive.Config{APIKey: o.key, Model: o.model, Voice: o.voice, Instructions: o.instructions, Silence: o.silence})
		if err != nil {
			return nil, err
		}
		r = g
	default:
		g, err := gptlive.Dial(ctx, gptlive.Config{APIKey: o.key, Model: o.model, Voice: o.voice, SampleRate: outRate, Instructions: o.instructions})
		if err != nil {
			return nil, err
		}
		r = g
	}
	s := session{realtime: r, interrupted: make(chan struct{}, 1)}
	go func() {
		for ev := range r.Events() {
			switch e := ev.(type) {
			case gptlive.Interrupted:
				select {
				case s.interrupted <- struct{}{}:
				default:
				}
			case gptlive.Error:
				log.Printf("%s error (recoverable=%v): %v", vendor, e.Recoverable, e.Err)
			}
		}
	}()
	return s, nil
}
