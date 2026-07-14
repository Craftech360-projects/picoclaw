// Package edge_tts is a client for Microsoft Edge's free, keyless TTS endpoint.
// It is UNOFFICIAL and can break without notice when Microsoft rotates the
// endpoint, Origin, version, or Sec-MS-GEC DRM scheme — intended as a cheap
// developer path, not for the paid production voice. The endpoint streams MP3
// (no raw-PCM format is offered), which this client decodes to mono PCM.
package edge_tts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hajimehoshi/go-mp3"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// clockSkewSeconds holds the offset (server - local) learned from a 403's Date
// header, so a wrong local clock doesn't invalidate the time-windowed token.
var clockSkewSeconds atomic.Int64

const (
	trustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	// wsBaseURL is Microsoft's current Edge TTS endpoint. The older
	// speech.platform.bing.com/.../readaloud host now returns 403.
	wsBaseURL       = "wss://api.msedgeservices.com/tts/cognitiveservices/websocket/v1"
	secMSGECVersion = "1-140.0.3485.14"
	edgeOrigin      = "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"
	edgeUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"
	// outputFormat is the only shape the endpoint offers that we can decode; it
	// yields 24 kHz mono MP3, decoded to 24 kHz mono PCM below.
	outputFormat = "audio-24khz-48kbitrate-mono-mp3"
	// SampleRate is the PCM rate this provider emits after decoding.
	SampleRate     = 24000
	defaultVoiceID = "en-US-AnaNeural"
)

// secMSGECToken derives the DRM token Microsoft requires. It is the uppercase
// SHA-256 hex of the trusted client token concatenated after the given time
// expressed in Windows FILETIME ticks (100 ns since 1601-01-01), rounded down to
// the nearest 5 minutes. Callers pass a clock-skew-adjusted time.
func secMSGECToken(now time.Time) string {
	const winEpochOffsetSec = 11644473600 // seconds between 1601-01-01 and 1970-01-01
	ticks := (now.Unix() + winEpochOffsetSec) * 10_000_000
	ticks -= ticks % 3_000_000_000 // round down to 5 minutes (300s * 1e7)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", ticks, trustedClientToken)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// EdgeTTS streams audio from the Edge TTS endpoint.
type EdgeTTS struct {
	cfg TTSConfig
}

// NewEdgeTTS creates a new Edge TTS client.
func NewEdgeTTS(cfg TTSConfig) *EdgeTTS {
	if strings.TrimSpace(cfg.VoiceID) == "" {
		cfg.VoiceID = defaultVoiceID
	}
	return &EdgeTTS{cfg: cfg}
}

// Synthesize opens a websocket, sends the config + SSML, collects the MP3
// stream, and returns it decoded to mono PCM.
func (t *EdgeTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("edge tts is nil")
	}

	connID := strings.ReplaceAll(uuid.NewString(), "-", "")

	logger.InfoCF("edge_tts", "Using Edge TTS provider", map[string]any{
		"tts_provider":       "edge",
		"tts_voice_id":       t.cfg.VoiceID,
		"tts_output_format":  outputFormat,
		"tts_sample_rate_hz": SampleRate,
	})

	conn, resp, err := t.dial(ctx, connID)
	if err != nil && resp != nil && resp.StatusCode == http.StatusForbidden {
		// The Sec-MS-GEC token is time-windowed; a skewed local clock yields 403.
		// Sync to the server's Date header and retry once.
		if correctClockSkew(resp) {
			resp.Body.Close()
			conn, resp, err = t.dial(ctx, connID)
		}
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("edge websocket dial: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(data)))
		}
		return nil, fmt.Errorf("edge websocket dial: %w", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(speechConfigMessage())); err != nil {
		return nil, fmt.Errorf("edge send speech.config: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(ssmlMessage(connID, t.cfg.VoiceID, text))); err != nil {
		return nil, fmt.Errorf("edge send ssml: %w", err)
	}

	mp3Data, err := collectMP3(conn)
	if err != nil {
		return nil, err
	}
	pcm, err := decodeMP3Mono(mp3Data)
	if err != nil {
		return nil, err
	}
	return tts.NewBufferStream(pcm), nil
}

// collectMP3 reads audio frames until turn.end (or close), concatenating the MP3
// payloads.
func collectMP3(conn *websocket.Conn) ([]byte, error) {
	var buf bytes.Buffer
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return buf.Bytes(), nil
			}
			return nil, err
		}
		switch messageType {
		case websocket.TextMessage:
			if strings.Contains(string(data), "Path:turn.end") {
				return buf.Bytes(), nil
			}
		case websocket.BinaryMessage:
			if audio := extractAudioPayload(data); len(audio) > 0 {
				buf.Write(audio)
			}
		}
	}
}

