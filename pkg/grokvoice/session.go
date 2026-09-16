// Package grokvoice speaks xAI's Grok Voice Agent API (OpenAI Realtime-style events,
// https://docs.x.ai/docs/guides/voice/agent) and emits gptlive events, so gptLivePipeline can run
// a session on it. Grok is a single model: it calls the session's tools itself.
package grokvoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/realtimeconn"
)

const (
	DefaultBaseURL = "wss://api.x.ai/v1/realtime"
	DefaultModel   = "grok-voice-think-fast-1.0"
	DefaultVoice   = "ara"
	SampleRate     = 24000 // PCM16 mono, both directions
	defaultSilence = 700 * time.Millisecond
)

type Config struct {
	APIKey       string
	BaseURL      string // default DefaultBaseURL
	Model        string // default DefaultModel
	Voice        string // default DefaultVoice
	Instructions string
	Tools        []map[string]any     // gptlive.FunctionTools shape; {"type":"web_search"} passes through
	Executor     gptlive.ToolExecutor // nil: every call is answered with an error output
	Silence      time.Duration        // server VAD end-of-turn silence; default 700ms
}

type Session struct {
	*realtimeconn.Conn
	cfg     Config
	started time.Time

	mu           sync.Mutex
	instructions string
	agentText    map[string]string
	// billableAudio is the running sum of the vendor's per-response billed audio
	// seconds, and billedSeen the response IDs already counted into it. Both
	// exist for the response.done LOG LINE alone — see recordBilled for why
	// this number is deliberately not what VoiceUsage reports.
	billableAudio float64
	billedSeen    map[string]bool

	// active, pending and owed together decide when the single response.create
	// that resumes the conversation may be sent. They are tracked globally
	// (not per response_id) because only one continuation is ever needed
	// regardless of how many responses or calls are outstanding at once — see
	// maybeContinue's own comment for why that is still correct across an
	// interruption (a later response starting while an earlier one's tool call
	// is still running).
	active  bool            // a response is in flight: response.created, or a call within one, seen; response.done not yet seen
	pending map[string]bool // call IDs from response.function_call_arguments.done not yet answered
	owed    bool            // a tool output or AppendCommentary is waiting on a response.create

	logLimit *realtimeconn.LogLimiter // unknown/unparsed events
	logDone  func(map[string]any)     // the response.done diagnostic's sink; see logDone
}

func Dial(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("grokvoice: API key is empty")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Voice == "" {
		cfg.Voice = DefaultVoice
	}
	if cfg.Silence <= 0 {
		cfg.Silence = defaultSilence
	}
	ws, err := realtimeconn.Dial(ctx, cfg.BaseURL+"?model="+url.QueryEscape(cfg.Model),
		http.Header{"Authorization": {"Bearer " + cfg.APIKey}}, cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("grokvoice: %w", err)
	}
	s := &Session{Conn: realtimeconn.New(ws), cfg: cfg, started: time.Now(), instructions: cfg.Instructions,
		agentText: map[string]string{}, pending: map[string]bool{}, billedSeen: map[string]bool{},
		logLimit: realtimeconn.NewLogLimiter(30 * time.Second), logDone: logDone}
	s.SetSecret(cfg.APIKey)
	if err := s.Send(s.sessionUpdate()); err != nil {
		_ = s.Conn.Close()
		return nil, fmt.Errorf("grokvoice: session.update: %w", err)
	}
	go s.Run(s.handle, nil, s.onEnd)
	return s, nil
}

func (s *Session) sessionUpdate() map[string]any {
	s.mu.Lock()
	instructions := s.instructions
	s.mu.Unlock()
	pcm := map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": SampleRate}}
	session := map[string]any{
		"voice":          s.cfg.Voice,
		"instructions":   instructions,
		"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": s.cfg.Silence.Milliseconds()},
		"audio":          map[string]any{"input": pcm, "output": pcm},
	}
	if len(s.cfg.Tools) > 0 {
		session["tools"] = s.cfg.Tools
	}
	return map[string]any{"type": "session.update", "session": session}
}

