// Command gptlive-playout-probe is the minimal Go realtime voice agent: it joins one LiveKit
// room and talks through OpenAI GPT-Live, xAI Grok Voice or Google Gemini Live (no tools,
// persona or persistence). The mic goes to the model as steady 100ms chunks and the model is
// played through the same elastic playout as picoclaw-livekit, with level, underruns and
// arrival rate logged every 5s. See docs/gptlive-audio-jitter.md.
//
//	LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET must be set, plus the vendor key:
//	OPENAI_API_KEY (openai), XAI_API_KEY (xai) or GOOGLE_API_KEY (google).
//	gptlive-playout-probe -vendor openai -room probe-1 -duration 90s
//	gptlive-playout-probe -vendor xai -voice eve
//	gptlive-playout-probe -vendor google -voice Kore -model gemini-3.1-flash-live-preview
//	gptlive-playout-probe -vendor google -agent cheeko-gemini-go   # join every room dispatched to that name
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
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

type config struct {
	vendor           string
	spec             vendorSpec
	dial             dialOptions
	greeting         string
	target           time.Duration
	url, key, secret string
}

func main() {
	vendor := flag.String("vendor", "openai", "realtime vendor: openai | xai | google")
	room := flag.String("room", "gptlive-probe", "LiveKit room to join (ignored with -agent)")
	agent := flag.String("agent", "", "act as a dispatchable agent: join every room dispatched to this name (e.g. from the admin dashboard)")
	voice := flag.String("voice", "", "vendor voice (default: the vendor's default)")
	model := flag.String("model", "", "vendor model (default: the vendor's default)")
	silence := flag.Duration("silence", 700*time.Millisecond, "end-of-turn silence for server VAD (xai, google); children pause mid-sentence")
	prompt := flag.String("prompt", "You are Cheeko, a warm, playful friend for young children. Speak English in short, simple sentences. Keep every reply brief and ask one question at a time.", "system instructions")
	greeting := flag.String("greeting", "Greet the child in one short, cheerful sentence and ask what they would like to talk about.", "what the model is asked to say first")
	target := flag.Duration("target", 200*time.Millisecond, "playout lead to hold")
	runFor := flag.Duration("duration", 0, "exit after this long (0: run until Ctrl+C)")
	flag.Parse()
	spec, ok := vendors[*vendor]
	if !ok {
		log.Fatalf("-vendor must be openai, xai or google, got %q", *vendor)
	}
	if *voice == "" {
		*voice = spec.defaultVoice
	}
	if *model == "" {
		*model = spec.defaultModel
	}
	env := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Fatalf("%s is not set", k)
		}
		return v
	}
	cfg := config{
		vendor: *vendor, spec: spec, greeting: *greeting, target: *target,
		dial: dialOptions{key: env(spec.envKey), model: *model, voice: *voice, silence: *silence, instructions: *prompt},
		url:  env("LIVEKIT_URL"), key: env("LIVEKIT_API_KEY"), secret: env("LIVEKIT_API_SECRET"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *runFor > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *runFor)
		defer cancel()
	}
	if *agent == "" {
		if err := runSession(ctx, cfg, *room); err != nil {
			log.Fatal(err)
		}
		return
	}
	serveDispatches(ctx, cfg, *agent)
}

// serveDispatches polls LiveKit for rooms dispatched to agent and runs a session in each.
// ponytail: polling instead of the agent worker protocol; a few seconds of join delay is fine
// for local testing, and picoclaw-livekit's worker is the real thing.
func serveDispatches(ctx context.Context, cfg config, agent string) {
	rooms := lksdk.NewRoomServiceClient(cfg.url, cfg.key, cfg.secret)
	dispatches := lksdk.NewAgentDispatchServiceClient(cfg.url, cfg.key, cfg.secret)
	seen := map[string]bool{}
	log.Printf("waiting for rooms dispatched to %q (vendor=%s model=%s voice=%s)", agent, cfg.vendor, cfg.dial.model, cfg.dial.voice)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		list, err := rooms.ListRooms(ctx, &livekit.ListRoomsRequest{})
		if err != nil {
			log.Printf("list rooms: %v", err)
			continue
		}
		for _, r := range list.Rooms {
			if seen[r.Name] {
				continue
			}
			ds, err := dispatches.ListDispatch(ctx, &livekit.ListAgentDispatchRequest{Room: r.Name})
			if err != nil {
				continue // retried next tick
			}
			seen[r.Name] = true
			for _, d := range ds.AgentDispatches {
				if d.AgentName == agent {
					go func(room string) {
						if err := runSession(ctx, cfg, room); err != nil {
							log.Printf("room %s: %v", room, err)
						}
					}(r.Name)
					break
				}
			}
		}
	}
}

