package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const defaultXAISttURL = "wss://api.x.ai/v1/stt"

type config struct {
	audioPath      string
	apiKey         string
	wsURL          string
	language       string
	interimResults bool
	endpointingMS  int
	diarize        bool
}

type wavInfo struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	DataOffset    int64
	DataSize      int64
}

type transcriptEvent struct {
	Type        string  `json:"type"`
	Text        string  `json:"text"`
	IsFinal     bool    `json:"is_final"`
	SpeechFinal bool    `json:"speech_final"`
	Duration    float64 `json:"duration"`
	Message     string  `json:"message"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	if cfg.audioPath == "" {
		return errors.New("missing -audio path")
	}
	if cfg.apiKey == "" {
		return errors.New("XAI_API_KEY is not set; pass -api-key or set the environment variable")
	}

	file, err := os.Open(cfg.audioPath)
	if err != nil {
		return fmt.Errorf("open audio: %w", err)
	}
	defer file.Close()

	info, err := parseWAV(file)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 16 {
		return fmt.Errorf("unsupported WAV bit depth %d: xAI raw PCM streaming expects signed 16-bit little-endian audio", info.BitsPerSample)
	}
	if _, err := file.Seek(info.DataOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek WAV data: %w", err)
	}

	wsURL, err := buildXAIURL(cfg, info)
	if err != nil {
		return err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+cfg.apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			return fmt.Errorf("dial xAI STT websocket: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(body)))
		}
		return fmt.Errorf("dial xAI STT websocket: %w", err)
	}
	defer conn.Close()

	if err := waitForReady(conn, out); err != nil {
		return err
	}

	chunkBytes := info.SampleRate * info.Channels * 2 / 10
	if chunkBytes <= 0 {
		chunkBytes = 3200
	}
	buf := make([]byte, chunkBytes)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return fmt.Errorf("send audio: %w", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read audio: %w", readErr)
		}
	}

	if err := conn.WriteJSON(map[string]string{"type": "audio.done"}); err != nil {
		return fmt.Errorf("send audio.done: %w", err)
	}

	return readUntilDone(conn, out)
}

func parseFlags(args []string) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("xai-stt-standalone", flag.ContinueOnError)
	fs.StringVar(&cfg.audioPath, "audio", "audio.wav", "WAV file to stream")
	fs.StringVar(&cfg.apiKey, "api-key", strings.TrimSpace(os.Getenv("XAI_API_KEY")), "xAI API key")
	fs.StringVar(&cfg.wsURL, "url", envOrDefault("XAI_STT_STREAMING_URL", defaultXAISttURL), "xAI STT websocket URL")
	fs.StringVar(&cfg.language, "language", "en", "language code for xAI text formatting")
	fs.BoolVar(&cfg.interimResults, "interim", true, "emit partial transcript events")
	fs.IntVar(&cfg.endpointingMS, "endpointing", 500, "silence duration in ms before xAI utterance-final event")
	fs.BoolVar(&cfg.diarize, "diarize", false, "enable xAI speaker diarization")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func buildXAIURL(cfg config, info wavInfo) (string, error) {
	u, err := url.Parse(cfg.wsURL)
	if err != nil {
		return "", fmt.Errorf("parse websocket URL: %w", err)
	}
	q := u.Query()
	q.Set("sample_rate", strconv.Itoa(info.SampleRate))
	q.Set("encoding", "pcm")
	q.Set("interim_results", strconv.FormatBool(cfg.interimResults))
	q.Set("endpointing", strconv.Itoa(cfg.endpointingMS))
	if cfg.language != "" {
		q.Set("language", cfg.language)
	}
	if cfg.diarize {
		q.Set("diarize", "true")
	}
	if info.Channels > 1 {
		q.Set("multichannel", "true")
		q.Set("channels", strconv.Itoa(info.Channels))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func waitForReady(conn *websocket.Conn, out io.Writer) error {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("wait for transcript.created: %w", err)
	}
	var event transcriptEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("decode ready event: %w", err)
	}
	if event.Type != "transcript.created" {
		if event.Type == "error" && event.Message != "" {
			return fmt.Errorf("xAI error before ready: %s", event.Message)
		}
		return fmt.Errorf("expected transcript.created, got %q", event.Type)
	}
	fmt.Fprintln(out, "Server ready")
	return nil
}

func readUntilDone(conn *websocket.Conn, out io.Writer) error {
	var finalParts []string
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read transcript event: %w", err)
		}
		var event transcriptEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode transcript event: %w", err)
		}
		switch event.Type {
		case "transcript.partial":
			prefix := "partial"
			if event.IsFinal {
				prefix = "FINAL"
				if strings.TrimSpace(event.Text) != "" {
					finalParts = append(finalParts, strings.TrimSpace(event.Text))
				}
			}
			fmt.Fprintf(out, "[%s] %s\n", prefix, event.Text)
		case "transcript.done":
			text := strings.TrimSpace(event.Text)
			if text == "" {
				text = strings.Join(finalParts, " ")
			}
			fmt.Fprintf(out, "\nFull transcript: %s\n", text)
			fmt.Fprintf(out, "Duration: %.2fs\n", event.Duration)
			return nil
		case "error":
			return fmt.Errorf("xAI websocket error: %s", event.Message)
		}
	}
}

func parseWAV(r io.ReadSeeker) (wavInfo, error) {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return wavInfo{}, fmt.Errorf("read WAV header: %w", err)
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return wavInfo{}, errors.New("audio must be a RIFF/WAVE file")
	}

	var info wavInfo
	var sawFmt bool
	for {
		var chunkHeader [8]byte
		if _, err := io.ReadFull(r, chunkHeader[:]); err != nil {
			return wavInfo{}, fmt.Errorf("read WAV chunk header: %w", err)
		}
		chunkID := string(chunkHeader[0:4])
		chunkSize := int64(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		chunkStart, _ := r.Seek(0, io.SeekCurrent)

		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return wavInfo{}, errors.New("invalid WAV fmt chunk")
			}
			buf := make([]byte, chunkSize)
			if _, err := io.ReadFull(r, buf); err != nil {
				return wavInfo{}, fmt.Errorf("read WAV fmt chunk: %w", err)
			}
			audioFormat := binary.LittleEndian.Uint16(buf[0:2])
			if audioFormat != 1 {
				return wavInfo{}, fmt.Errorf("unsupported WAV format %d: only PCM is supported", audioFormat)
			}
			info.Channels = int(binary.LittleEndian.Uint16(buf[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(buf[4:8]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(buf[14:16]))
			sawFmt = true
		case "data":
			if !sawFmt {
				return wavInfo{}, errors.New("WAV data chunk appeared before fmt chunk")
			}
			info.DataOffset = chunkStart
			info.DataSize = chunkSize
			return info, nil
		default:
			if _, err := r.Seek(chunkSize, io.SeekCurrent); err != nil {
				return wavInfo{}, fmt.Errorf("skip WAV chunk %q: %w", chunkID, err)
			}
		}
		if chunkSize%2 == 1 {
			if _, err := r.Seek(1, io.SeekCurrent); err != nil {
				return wavInfo{}, fmt.Errorf("skip WAV chunk padding: %w", err)
			}
		}
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
