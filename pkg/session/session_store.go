package session

import (
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// SessionStore defines the persistence operations used by the agent loop.
// Both SessionManager (legacy JSON backend) and JSONLBackend satisfy this
// interface, allowing the storage layer to be swapped without touching the
// agent loop code.
//
// Write methods (Add*, Set*, Truncate*) are fire-and-forget: they do not
// return errors. Implementations should log failures internally. This
// matches the original SessionManager contract that the agent loop relies on.
type SessionStore interface {
	// AddMessage appends a simple role/content message to the session.
	AddMessage(sessionKey, role, content string)
	// AddFullMessage appends a complete message including tool calls.
	AddFullMessage(sessionKey string, msg providers.Message)
	// GetHistory returns the full message history for the session.
	GetHistory(key string) []providers.Message
	// GetSummary returns the conversation summary, or "" if none.
	GetSummary(key string) string
	// SetSummary replaces the conversation summary.
	SetSummary(key, summary string)
	// SetHistory replaces the full message history.
	SetHistory(key string, history []providers.Message)
	// TruncateHistory keeps only the last keepLast messages.
	TruncateHistory(key string, keepLast int)
	// Save persists any pending state to durable storage.
	Save(key string) error
	// Close releases resources held by the store.
	Close() error
}

// RealtimeChatPersistenceMarker is implemented by stores that already persist
// user/assistant chat turns during the session. Callers can use it to avoid
// duplicate end-of-session transcript uploads.
type RealtimeChatPersistenceMarker interface {
	RealtimeChatPersistenceEnabled() bool
}

// LastActivityReporter is implemented by stores that can say when a session was
// last written to. Callers use it to tell a reconnect from a new visit; stores
// that cannot answer simply do not implement it.
type LastActivityReporter interface {
	LastActivity(key string) (time.Time, bool)
}

// SessionDeleter is implemented by stores that can retire a session key
// outright. Used to drop a transcript whose key scheme changed, so the orphan
// stops riding along in every workspace sync.
type SessionDeleter interface {
	Delete(key string)
}