// runSession joins room, talks through the vendor, and returns when ctx ends or everyone else leaves.
func runSession(parent context.Context, cfg config, room string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	spec := cfg.spec

	sess, err := dialVendor(ctx, cfg.vendor, cfg.dial)
	if err != nil {
		return fmt.Errorf("dial %s: %w", cfg.vendor, err)
	}
	defer sess.Close()

	track, err := lkmedia.NewPCMLocalTrack(outRate, 1, protoLogger.GetLogger())
	if err != nil {
		return fmt.Errorf("local track: %w", err)
	}

	mic := &micBuffer{limit: spec.inRate} // 500ms of PCM16 mono
	go mic.pump(ctx, sess, spec.inRate)

	listening := make(chan struct{})
	var once sync.Once
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(t *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if t.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		if _, err := lkmedia.NewPCMRemoteTrack(t, mic, lkmedia.WithTargetSampleRate(spec.inRate), lkmedia.WithTargetChannels(1)); err != nil {
			log.Printf("remote track: %v", err)
			return
		}
		log.Printf("mic from %s wired to the model", rp.Identity())
		once.Do(func() { close(listening) })
	}
	cb.OnParticipantConnected = func(rp *lksdk.RemoteParticipant) { // headless listeners (lk room join) have no mic
		log.Printf("%s joined", rp.Identity())
		once.Do(func() { close(listening) })
	}
	var lkRoom *lksdk.Room
	cb.OnParticipantDisconnected = func(rp *lksdk.RemoteParticipant) {
		log.Printf("%s left %s", rp.Identity(), room)
		if lkRoom != nil && len(lkRoom.GetRemoteParticipants()) == 0 {
			cancel() // the session is over once nobody is left to talk to
		}
	}
	cb.OnDisconnected = cancel

	at := auth.NewAccessToken(cfg.key, cfg.secret)
	at.SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: room})
	at.SetIdentity("gptlive-probe")
	at.SetKind(livekit.ParticipantInfo_AGENT) // the dashboard and agent-starter-react wait for an agent participant
	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	lkRoom, err = lksdk.ConnectToRoomWithToken(cfg.url, token, cb)
	if err != nil {
		return fmt.Errorf("join room %s: %w", room, err)
	}
	defer lkRoom.Disconnect()
	if len(lkRoom.GetRemoteParticipants()) > 0 {
		once.Do(func() { close(listening) })
	}
	if _, err := lkRoom.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "gptlive-probe"}); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	log.Printf("joined %s, vendor=%s model=%s voice=%s in=%dHz target=%s; waiting for a listener",
		room, cfg.vendor, cfg.dial.model, cfg.dial.voice, spec.inRate, cfg.target)
	lt := auth.NewAccessToken(cfg.key, cfg.secret)
	lt.SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: room, CanPublish: &yes, CanSubscribe: &yes})
	lt.SetIdentity("listener")
	lt.SetValidFor(time.Hour)
	if listenerToken, err := lt.ToJWT(); err == nil {
		// local dev only: the link carries a one-hour join token for this room
		log.Printf("listen: https://meet.livekit.io/custom?liveKitUrl=%s&token=%s", neturl.QueryEscape(cfg.url), listenerToken)
	}

	go func() {
		select {
		case <-listening:
			sess.Greet(cfg.greeting)
		case <-ctx.Done():
		}
	}()
	play(ctx, sess.Audio(), sess.Interrupted(), track, cfg.target)
	log.Printf("session in %s ended", room)
	return nil
}

// micBuffer collects room mic audio (lkmedia.PCMRemoteTrackWriter) for pump.
type micBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int // bytes; the oldest audio is dropped past it so a stall can't grow input latency
}

func (m *micBuffer) WriteSample(s media.PCM16Sample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range s {
		m.buf = binary.LittleEndian.AppendUint16(m.buf, uint16(v))
	}
	if len(m.buf) > m.limit {
		m.buf = append(m.buf[:0], m.buf[len(m.buf)-m.limit:]...)
	}
	return nil
}
func (m *micBuffer) Close() error { return nil }

// pump sends the mic as 100ms chunks on a steady clock, silence when the mic falls short
// (browsers send nothing during silence). Same shape as picoclaw-livekit's pumpMicIn: it
// waits for 200ms of mic before taking it, and rebuilds that slack whenever it runs dry.
func (m *micBuffer) pump(ctx context.Context, sess voiceSession, rate int) {
	chunk := rate / 10 * 2
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	primed := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		out := make([]byte, chunk)
		m.mu.Lock()
		if !primed && len(m.buf) >= 2*chunk {
			primed = true
		}
		if primed {
			n := copy(out, m.buf)
			m.buf = append(m.buf[:0], m.buf[n:]...)
			primed = n == chunk
		}
		m.mu.Unlock()
		sess.PushAudio(out)
	}
}

func toPCM16(b []byte) media.PCM16Sample {
	out := make(media.PCM16Sample, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// play drains model audio into the track with elastic playout. level mirrors
// PCMLocalTrack's queue (grows on every write, shrinks by wall time), and speech that
// hits an empty queue is what reaches the listener as a gap or click. A barge-in drops
// everything queued so the model stops talking over the listener.
func play(ctx context.Context, audio <-chan []byte, interrupted <-chan struct{}, track *lkmedia.PCMLocalTrack, target time.Duration) {
	dur := func(n int) time.Duration { return time.Duration(n/2) * time.Second / time.Duration(outRate) }
	frameBytes := outRate / 50 * 2
	seg := gptlive.NewSegmenter(func() {}, func() {})

	var level time.Duration
	var levelAt time.Time
	starved := true
	var underruns, bargeIns int
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
		case <-interrupted:
			track.ClearQueue()
			carry, level, starved = carry[:0], 0, true
			bargeIns++
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
				log.Printf("level=%dms underruns=%d underrun=%dms arrival_ratio=%.3f max_tick_gap=%dms padded=%dms dropped=%dms barge_ins=%d",
					level.Milliseconds(), underruns, underrunDur.Milliseconds(), arrived.Seconds()/elapsed.Seconds(),
					maxTickGap.Milliseconds(), padded.Milliseconds(), dropped.Milliseconds(), bargeIns)
				underruns, bargeIns, underrunDur, maxTickGap, arrived, padded, dropped = 0, 0, 0, 0, 0, 0, 0
				windowStart = now
			}
		case now := <-tick100.C:
			seg.Tick(now)
		}
	}
}
