package realtimeconn

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// maxLogText bounds vendor-supplied text (close reasons, error strings) put on a log line.
const maxLogText = 200

// maxLimiterKeys bounds how many distinct keys a LogLimiter remembers; vendor-supplied keys
// past that share one overflow key, so a misbehaving vendor cannot grow the map.
const maxLimiterKeys = 64

const limiterOverflowKey = "\x00overflow"

// LogLimiter lets a log line for a key through at most once per interval and counts the
// lines it held back, so per-frame diagnostics cannot flood the log. A nil *LogLimiter
// allows everything.
type LogLimiter struct {
	every time.Duration
	now   func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
	held map[string]int
}

func NewLogLimiter(every time.Duration) *LogLimiter {
	return &LogLimiter{every: every, now: time.Now, last: map[string]time.Time{}, held: map[string]int{}}
}

// Allow reports whether a line for key may be logged now and, if so, how many lines for
// that key were held back since the last one allowed.
func (l *LogLimiter) Allow(key string) (bool, int) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if _, seen := l.last[key]; !seen && len(l.last) >= maxLimiterKeys {
		key = limiterOverflowKey
	}
	if last, seen := l.last[key]; seen && now.Sub(last) < l.every {
		l.held[key]++
		return false, 0
	}
	held := l.held[key]
	l.last[key] = now
	l.held[key] = 0
	return true, held
}

// closeDetails extracts the websocket close code and reason from a read error. Without a
// close frame (a reset, EOF) the code is 0 and the text is the error's own. The text is
// scrubbed of secret (raw and URL-escaped) before it is cut, in case a vendor echoes it.
func closeDetails(secret string, err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code, truncate(scrubSecret(ce.Text, secret))
	}
	return 0, truncate(scrubSecret(err.Error(), secret))
}

// scrubSecret masks secret in s, in both its raw and URL-escaped forms (Gemini's key rides in the URL).
func scrubSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	s = strings.ReplaceAll(s, secret, "***")
	if escaped := url.QueryEscape(secret); escaped != secret {
		s = strings.ReplaceAll(s, escaped, "***")
	}
	return s
}

func truncate(s string) string {
	if len(s) > maxLogText {
		return s[:maxLogText] + "..."
	}
	return s
}

func msSince(t time.Time) int64 {
	if t.IsZero() {
		return -1
	}
	return time.Since(t).Milliseconds()
}
