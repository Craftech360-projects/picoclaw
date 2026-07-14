package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

type managerActiveProviders struct {
	UpdatedAt string                   `json:"updated_at"`
	LLM       managerActiveLLMProvider `json:"llm"`
	STT       managerActiveSTTProvider `json:"stt"`
	TTS       managerActiveTTSProvider `json:"tts"`
}

type managerActiveLLMProvider struct {
	ModelName string `json:"model_name"`
	Model     string `json:"model"`
	APIBase   string `json:"api_base"`
	APIKey    string `json:"api_key"`
}

type managerActiveSTTProvider struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Language string `json:"language"`
	APIKey   string `json:"api_key"`
}

type managerActiveTTSProvider struct {
	Provider     string  `json:"provider"`
	VoiceID      string  `json:"voice_id"`
	ModelID      string  `json:"model_id"`
	OutputFormat string  `json:"output_format"`
	SampleRateHz int     `json:"sample_rate_hz"`
	Temperature  float64 `json:"temperature"`
	APIKey       string  `json:"api_key"`
}

type managerActiveProvidersCache struct {
	mu        sync.Mutex
	expiresAt time.Time
	hasData   bool
	data      managerActiveProviders
}

var liveKitActiveProvidersCache managerActiveProvidersCache

func resolveLiveKitProviderConfigForSession(
	baseCfg *config.Config,
	managerCfg config.LiveKitServiceManagerAPIConfig,
) (*config.Config, string, error) {
	cloned := cloneConfigForLiveKitSession(baseCfg)
	if cloned == nil {
		return nil, "config_json", fmt.Errorf("base config is nil")
	}
	if strings.TrimSpace(managerAPIBaseURL(managerCfg)) == "" {
		return cloned, "config_json", nil
	}

	now := time.Now()
	ttl := liveKitProviderCacheTTL()
	liveKitActiveProvidersCache.mu.Lock()
	if liveKitActiveProvidersCache.hasData && now.Before(liveKitActiveProvidersCache.expiresAt) {
		data := liveKitActiveProvidersCache.data
		liveKitActiveProvidersCache.mu.Unlock()
		applyManagerActiveProviders(cloned, data)
		return cloned, "manager_cache", nil
	}
	cachedData := liveKitActiveProvidersCache.data
	hasCached := liveKitActiveProvidersCache.hasData
	liveKitActiveProvidersCache.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fetched, err := fetchManagerActiveProviders(ctx, managerCfg, managerAPIServiceKey())
	if err == nil {
		liveKitActiveProvidersCache.mu.Lock()
		liveKitActiveProvidersCache.data = fetched
		liveKitActiveProvidersCache.expiresAt = time.Now().Add(ttl)
		liveKitActiveProvidersCache.hasData = true
		liveKitActiveProvidersCache.mu.Unlock()
		applyManagerActiveProviders(cloned, fetched)
		refreshManagerSTTFactory(fetched) // STT tracks manager changes on the same TTL tick as LLM/TTS
		return cloned, "manager_api", nil
	}
	if hasCached {
		applyManagerActiveProviders(cloned, cachedData)
		return cloned, "manager_cache_stale", err
	}
	return cloned, "config_json", err
}

