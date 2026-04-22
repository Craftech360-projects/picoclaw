package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

const defaultManagerAPIBaseURL = "http://localhost:8002/toy"

// ManagerAPIBackendConfig configures manager API backed session storage.
type ManagerAPIBackendConfig struct {
	BaseURL     string
	ServiceKey  string
	MACAddress  string
	AgentID     string
	SessionID   string
	RecentLimit int
	HTTPClient  *http.Client
}

// ManagerAPIBackend hydrates session context from Manager API and persists
// voice conversation turns incrementally. It keeps an in-process cache so the
// existing SessionStore interface remains fast for repeated reads.
type ManagerAPIBackend struct {
	cfg      ManagerAPIBackendConfig
	client   *http.Client
	local    *SessionManager
	mu       sync.Mutex
	hydrated map[string]bool
}

// NewManagerAPIBackend creates a Manager API backed SessionStore.
func NewManagerAPIBackend(cfg ManagerAPIBackendConfig) *ManagerAPIBackend {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultManagerAPIBaseURL
	}
	cfg.BaseURL = baseURL
	if cfg.RecentLimit <= 0 {
		cfg.RecentLimit = 50
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	return &ManagerAPIBackend{
		cfg:      cfg,
		client:   client,
		local:    NewSessionManager(""),
		hydrated: map[string]bool{},
	}
}

func (b *ManagerAPIBackend) AddMessage(sessionKey, role, content string) {
	b.AddFullMessage(sessionKey, providers.Message{Role: role, Content: content})
}

func (b *ManagerAPIBackend) AddFullMessage(sessionKey string, msg providers.Message) {
	if b == nil {
		return
	}
	b.local.AddFullMessage(sessionKey, msg)

	chatType := chatTypeFromRole(msg.Role)
	if chatType == 0 || strings.TrimSpace(msg.Content) == "" {
		return
	}
	if err := b.reportMessage(context.Background(), chatType, msg.Content); err != nil {
		log.Printf("session: manager api report message: %v", err)
	}
}

func (b *ManagerAPIBackend) GetHistory(key string) []providers.Message {
	if b == nil {
		return []providers.Message{}
	}
	b.hydrate(key)
	return b.local.GetHistory(key)
}

func (b *ManagerAPIBackend) GetSummary(key string) string {
	if b == nil {
		return ""
	}
	b.hydrate(key)
	return b.local.GetSummary(key)
}

func (b *ManagerAPIBackend) SetSummary(key, summary string) {
	if b == nil {
		return
	}
	b.local.GetOrCreate(key)
	b.local.SetSummary(key, summary)
	if err := b.saveSummary(context.Background(), summary); err != nil {
		log.Printf("session: manager api save summary: %v", err)
	}
}

func (b *ManagerAPIBackend) SetHistory(key string, history []providers.Message) {
	if b == nil {
		return
	}
	b.local.GetOrCreate(key)
	b.local.SetHistory(key, history)
}

func (b *ManagerAPIBackend) TruncateHistory(key string, keepLast int) {
	if b == nil {
		return
	}
	b.local.TruncateHistory(key, keepLast)
}

func (b *ManagerAPIBackend) Save(key string) error {
	if b == nil {
		return nil
	}
	return b.local.Save(key)
}

func (b *ManagerAPIBackend) Close() error {
	if b == nil {
		return nil
	}
	return b.local.Close()
}

func (b *ManagerAPIBackend) RealtimeChatPersistenceEnabled() bool {
	return b != nil
}