func (s *Session) PushAudio(pcm []byte) {
	_ = s.Send(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)})
}

// AppendInstructions grows the standing instructions (the quiz Door directive) and re-sends them.
func (s *Session) AppendInstructions(text string) {
	s.mu.Lock()
	s.instructions += "\n\n" + text
	s.mu.Unlock()
	_ = s.Send(s.sessionUpdate())
}

// AppendCommentary makes the model say something now (greeting, goodbye). The
// item is queued immediately — that's always safe — but the response.create
// that makes the model actually speak to it goes through the same maybeContinue
// gate as a tool continuation, so it waits out a response that's already
// active instead of being rejected mid-speech.
func (s *Session) AppendCommentary(text string) {
	_ = s.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}},
	}})
	s.mu.Lock()
	s.owed = true
	s.mu.Unlock()
	s.maybeContinue()
}

func (s *Session) Close(ctx context.Context) error {
	_ = s.Conn.Close()
	select {
	case <-s.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type serverEvent struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	ItemID     string `json:"item_id"`
	Transcript string `json:"transcript"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	// Usage is the TOP-LEVEL usage object — a sibling of type/event_id, not a
	// member of response. That is where the live server actually puts the
	// numbers; response.usage, the only place xAI's published schema mentions,
	// arrives as an empty object. See pickUsage.
	Usage *usage `json:"usage"`
	// ResponseID names the response this frame closes. It is used for one thing:
	// deduping the billed-audio total the response.done line logs, since the
	// live frame's items carry a "replayed" flag — so the vendor can resend a
	// response.done, and a replay must not be counted twice.
	ResponseID string `json:"response_id"`
	Response   *struct {
		ID    string `json:"id"`
		Usage *usage `json:"usage"`
	} `json:"response"`
	Error json.RawMessage `json:"error"`
}

// responseID prefers the top-level field and falls back to response.id; the
// live frame carries both, the published schema only the latter.
func (ev *serverEvent) responseID() string {
	if ev.ResponseID != "" {
		return ev.ResponseID
	}
	if ev.Response != nil {
		return ev.Response.ID
	}
	return ""
}

// pickUsage maps a response.done frame onto gptlive.BackendUsage, reading both
// places a usage object has ever been seen: the top-level one the live server
// actually fills and the documented response.usage, which arrives empty. Both
// are tried because only one of them is documented — a server that starts
// filling the documented place must not go unnoticed.
//
// Each SIDE is resolved independently across the two objects, top-level first
// and response.usage as the fallback, rather than picking one object whole: a
// frame carrying input_tokens at the top level and output_tokens only in
// response.usage used to report output 0, which is the same silent-zero failure
// this whole mapping exists to prevent. The returned name is logged as usage_at
// and names the object each number came from (or both, joined, when they were
// split), so a change of venue costs one log line instead of another zero-token
// session.
func (ev *serverEvent) pickUsage(model string) (gptlive.BackendUsage, usageSource, string) {
	var respUsage *usage
	if ev.Response != nil {
		respUsage = ev.Response.Usage
	}
	type mapped struct {
		bu   gptlive.BackendUsage
		src  usageSource
		name string
	}
	var objs []mapped
	for _, c := range []struct {
		u    *usage
		name string
	}{{ev.Usage, "usage"}, {respUsage, "response.usage"}} {
		if c.u == nil {
			continue
		}
		bu, src := c.u.backendUsage(model)
		objs = append(objs, mapped{bu, src, c.name})
	}
	if len(objs) == 0 {
		return gptlive.BackendUsage{}, usageSource{Input: "none", Output: "none", Total: "none"}, "none"
	}
	out := gptlive.BackendUsage{Model: model}
	src := usageSource{Input: "none", Output: "none", Total: "none"}
	var inAt, outAt string
	for _, o := range objs {
		// Cached/Reasoning travel with the side they belong to, so a breakdown
		// is never read out of one object while the total came from the other.
		if out.Input == 0 && o.bu.Input != 0 {
			out.Input, out.Cached, src.Input, inAt = o.bu.Input, o.bu.Cached, o.src.Input, o.name
		}
		if out.Output == 0 && o.bu.Output != 0 {
			out.Output, out.Reasoning, src.Output, outAt = o.bu.Output, o.bu.Reasoning, o.src.Output, o.name
		}
	}
	// A vendor-sent total is passed through — backendUsage only reports
	// total_from=total_tokens when the vendor actually sent one — but only from
	// an object that also supplied a side, so the loser object's total can never
	// contradict the numbers that were kept. When no side resolved anywhere,
	// any object's total still counts, since a frame carrying nothing but
	// total_tokens is better read than dropped. Otherwise the two resolved sides
	// are added, which is what backendUsage does within one object and stays
	// right across two.
	for _, o := range objs {
		contributed := o.name == inAt || o.name == outAt || (inAt == "" && outAt == "")
		if contributed && o.src.Total == "total_tokens" && o.bu.Total != 0 {
			out.Total, src.Total = o.bu.Total, "total_tokens"
			break
		}
	}
	if out.Total == 0 {
		out.Total, src.Total = out.Input+out.Output, "input+output"
	}
	return out, src, usageAt(objs[0].name, inAt, outAt)
}

// usageAt names the object (or objects) the numbers came from. Nothing mapped
// is reported as "<first present> (unmapped)" — not "(empty)", which is what it
// used to say and was wrong: an object can be full of numbers and merely spell
// them in a way this code has not met yet, and an operator reading "(empty)"
// beside a shape dump of a populated object chases the wrong thing.
func usageAt(first, inAt, outAt string) string {
	switch {
	case inAt != "" && outAt != "" && inAt != outAt:
		return inAt + "+" + outAt
	case inAt != "":
		return inAt
	case outAt != "":
		return outAt
	}
	return first + " (unmapped)"
}

// usage decodes a response.done usage object. xAI's published websocket schema
// (https://docs.x.ai/voice-realtime.ws.json, response.done -> response.usage)
// documents only the three flat integers input_tokens/output_tokens/total_tokens
// — but a live run logged has_usage=true and still produced a zero-token
// session, which can only mean the server sent a usage object whose numbers live
// under names the schema does not mention. xAI's own REST spec
// (https://docs.x.ai/openapi.json) carries three different spellings of the same
// object, and the model behind the voice agent is served by the same stack:
//
//	Usage        (chat completions): prompt_tokens / completion_tokens / total_tokens,
//	                                 prompt_tokens_details{text,audio,image,cached},
//	                                 completion_tokens_details{reasoning,audio,...}
//	ModelUsage   (responses):        input_tokens / output_tokens / total_tokens,
//	                                 input_tokens_details{cached}, output_tokens_details{reasoning}
//	OpenAI realtime response.done:   same three flat integers plus
//	                                 input_token_details / output_token_details (singular
//	                                 "token") with text/audio/cached splits
//
// So every spelling is accepted, and the flat number wins when present; the
// detail objects are only summed when the flat number is absent or zero, which
// is exactly the case that used to persist tokens=0.
type usage struct {
	// xAI voice-realtime / responses / OpenAI realtime spelling.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	// xAI chat-completions spelling of the same two numbers.
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// Detail objects, in all three documented spellings.
	InputTokenDetails       tokenDetails `json:"input_token_details"`
	InputTokensDetails      tokenDetails `json:"input_tokens_details"`
	PromptTokensDetails     tokenDetails `json:"prompt_tokens_details"`
	OutputTokenDetails      tokenDetails `json:"output_token_details"`
	OutputTokensDetails     tokenDetails `json:"output_tokens_details"`
	CompletionTokensDetails tokenDetails `json:"completion_tokens_details"`
	// BillableAudioSeconds is xAI's own billed audio time for the response. It
	// appears in no published schema (neither voice-realtime.ws.json nor
	// openapi.json mentions it); it is read off the live protocol, so it is
	// treated as optional throughout, and it is LOGGED ONLY — see
	// Session.recordBilled and Session.voiceSeconds.
	BillableAudioSeconds float64 `json:"billable_audio_seconds"`
}

// tokenDetails is one side's breakdown. cached_tokens is deliberately left out
// of every sum below: xAI documents it as "a subset of text_tokens, not
// additive" (MediaUsage.input_tokens in openapi.json), as does OpenAI realtime.
type tokenDetails struct {
	TextTokens      int `json:"text_tokens"`
	AudioTokens     int `json:"audio_tokens"`
	ImageTokens     int `json:"image_tokens"`
	CachedTokens    int `json:"cached_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
	// GrokTokens is undocumented — it appears in the live input_token_details
	// (beside audio_tokens and text_tokens) and in neither published schema. It
	// is summed as a distinct slice of its side's total, the way every other
	// member here is, because that is what a breakdown member means everywhere
	// xAI does document one; the only member either vendor calls a subset is
	// cached_tokens, and this is not it. If it ever turns out to overlap
	// text_tokens, the effect is an over-count of the fallback path only — the
	// flat number, when the vendor sends one, always wins over this sum.
	GrokTokens int `json:"grok_tokens"`
}

func (d tokenDetails) empty() bool { return d == tokenDetails{} }

// sum adds up a breakdown. cached_tokens is excluded (see the type comment);
// every other member is a distinct slice of the same side's total, so image and
// reasoning tokens count too — xAI's MediaUsage documents output_tokens as
// "rewritten-prompt text tokens + reasoning tokens + generated image tokens".
func (d tokenDetails) sum() int {
	return d.TextTokens + d.AudioTokens + d.ImageTokens + d.ReasoningTokens + d.GrokTokens
}

// named is a breakdown together with the member name it was decoded from, so
// the log can always say which spelling a number came from.
type named struct {
	tokenDetails
	name string
}

func pick(ds ...named) named {
	for _, d := range ds {
		if !d.empty() {
			return d
		}
	}
	return named{name: "none"}
}

// candidate is one spelling of a side's numbers: a flat member and the
// breakdown belonging to the SAME family. Keeping them paired is what stops
// Cached/Reasoning being read out of one vendor's spelling while the total came
// from another's, when a frame happens to carry both.
type candidate struct {
	flat int
	name string
	det  named
}

// resolve returns one side's token count, the breakdown that goes with it, and
// the member name the count came from. A flat number always wins; a breakdown
// is summed only when no flat member of any spelling carries anything, which is
// the case that used to persist tokens=0.
func resolve(cands ...candidate) (int, named, string) {
	for _, c := range cands {
		if c.flat != 0 {
			return c.flat, c.det, c.name
		}
	}
	for _, c := range cands {
		if !c.det.empty() {
			return c.det.sum(), c.det, c.det.name + " sum"
		}
	}
	return 0, named{name: "none"}, "none"
}

// usageSource names the member each number was taken from. It is logged on
// every response.done line, not just the ones that parsed to nothing: a tolerant
// six-way mapping that never says which way it went can never be narrowed, and a
// vendor quietly switching spellings would stay invisible until it regressed to
// zero again.
type usageSource struct{ Input, Output, Total string }

// backendUsage maps the decoded object onto gptlive.BackendUsage, whose
// Input/Output/Total the pipeline sums across the session.
func (u *usage) backendUsage(model string) (gptlive.BackendUsage, usageSource) {
	input, in, inFrom := resolve(
		candidate{u.InputTokens, "input_tokens", pick(
			named{u.InputTokenDetails, "input_token_details"},
			named{u.InputTokensDetails, "input_tokens_details"})},
		candidate{u.PromptTokens, "prompt_tokens", named{u.PromptTokensDetails, "prompt_tokens_details"}},
	)
	output, out, outFrom := resolve(
		candidate{u.OutputTokens, "output_tokens", pick(
			named{u.OutputTokenDetails, "output_token_details"},
			named{u.OutputTokensDetails, "output_tokens_details"})},
		candidate{u.CompletionTokens, "completion_tokens", named{u.CompletionTokensDetails, "completion_tokens_details"}},
	)
	total, totalFrom := u.TotalTokens, "total_tokens"
	if total == 0 {
		total, totalFrom = input+output, "input+output"
	}
	return gptlive.BackendUsage{
			Model:     model,
			Input:     input,
			Cached:    in.CachedTokens,
			Output:    output,
			Reasoning: out.ReasoningTokens,
			Total:     total,
		}, usageSource{
			Input:  inFrom,
			Output: outFrom,
			Total:  totalFrom,
		}
}

// billableAudioSeconds reads xAI's own billed audio time for this response off
// whichever usage object carries it.
func (ev *serverEvent) billableAudioSeconds() float64 {
	if ev.Usage != nil && ev.Usage.BillableAudioSeconds > 0 {
		return ev.Usage.BillableAudioSeconds
	}
	if ev.Response != nil && ev.Response.Usage != nil {
		return ev.Response.Usage.BillableAudioSeconds
	}
	return 0
}

// voiceSeconds is the session's wall clock, and is what every VoiceUsage and
// the teardown Closed event report.
//
// It is deliberately NOT the vendor's billable_audio_seconds. VoiceUsage.Seconds
// travels gptLivePipeline.recordVoiceSeconds -> RoomSession -> the persistence
// POST's sessionDurationSeconds, a field geminilive fills with a wall clock and
// gptlive with the vendor's session seconds; feeding summed billed audio into it
// for Grok alone would silently redefine one persisted billing number per
// vendor (the observed ~90 s session that billed 3 s would persist ~3). It would
// also mix bases within a single Grok session, since a response.done that
// carries no billable_audio_seconds — a tool-call-only response, or the vendor
// simply omitting an undocumented field — would emit a wall clock that the
// pipeline's monotonic keep then makes permanent, discarding every later billed
// total. One meaning per field; the billed number is logged instead, by
// recordBilled.
func (s *Session) voiceSeconds() float64 { return time.Since(s.started).Seconds() }

// recordBilled adds one response's vendor-billed audio time to the session's
// running total and returns that response's own value and the new total, both
// for the response.done log line — nothing else reads them, and nothing
// persists them (see voiceSeconds).
//
// Logging both is what makes the open question answerable: the field is in no
// published schema, and whether it is per-response or cumulative cannot be told
// from one sample. Per-response, the total tracks the session's speech and stays
// well under its wall clock; cumulative, the sum goes triangular and overtakes
// the wall clock on the second or third response — which the line's own
// billable/billable_total/wall_seconds fields show directly.
//
// A response ID is counted once: the live frame's items carry a "replayed"
// flag, so the vendor can resend a response.done, and a replay must not inflate
// the total. An ID-less frame is counted, since there is nothing to dedupe on.
func (s *Session) recordBilled(id string, billed float64) (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if billed > 0 && (id == "" || !s.billedSeen[id]) {
		if id != "" {
			if s.billedSeen == nil { // a Session built by hand rather than by Dial
				s.billedSeen = map[string]bool{}
			}
			s.billedSeen[id] = true
		}
		s.billableAudio += billed
	}
	return billed, s.billableAudio
}

// round1 keeps the log's seconds readable: a wall clock printed to nanosecond
// precision buries the comparison the line exists to make.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// logDone is the response.done diagnostic's only way out to the log — a
// variable so the tests can assert what it actually emits, since this is the
// one line in the package that reports on a frame's own contents: it has to
// fire when nothing parsed (a live run that reports zero tokens must explain
// itself) and it must never carry a string value out of that frame. Dial copies
// it into the Session, so a test swapping it back can never race a session that
// is still reading frames.
var logDone = func(fields map[string]any) {
	logger.InfoCF("realtime", "grok voice: response.done", fields)
}

func (s *Session) emitDoneLog(fields map[string]any) {
	f := s.logDone
	if f == nil { // a Session built by hand rather than by Dial
		f = logDone
	}
	f(fields)
}

// protocolStrings are the only string values describeJSON prints: protocol
// constants (an event type, "realtime.response", a completed/cancelled/
// incomplete status), never free text. Every other string is elided, so a frame
// can be described — which is all a field-name mismatch needs — with no way for
// a transcript of the child or the model to reach the log.
var protocolStrings = map[string]bool{"type": true, "status": true, "object": true}

// shapeLimit bounds the structural dump. It is generous enough for a whole
// usage object and both of its breakdowns: the 400-byte cap this started at cut
// the live frame off mid-key, inside the very object that was being diagnosed.
const shapeLimit = 1200

// doneShape describes the part of a response.done frame a token mismatch can
// live in: every usage object the frame carries, at every place one has ever
// been seen (the documented response.usage and the top-level one the live server
// actually fills), each rendered by describeValue so names and numbers survive
// and strings do not.
//
// The usage objects alone are enough only while the numbers are still IN one of
// them under some spelling — which shows up as a non-zero number somewhere
// inside. When no usage object carries one (none present at all, or every one
// of them empty or all-zero, e.g. usage:{} beside response.usage:{} with the
// real numbers moved to a sibling token_usage), that narrowing is exactly the
// blind spot this diagnostic was built to close, so the WHOLE frame is
// described instead — the question is then where the vendor moved them to, and
// only the frame can answer it. Strings stay elided either way, and the caller
// scrubs and clips the result as before.
func doneShape(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "unparsed"
	}
	top, _ := v.(map[string]any)
	var parts []string
	narrow := false
	if u, ok := top["usage"]; ok {
		parts = append(parts, "usage:"+describeValue("usage", u, 6))
		narrow = narrow || hasNonZeroNumber(u)
	}
	if r, ok := top["response"].(map[string]any); ok {
		if u, ok := r["usage"]; ok {
			parts = append(parts, "response.usage:"+describeValue("usage", u, 6))
			narrow = narrow || hasNonZeroNumber(u)
		}
	}
	if !narrow {
		return describeValue("", v, 6)
	}
	return strings.Join(parts, " ")
}

