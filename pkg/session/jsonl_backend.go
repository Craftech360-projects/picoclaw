package session

import (
	"context"
	"log"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface.
// Write errors are logged rather than returned, matching the fire-and-forget
// contract of SessionManager that the agent loop relies on.
type JSONLBackend struct {
	store memory.Store
}

// NewJSONLBackend wraps a memory.Store for use as a SessionStore.
func NewJSONLBackend(store memory.Store) *JSONLBackend {
	return &JSONLBackend{store: store}
}

func (b *JSONLBackend) AddMessage(sessionKey, role, content string) {
	if err := b.store.AddMessage(context.Background(), sessionKey, role, content); err != nil {
		log.Printf("session: add message: %v", err)
	}
}

func (b *JSONLBackend) AddFullMessage(sessionKey string, msg providers.Message) {
	if err := b.store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
		log.Printf("session: add full message: %v", err)
	}
}

func (b *JSONLBackend) GetHistory(key string) []providers.Message {
	msgs, err := b.store.GetHistory(context.Background(), key)
	if err != nil {
		log.Printf("session: get history: %v", err)
		return []providers.Message{}
	}
	return msgs
}

func (b *JSONLBackend) GetSummary(key string) string {
	summary, err := b.store.GetSummary(context.Background(), key)
	if err != nil {
		log.Printf("session: get summary: %v", err)
		return ""
	}
	return summary
}

func (b *JSONLBackend) SetSummary(key, summary string) {
	if err := b.store.SetSummary(context.Background(), key, summary); err != nil {
		log.Printf("session: set summary: %v", err)
	}
}

func (b *JSONLBackend) SetHistory(key string, history []providers.Message) {
	if err := b.store.SetHistory(context.Background(), key, history); err != nil {
		log.Printf("session: set history: %v", err)
	}
}

func (b *JSONLBackend) TruncateHistory(key string, keepLast int) {
	if err := b.store.TruncateHistory(context.Background(), key, keepLast); err != nil {
		log.Printf("session: truncate history: %v", err)
	}
}

// Save persists session state. Since the JSONL store fsyncs every write
// immediately, the data is already durable. Save runs compaction to reclaim
// space from logically truncated messages (no-op when there are none).
func (b *JSONLBackend) Save(key string) error {
	return b.store.Compact(context.Background(), key)
}

// LastActivity reports when the session was last written, when the underlying
// store tracks it. The legacy JSON SessionManager does not, so callers must
// treat a false result as "unknown", never as "stale".
func (b *JSONLBackend) LastActivity(key string) (time.Time, bool) {
	reporter, ok := b.store.(interface {
		LastActivity(context.Context, string) (time.Time, bool)
	})
	if !ok {
		return time.Time{}, false
	}
	return reporter.LastActivity(context.Background(), key)
}

// Delete removes the session outright when the underlying store supports it.
// Stores that cannot delete simply do not, the same way LastActivity degrades:
// nothing depends on the removal, it only reclaims a file nobody reads.
func (b *JSONLBackend) Delete(key string) {
	deleter, ok := b.store.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		return
	}
	if err := deleter.Delete(context.Background(), key); err != nil {
		log.Printf("session: delete: %v", err)
	}
}

// Close releases resources held by the underlying store.
func (b *JSONLBackend) Close() error {
	return b.store.Close()
}