func (b *ManagerAPIBackend) hydrate(key string) {
	b.mu.Lock()
	if b.hydrated[key] {
		b.mu.Unlock()
		return
	}
	b.hydrated[key] = true
	b.mu.Unlock()

	if strings.TrimSpace(b.cfg.MACAddress) == "" {
		return
	}

	bootstrap, err := b.fetchBootstrap(context.Background())
	if err != nil {
		log.Printf("session: manager api bootstrap: %v", err)
		return
	}

	b.local.GetOrCreate(key)
	if len(b.local.GetHistory(key)) == 0 && len(bootstrap.RecentMessages) > 0 {
		b.local.SetHistory(key, bootstrapMessagesToProvider(bootstrap.RecentMessages))
	}
	if strings.TrimSpace(b.local.GetSummary(key)) == "" && strings.TrimSpace(bootstrap.Agent.SummaryMemory) != "" {
		b.local.SetSummary(key, bootstrap.Agent.SummaryMemory)
	}
}

type managerAPIResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type managerBootstrapData struct {
	Agent struct {
		SummaryMemory string `json:"summaryMemory"`
	} `json:"agent"`
	RecentMessages []managerBootstrapMessage `json:"recentMessages"`
}

type managerBootstrapMessage struct {
	Role     string `json:"role"`
	ChatType int    `json:"chatType"`
	Content  string `json:"content"`
}

func (b *ManagerAPIBackend) fetchBootstrap(ctx context.Context) (managerBootstrapData, error) {
	var out managerBootstrapData
	endpoint := fmt.Sprintf("%s/agent/device/%s/bootstrap?includeMemories=false&recentLimit=%d",
		b.cfg.BaseURL,
		url.PathEscape(strings.TrimSpace(b.cfg.MACAddress)),
		b.cfg.RecentLimit,
	)
	body, err := b.doJSON(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return out, err
	}
	if len(body) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode bootstrap data: %w", err)
	}
	return out, nil
}

func (b *ManagerAPIBackend) reportMessage(ctx context.Context, chatType int, content string) error {
	if strings.TrimSpace(b.cfg.MACAddress) == "" || strings.TrimSpace(b.cfg.SessionID) == "" {
		return nil
	}
	payload := map[string]any{
		"macAddress": b.cfg.MACAddress,
		"sessionId":  b.cfg.SessionID,
		"chatType":   chatType,
		"content":    content,
	}
	if strings.TrimSpace(b.cfg.AgentID) != "" {
		payload["agentId"] = b.cfg.AgentID
	}
	_, err := b.doJSON(ctx, http.MethodPost, b.cfg.BaseURL+"/agent/chat-history/report", payload)
	return err
}

func (b *ManagerAPIBackend) saveSummary(ctx context.Context, summary string) error {
	if strings.TrimSpace(b.cfg.MACAddress) == "" {
		return nil
	}
	endpoint := b.cfg.BaseURL + "/agent/saveMemory/" + url.PathEscape(strings.TrimSpace(b.cfg.MACAddress))
	payload := map[string]any{"summaryMemory": summary}
	_, err := b.doJSON(ctx, http.MethodPut, endpoint, payload)
	return err
}

func (b *ManagerAPIBackend) doJSON(ctx context.Context, method, endpoint string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(b.cfg.ServiceKey) != "" {
		req.Header.Set("X-Service-Key", b.cfg.ServiceKey)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var wrapper managerAPIResponse
	if err := json.Unmarshal(respBody, &wrapper); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if wrapper.Code != 0 {
		return nil, fmt.Errorf("api code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	return wrapper.Data, nil
}

func bootstrapMessagesToProvider(messages []managerBootstrapMessage) []providers.Message {
	out := make([]providers.Message, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = roleFromChatType(msg.ChatType)
		}
		if role == "" {
			continue
		}
		out = append(out, providers.Message{
			Role:    role,
			Content: msg.Content,
		})
	}
	return out
}

func chatTypeFromRole(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return 1
	case "assistant", "agent":
		return 2
	default:
		return 0
	}
}

func roleFromChatType(chatType int) string {
	switch chatType {
	case 1:
		return "user"
	case 2:
		return "assistant"
	default:
		return ""
	}
}

var _ SessionStore = (*ManagerAPIBackend)(nil)