// hasNonZeroNumber reports whether a decoded JSON value carries a non-zero
// number anywhere inside it. That is what separates a usage object worth
// dumping on its own — populated, just spelled in a way this code has not met
// yet — from one that has nothing to say.
func hasNonZeroNumber(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for _, e := range t {
			if hasNonZeroNumber(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasNonZeroNumber(e) {
				return true
			}
		}
	case float64:
		return t != 0
	}
	return false
}

// describeJSON renders a frame as its structure alone: the key names at every
// level, numbers, booleans and nulls verbatim, arrays as their length and
// element shape, and string values as "str" (bar protocolStrings). "unparsed"
// if raw is not JSON. Callers scrub first and clip after — in that order, so a
// secret can never survive by straddling the cut.
func describeJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "unparsed"
	}
	return describeValue("", v, 6)
}

// clip shortens s to at most limit bytes, cutting on a rune boundary so a
// multi-byte character is never split into replacement-character garbage.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func describeValue(key string, v any, depth int) string {
	switch t := v.(type) {
	case map[string]any:
		if depth == 0 {
			return "{...}"
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+describeValue(k, t[k], depth-1))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(t) == 0 {
			return "[]"
		}
		if depth == 0 {
			return "[" + strconv.Itoa(len(t)) + "]"
		}
		return "[" + strconv.Itoa(len(t)) + "x" + describeValue(key, t[0], depth-1) + "]"
	case string:
		if protocolStrings[key] && len(t) <= 32 {
			return t
		}
		return "str"
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return "null"
	}
}