func fetchManagerActiveProviders(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
) (managerActiveProviders, error) {
	var out managerActiveProviders
	baseURL := strings.TrimSpace(managerAPIBaseURL(cfg))
	if baseURL == "" {
		return out, fmt.Errorf("manager base URL is empty")
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/livekit/providers/active"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return out, err
	}
	if key := strings.TrimSpace(serviceKey); key != "" {
		req.Header.Set("X-Service-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := (&http.Client{Timeout: 2500 * time.Millisecond}).Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}

	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return out, fmt.Errorf("decode wrapper: %w", err)
	}
	if wrapper.Code != 0 {
		return out, fmt.Errorf("api code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(wrapper.Data, &out); err != nil {
		return out, fmt.Errorf("decode providers data: %w", err)
	}
	return out, nil
}

func applyManagerActiveProviders(cfg *config.Config, active managerActiveProviders) {
	if cfg == nil {
		return
	}
	applyManagerLLMProvider(cfg, active.LLM)
	applyManagerSTTProvider(cfg, active.STT)
	applyManagerTTSProvider(cfg, active.TTS)
}

func applyManagerLLMProvider(cfg *config.Config, llm managerActiveLLMProvider) {
	modelName := strings.TrimSpace(llm.ModelName)
	model := strings.TrimSpace(llm.Model)
	if modelName == "" || model == "" {
		return
	}

	var firstMatch *config.ModelConfig
	filtered := make([]*config.ModelConfig, 0, len(cfg.ModelList))
	for _, item := range cfg.ModelList {
		if item != nil && strings.EqualFold(strings.TrimSpace(item.ModelName), modelName) {
			if firstMatch == nil {
				firstMatch = item
			}
			continue
		}
		filtered = append(filtered, item)
	}

	replacement := &config.ModelConfig{
		ModelName: modelName,
		Model:     model,
		APIBase:   strings.TrimSpace(llm.APIBase),
	}

	if firstMatch != nil {
		replacement.ModelName = firstMatch.ModelName
		replacement.APIBase = firstMatch.APIBase
		replacement.Proxy = firstMatch.Proxy
		replacement.Fallbacks = append([]string(nil), firstMatch.Fallbacks...)
		replacement.AuthMethod = firstMatch.AuthMethod
		replacement.ConnectMode = firstMatch.ConnectMode
		replacement.Workspace = firstMatch.Workspace
		replacement.RPM = firstMatch.RPM
		replacement.MaxTokensField = firstMatch.MaxTokensField
		replacement.RequestTimeout = firstMatch.RequestTimeout
		replacement.ThinkingLevel = firstMatch.ThinkingLevel
		if firstMatch.ExtraBody != nil {
			replacement.ExtraBody = make(map[string]any, len(firstMatch.ExtraBody))
			for k, v := range firstMatch.ExtraBody {
				replacement.ExtraBody[k] = v
			}
		}
	}
	if apiBase := strings.TrimSpace(llm.APIBase); apiBase != "" {
		replacement.APIBase = apiBase
	}
	if key := strings.TrimSpace(llm.APIKey); key != "" {
		replacement.SetAPIKey(key)
	}
	cfg.ModelList = append(filtered, replacement)
	cfg.Agents.Defaults.ModelName = modelName
}

func applyManagerSTTProvider(cfg *config.Config, sttCfg managerActiveSTTProvider) {
	if cfg == nil {
		return
	}
	if provider := strings.TrimSpace(sttCfg.Provider); provider != "" {
		cfg.LiveKitService.STT.Provider = provider
	}
	if model := strings.TrimSpace(sttCfg.Model); model != "" {
		cfg.LiveKitService.STT.Model = model
	}
	if language := strings.TrimSpace(sttCfg.Language); language != "" {
		cfg.LiveKitService.STT.Language = language
	}
}

func applyManagerTTSProvider(cfg *config.Config, ttsCfg managerActiveTTSProvider) {
	if cfg == nil {
		return
	}
	if provider := strings.TrimSpace(ttsCfg.Provider); provider != "" {
		cfg.LiveKitService.TTS.Provider = provider
	}
	if voiceID := strings.TrimSpace(ttsCfg.VoiceID); voiceID != "" {
		cfg.LiveKitService.TTS.VoiceID = voiceID
	}
	if modelID := strings.TrimSpace(ttsCfg.ModelID); modelID != "" {
		cfg.LiveKitService.TTS.ModelID = modelID
	}
	if format := strings.TrimSpace(ttsCfg.OutputFormat); format != "" {
		cfg.LiveKitService.TTS.OutputFormat = format
	}
	if ttsCfg.SampleRateHz > 0 {
		cfg.LiveKitService.TTS.SampleRateHz = ttsCfg.SampleRateHz
	}
	if ttsCfg.Temperature != 0 {
		cfg.LiveKitService.TTS.Temperature = ttsCfg.Temperature
	}
	key := strings.TrimSpace(ttsCfg.APIKey)
	if key == "" {
		return
	}

	switch strings.ToLower(strings.TrimSpace(cfg.LiveKitService.TTS.Provider)) {
	case "cartesia":
		cfg.LiveKitService.SetCartesiaAPIKey(key)
	case "deepgram":
		cfg.LiveKitService.SetDeepgramAPIKey(key)
	case "smallest", "smallestai":
		cfg.LiveKitService.SetSmallestAPIKey(key)
	case "inworld":
		cfg.LiveKitService.SetInworldAPIKey(key)
	case "sarvam":
		cfg.LiveKitService.SetSarvamAPIKey(key)
	case "azure":
		cfg.LiveKitService.SetAzureAPIKey(key)
	case "edge", "edgetts":
		// Edge TTS is keyless; nothing to route.
	default:
		cfg.Voice.ElevenLabsAPIKey = key
	}
}

func cloneConfigForLiveKitSession(src *config.Config) *config.Config {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.ModelList = cloneModelList(src.ModelList)
	cloned.Bindings = append([]config.AgentBinding(nil), src.Bindings...)
	cloned.Agents.List = append([]config.AgentConfig(nil), src.Agents.List...)
	cloned.LiveKitService.Skills = append([]string(nil), src.LiveKitService.Skills...)
	return &cloned
}

func cloneModelList(items []*config.ModelConfig) []*config.ModelConfig {
	if len(items) == 0 {
		return nil
	}
	out := make([]*config.ModelConfig, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		cloned := *item
		if item.Fallbacks != nil {
			cloned.Fallbacks = append([]string(nil), item.Fallbacks...)
		}
		if item.ExtraBody != nil {
			cloned.ExtraBody = make(map[string]any, len(item.ExtraBody))
			for k, v := range item.ExtraBody {
				cloned.ExtraBody[k] = v
			}
		}
		out = append(out, &cloned)
	}
	return out
}

func liveKitProviderCacheTTL() time.Duration {
	const (
		def = 30
		min = 5
		max = 300
	)
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_PROVIDER_CACHE_SECONDS"))
	if raw == "" {
		return def * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return def * time.Second
	}
	if seconds < min {
		seconds = min
	}
	if seconds > max {
		seconds = max
	}
	return time.Duration(seconds) * time.Second
}