// decodeMP3Mono decodes MP3 to 16-bit little-endian mono PCM. go-mp3 always
// outputs 16-bit stereo, so we keep the left channel of each frame.
func decodeMP3Mono(mp3Data []byte) ([]byte, error) {
	if len(mp3Data) == 0 {
		return nil, errors.New("edge returned no audio")
	}
	dec, err := mp3.NewDecoder(bytes.NewReader(mp3Data))
	if err != nil {
		return nil, fmt.Errorf("edge mp3 decode: %w", err)
	}
	stereo, err := io.ReadAll(dec)
	if err != nil {
		return nil, fmt.Errorf("edge mp3 read: %w", err)
	}
	// stereo is [L0 L1 R0 R1] per frame (4 bytes); keep the left sample.
	mono := make([]byte, 0, len(stereo)/2)
	for i := 0; i+4 <= len(stereo); i += 4 {
		mono = append(mono, stereo[i], stereo[i+1])
	}
	return mono, nil
}

// dial opens the Edge websocket using a clock-skew-adjusted Sec-MS-GEC token.
func (t *EdgeTTS) dial(ctx context.Context, connID string) (*websocket.Conn, *http.Response, error) {
	adjusted := time.Now().Add(time.Duration(clockSkewSeconds.Load()) * time.Second)
	endpoint := fmt.Sprintf("%s?Ocp-Apim-Subscription-Key=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=%s&ConnectionId=%s",
		wsBaseURL, trustedClientToken, secMSGECToken(adjusted), secMSGECVersion, connID)

	header := http.Header{}
	header.Set("User-Agent", edgeUserAgent)
	header.Set("Origin", edgeOrigin)
	header.Set("Pragma", "no-cache")
	header.Set("Cache-Control", "no-cache")
	header.Set("Accept-Encoding", "gzip, deflate, br")
	header.Set("Accept-Language", "en-US,en;q=0.9")

	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		EnableCompression: true,
		Subprotocols:      []string{"synthesize"},
	}
	return dialer.DialContext(ctx, endpoint, header)
}

// correctClockSkew reads the server time from a 403 response's Date header and
// stores the offset so the next token matches Microsoft's clock. Returns whether
// an offset was learned.
func correctClockSkew(resp *http.Response) bool {
	dateHdr := resp.Header.Get("Date")
	if dateHdr == "" {
		return false
	}
	serverTime, err := http.ParseTime(dateHdr)
	if err != nil {
		return false
	}
	clockSkewSeconds.Store(int64(serverTime.Sub(time.Now()).Seconds()))
	return true
}

// extractAudioPayload parses an Edge binary frame: a 2-byte big-endian header
// length, the header text, then the audio bytes.
func extractAudioPayload(frame []byte) []byte {
	if len(frame) < 2 {
		return nil
	}
	headerLen := int(binary.BigEndian.Uint16(frame[0:2]))
	start := 2 + headerLen
	if start > len(frame) {
		return nil
	}
	header := string(frame[2:start])
	// Match the full "Path:audio" line so it doesn't also catch "Path:audio.metadata".
	if !strings.Contains(header, "Path:audio\r\n") {
		return nil
	}
	return frame[start:]
}

func speechConfigMessage() string {
	body := fmt.Sprintf(
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"%s"}}}}`,
		outputFormat,
	)
	return "X-Timestamp:" + edgeTimestamp() + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" + body
}

func ssmlMessage(requestID, voice, text string) string {
	lang := ssmlLangFromVoice(voice)
	ssml := fmt.Sprintf(
		`<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='%s'><voice name='%s'>%s</voice></speak>`,
		lang, ssmlEscaper.Replace(voice), ssmlEscaper.Replace(text),
	)
	return "X-RequestId:" + requestID + "\r\n" +
		"Content-Type:application/ssml+xml\r\n" +
		"X-Timestamp:" + edgeTimestamp() + "\r\n" +
		"Path:ssml\r\n\r\n" + ssml
}

var ssmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func ssmlLangFromVoice(voice string) string {
	parts := strings.Split(strings.TrimSpace(voice), "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}

func edgeTimestamp() string {
	return time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}