func (s *Session) handle(raw []byte) {
	var ev serverEvent
	if json.Unmarshal(raw, &ev) != nil {
		if ok, held := s.logLimit.Allow("unparsed"); ok {
			logger.WarnCF("realtime", "grok voice: unparsed server event dropped", map[string]any{"bytes": len(raw), "suppressed": held})
		}
		return
	}
	switch ev.Type {
	case "response.output_audio.delta", "response.audio.delta":
		if pcm, err := base64.StdEncoding.DecodeString(ev.Delta); err == nil {
			s.EmitAudio(pcm)
		}
	case "input_audio_buffer.speech_started":
		s.Emit(gptlive.Interrupted{})
	case "conversation.item.input_audio_transcription.completed":
		if ev.Transcript != "" {
			s.Emit(gptlive.UserTranscript{ID: ev.ItemID, Text: ev.Transcript, Final: true, StartedAt: time.Now()})
		}
	case "response.output_audio_transcript.delta":
		s.mu.Lock()
		s.agentText[ev.ItemID] += ev.Delta
		text := s.agentText[ev.ItemID]
		s.mu.Unlock()
		s.Emit(gptlive.AgentTranscript{ID: ev.ItemID, Delta: ev.Delta, Text: text})
	case "response.created":
		s.mu.Lock()
		s.active = true
		s.mu.Unlock()
	case "response.function_call_arguments.done":
		call := gptlive.FunctionCall{CallID: ev.CallID, Name: ev.Name, Arguments: ev.Arguments}
		s.mu.Lock()
		s.active = true // a call only ever happens inside a response; covers a vendor that omits response.created
		s.pending[ev.CallID] = true
		s.mu.Unlock()
		s.Emit(call)
		s.Go(func() { s.answer(call) })
	case "response.done":
		// usage is optional in xAI's own schema, and a usage object that IS sent
		// may spell its numbers in any of the ways the usage type documents —
		// both of which are invisible from the outside (either just looks like a
		// zero-token session). So the line carries the parsed numbers AND the
		// member each one came from (input_from/output_from/total_from) — that is
		// what lets the tolerant mapping above be narrowed later, and what makes a
		// vendor switching spellings visible before it regresses to zero rather
		// than after — as does usage_at, which says WHICH usage object was read
		// (the live server fills the top-level one and leaves response.usage
		// empty; only the latter is documented). And whenever the line has no
		// numbers to carry — no usage anywhere, or one that mapped to nothing —
		// it adds the frame's SHAPE: every key name at every level (usage and its
		// *_details objects included) with numbers kept and every string value
		// elided by doneShape. That names the fields we are missing without
		// logging a word of what was said, which a raw frame could not promise:
		// this API is an OpenAI-realtime clone whose response.done carries
		// response.output[].content[].transcript, and this very bug is proof that
		// the live server departs from the published schema. Rate-limited under
		// its own key, so an anomalous frame is never crowded out of the window
		// by the healthy line.
		hasUsage := ev.Usage != nil || (ev.Response != nil && ev.Response.Usage != nil)
		bu, src, at := ev.pickUsage(s.cfg.Model)
		blind := bu.Input == 0 && bu.Output == 0 && bu.Total == 0
		// Recorded outside the rate limiter: the running total has to be right
		// on the lines that DO get logged, whichever those are.
		billed, billedTotal := s.recordBilled(ev.responseID(), ev.billableAudioSeconds())
		wall := s.voiceSeconds()
		key := "response.done"
		if blind {
			key = "response.done:no-tokens"
		}
		if ok, held := s.logLimit.Allow(key); ok {
			fields := map[string]any{
				"has_response": ev.Response != nil,
				"has_usage":    hasUsage,
				"usage_at":     at,
				"input":        bu.Input,
				"output":       bu.Output,
				"total":        bu.Total,
				"input_from":   src.Input,
				"output_from":  src.Output,
				"total_from":   src.Total,
				// The vendor's own billed audio time for this response, the
				// session's running sum of it, and the wall clock to read them
				// against — a sum that overtakes the wall clock means
				// billable_audio_seconds is cumulative, not per-response. None of
				// the three is persisted; see voiceSeconds and recordBilled.
				"billable":         billed,
				"billable_total":   round1(billedTotal),
				"wall_seconds":     round1(wall),
				"billed_over_wall": billedTotal > wall,
				"suppressed":       held,
			}
			if blind {
				fields["shape"] = clip(s.Scrub(doneShape(raw)), shapeLimit)
			}
			s.emitDoneLog(fields)
		}
		if hasUsage {
			s.Emit(bu)
		}
		s.Emit(gptlive.VoiceUsage{Seconds: wall})
		s.mu.Lock()
		// The response (and every item in it) is finished: clear the accumulated
		// per-item transcript text now rather than letting it grow for the life
		// of the session, and rather than letting a missing item_id accumulate
		// every response's deltas under the same "" key.
		s.agentText = map[string]string{}
		s.active = false
		s.mu.Unlock()
		s.maybeContinue()
	case "error":
		s.Emit(gptlive.Error{Err: fmt.Errorf("grokvoice: %s", ev.Error), Recoverable: true})
	default:
		if ignoredEvents[ev.Type] {
			return
		}
		typ := ev.Type
		if len(typ) > 64 {
			typ = typ[:64]
		}
		if ok, held := s.logLimit.Allow("type:" + typ); ok { // the type name only: payloads carry transcript text
			logger.InfoCF("realtime", "grok voice: unhandled server event type", map[string]any{"type": typ, "bytes": len(raw), "suppressed": held})
		}
	}
}

