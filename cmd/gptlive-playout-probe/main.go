// Command gptlive-playout-probe checks GPT-Live output playout in isolation. It joins
// one room, talks through the same pkg/gptlive session the agent uses (no tools,
// persona or persistence), plays the model through the same elastic playout as the
// agent, and logs level, underruns and arrival rate every 5s.
// See docs/gptlive-audio-jitter.md.
//
//	LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET, OPENAI_API_KEY must be set.
//	gptlive-playout-probe -room probe-1 -duration 90s
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"log"
	neturl "net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/livekit/media-sdk"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/pion/webrtc/v4"

	"github.com/sipeed/picoclaw/pkg/gptlive"
)

const (
	trackFrame = 20 * time.Millisecond // PCMLocalTrack takes one frame per tick, zero-padding when short
	statsEvery = 5 * time.Second
)

var yes = true

func main() {
	room := flag.String("room", "gptlive-probe", "LiveKit room to join")
	rate := flag.Int("rate", 24000, "model sample rate (the agent always uses 24000)")
	target := flag.Duration("target", 200*time.Millisecond, "playout lead to hold")
	runFor := flag.Duration("duration", 0, "exit after this long (0: run until Ctrl+C)")
	flag.Parse()
	env := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Fatalf("%s is not set", k)
		}
		return v
	}
	url, key, secret, openaiKey := env("LIVEKIT_URL"), env("LIVEKIT_API_KEY"), env("LIVEKIT_API_SECRET"), env("OPENAI_API_KEY")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *runFor > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *runFor)
		defer cancel()
	}

	sess, err := gptlive.Dial(ctx, gptlive.Config{
		APIKey: openaiKey, SampleRate: *rate,
		Instructions: "You are a warm storyteller. Speak English. When asked for a story, tell it for about a minute without pausing for the listener.",
	})
	if err != nil {
		log.Fatalf("gptlive dial: %v", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = sess.Close(c)
		cancel()
	}()
	go func() {
		for ev := range sess.Events() {
			if e, ok := ev.(gptlive.Error); ok {
				log.Printf("gptlive error (recoverable=%v): %v", e.Recoverable, e.Err)
			}
		}
	}()

	track, err := lkmedia.NewPCMLocalTrack(*rate, 1, protoLogger.GetLogger())
	if err != nil {
		log.Fatalf("local track: %v", err)
	}

	// GPT-Live is clocked by input audio: with no mic frames it neither injects
	// context nor speaks. Feed real-time silence until a real mic takes over.
	micWired := make(chan struct{})
	var micOnce sync.Once
	go func() {
		t := time.NewTicker(trackFrame)
		defer t.Stop()
		silence := make([]byte, *rate/50*2)
		for {
			select {
			case <-ctx.Done():
				return
			case <-micWired:
				return
			case <-t.C:
				sess.PushAudio(silence)
			}
		}
	}()

	listening := make(chan struct{})
	var once sync.Once
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(t *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if t.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		if _, err := lkmedia.NewPCMRemoteTrack(t, micWriter{sess}, lkmedia.WithTargetSampleRate(*rate), lkmedia.WithTargetChannels(1)); err != nil {
			log.Printf("remote track: %v", err)
			return
		}
		log.Printf("mic from %s wired to the model", rp.Identity())
		micOnce.Do(func() { close(micWired) })
		once.Do(func() { close(listening) })
	}
	cb.OnParticipantConnected = func(rp *lksdk.RemoteParticipant) { // headless listeners (lk room join) have no mic
		log.Printf("%s joined", rp.Identity())
		once.Do(func() { close(listening) })
	}

	at := auth.NewAccessToken(key, secret)
	at.SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: *room})
	at.SetIdentity("gptlive-probe")
	at.SetKind(livekit.ParticipantInfo_AGENT) // agent-starter-react waits for an agent participant
	token, err := at.ToJWT()
	if err != nil {
		log.Fatalf("token: %v", err)
	}
	lkRoom, err := lksdk.ConnectToRoomWithToken(url, token, cb)
	if err != nil {
		log.Fatalf("join room: %v", err)
	}
	defer lkRoom.Disconnect()
	if len(lkRoom.GetRemoteParticipants()) > 0 {
		once.Do(func() { close(listening) })
	}
	if _, err := lkRoom.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "gptlive-probe"}); err != nil {
		log.Fatalf("publish: %v", err)
	}
	log.Printf("joined %s, rate=%d target=%s; waiting for a listener", *room, *rate, *target)
	lt := auth.NewAccessToken(key, secret)
	lt.SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: *room, CanPublish: &yes, CanSubscribe: &yes})
	lt.SetIdentity("listener")
	lt.SetValidFor(time.Hour)
	if listenerToken, err := lt.ToJWT(); err == nil {
		// local dev only: the link carries a one-hour join token for this room
		log.Printf("listen: https://meet.livekit.io/custom?liveKitUrl=%s&token=%s", neturl.QueryEscape(url), listenerToken)
	}

	go func() {
		select {
		case <-listening:
			sess.AppendCommentary("Greet the listener in one sentence, then tell a story about a brave little robot for about a minute without stopping.")
		case <-ctx.Done():
		}
	}()
	play(ctx, sess.Audio(), track, *rate, *target)
}