// ignoredEvents are documented Voice Agent events handle deliberately does nothing with, so the
// "unhandled server event type" line shows only what is actually unexpected.
var ignoredEvents = map[string]bool{
	"session.created": true, "session.updated": true, "conversation.created": true,
	"conversation.item.added": true, "conversation.item.created": true,
	"input_audio_buffer.speech_stopped": true, "input_audio_buffer.committed": true, "input_audio_buffer.cleared": true,
	"response.output_item.added": true, "response.output_item.done": true,
	"response.content_part.added": true, "response.content_part.done": true,
	"response.output_audio.done": true, "response.audio.done": true, "response.output_audio_transcript.done": true,
	"response.function_call_arguments.delta": true, "ping": true,
}

func (s *Session) answer(call gptlive.FunctionCall) {
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &args)
	start := time.Now()
	output, isErr := realtimeconn.RunTool(s.cfg.Executor, call.Name, args)
	toolMs := time.Since(start).Milliseconds()
	s.Emit(gptlive.FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	sendErr := s.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "function_call_output", "call_id": call.CallID, "output": output,
	}})
	logger.InfoCF("realtime", "grok voice: tool result", map[string]any{
		"name": call.Name, "is_error": isErr, "ms": toolMs, "output_bytes": len(output), "response_sent": sendErr == nil,
	})
	s.mu.Lock()
	delete(s.pending, call.CallID)
	s.owed = true
	s.mu.Unlock()
	s.maybeContinue()
}

// maybeContinue sends the single response.create that resumes the conversation,
// once no response is active, no call is still outstanding, and at least one
// has returned an output (or AppendCommentary queued something) since the last
// continuation was sent. active/pending/owed are a single running total, not
// one entry per response_id as gptlive/delegation.go's maybeContinue keeps for
// the GPT-Live backend, because only one continuation is ever needed regardless
// of how many responses or calls are outstanding — which also makes this
// correct across an interruption: a slow tool call from response A returning
// while the child's own speech has already started response B defers the
// create (active is still true, from B) instead of firing it mid-response B
// (rejected by the protocol) or losing track of A's call once B's bookkeeping
// would otherwise have overwritten it.
func (s *Session) maybeContinue() {
	s.mu.Lock()
	if s.active || len(s.pending) > 0 || !s.owed {
		s.mu.Unlock()
		return
	}
	s.owed = false
	s.mu.Unlock()
	_ = s.Send(map[string]any{"type": "response.create"})
}

// onEnd: Grok has no session resumption here, so an unexpected socket end ends the session
// (gptLivePipeline leaves the room on a non-recoverable Error).
func (s *Session) onEnd(err error) {
	if !s.Closing() {
		s.Emit(gptlive.Error{Err: errors.New("grokvoice: connection lost: " + s.Scrub(fmt.Sprint(err))), Recoverable: false})
	}
	// Same basis as every VoiceUsage this session emitted (see voiceSeconds):
	// the pipeline keeps the larger of the two, so the two must never differ in
	// what they measure.
	s.Emit(gptlive.Closed{Reason: "socket closed", VoiceSeconds: s.voiceSeconds()})
}