type micWriter struct{ sess *gptlive.Session }

func (w micWriter) WriteSample(s media.PCM16Sample) error {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	w.sess.PushAudio(b)
	return nil
}
func (w micWriter) Close() error { return nil }

func toPCM16(b []byte) media.PCM16Sample {
	out := make(media.PCM16Sample, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// play drains model audio into the track with elastic playout. level mirrors
// PCMLocalTrack's queue (grows on every write, shrinks by wall time), and speech
// that hits an empty queue is what reaches the listener as a gap or click.
func play(ctx context.Context, audio <-chan []byte, track *lkmedia.PCMLocalTrack, rate int, target time.Duration) {
	dur := func(n int) time.Duration { return time.Duration(n/2) * time.Second / time.Duration(rate) }
	frameBytes := rate / 50 * 2
	seg := gptlive.NewSegmenter(func() {}, func() {})

	var level time.Duration
	var levelAt time.Time
	starved := true
	var underruns int
	var underrunDur, maxTickGap, arrived, padded, dropped time.Duration
	windowStart := time.Now()

	drain := func(now time.Time) {
		if !levelAt.IsZero() {
			level -= now.Sub(levelAt)
		}
		levelAt = now
		if level >= 0 {
			return
		}
		if seg.Open() {
			underrunDur -= level
			if !starved {
				underruns++
			}
		}
		starved = true
		level = 0
	}
	write := func(pcm []byte) {
		if len(pcm) == 0 {
			return
		}
		level += dur(len(pcm))
		starved = false
		if err := track.WriteSample(toPCM16(pcm)); err != nil {
			log.Printf("track write: %v", err)
		}
	}

	var carry []byte

	tick20 := time.NewTicker(trackFrame)
	defer tick20.Stop()
	tick100 := time.NewTicker(100 * time.Millisecond)
	defer tick100.Stop()
	lastTick20 := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case pcm, ok := <-audio:
			if !ok {
				log.Printf("model audio closed")
				return
			}
			now := time.Now()
			drain(now)
			arrived += dur(len(pcm))
			seg.Feed(pcm, dur(len(pcm)))
			carry = append(carry, pcm...)
			n := len(carry) / frameBytes * frameBytes
			if n == 0 {
				continue
			}
			out := carry[:n]
			if !seg.Open() { // model silence: the only place latency is corrected
				switch {
				case level > 2*target:
					dropped += dur(n)
					out = nil
				case level < target:
					pad := int((target-level)/trackFrame) * frameBytes
					padded += dur(pad)
					out = append(make([]byte, pad, pad+n), out...)
				}
			}
			write(out) // toPCM16 copies, so reusing carry below is safe
			carry = append(carry[:0], carry[n:]...)
		case now := <-tick20.C:
			if gap := now.Sub(lastTick20); gap > maxTickGap {
				maxTickGap = gap
			}
			lastTick20 = now
			drain(now)
			if elapsed := now.Sub(windowStart); elapsed >= statsEvery {
				log.Printf("level=%dms underruns=%d underrun=%dms arrival_ratio=%.3f max_tick_gap=%dms padded=%dms dropped=%dms",
					level.Milliseconds(), underruns, underrunDur.Milliseconds(), arrived.Seconds()/elapsed.Seconds(),
					maxTickGap.Milliseconds(), padded.Milliseconds(), dropped.Milliseconds())
				underruns, underrunDur, maxTickGap, arrived, padded, dropped = 0, 0, 0, 0, 0, 0
				windowStart = now
			}
		case now := <-tick100.C:
			seg.Tick(now)
		}
	}
}
