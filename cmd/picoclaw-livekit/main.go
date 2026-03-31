package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	livekitproto "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/voice/cartesia_tts"
	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
	"github.com/sipeed/picoclaw/pkg/voice/elevenlabs_tts"
	"github.com/sipeed/picoclaw/pkg/voice/inworld_tts"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

func main() {
	agentName := flag.String("agent-name", "", "LiveKit named agent identifier (required)")
	configPath := flag.String("config", "", "Path to config.json (default: ~/.picoclaw/config.json)")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	if strings.TrimSpace(*agentName) == "" {
		fmt.Fprintln(os.Stderr, "Error: --agent-name is required")
		flag.Usage()
		os.Exit(1)
	}

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	logger.SetLevelFromString(*logLevel)

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating provider: %v\n", err)
		os.Exit(1)
	}

	agentInstance := agent.NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)
	defer agentInstance.Close()

	// Register shared tools (web_search, spawn, subagent, etc.) on the agent instance
	// so they are available to the voice agent bridge.
	singleAgentRegistry := agent.NewAgentRegistry(cfg, provider)
	agent.RegisterSharedTools(agent.SharedToolDependencies{
		Config:   cfg,
		Registry: singleAgentRegistry,
		Provider: provider,
		// No MessageBus in LiveKit mode — message tool will gracefully no-op
		// No SubTurnSpawner or SubagentSpawner — spawn tools will use their
		// fallback legacy tool loop path via SubagentManager
	})
	// Copy the tools from the registry's default agent into our instance
	if defaultAgent := singleAgentRegistry.GetDefaultAgent(); defaultAgent != nil {
		for _, toolName := range defaultAgent.Tools.List() {
			if t, ok := defaultAgent.Tools.Get(toolName); ok {
				agentInstance.Tools.Register(t)
			}
		}
	}
	logger.InfoCF("livekit", "Registered shared tools on agent instance", map[string]any{
		"tool_count": agentInstance.Tools.Count(),
	})

	lkCfg := cfg.LiveKitService
	if lkCfg.ServerURL == "" {
		fmt.Fprintln(os.Stderr, "Error: livekit_service.server_url is required")
		os.Exit(1)
	}

	var dg *deepgram.DeepgramTranscriber
	if lkCfg.DeepgramAPIKey() != "" {
		dg = deepgram.NewDeepgramTranscriber(lkCfg.DeepgramAPIKey())
	}

	ttsProvider, ttsSampleRate := buildTTSProvider(cfg, lkCfg)
	logger.InfoCF("livekit", "Configured TTS provider", map[string]any{
		"provider":           lkCfg.TTS.Provider,
		"voice_id":           lkCfg.TTS.VoiceID,
		"model_id":           lkCfg.TTS.ModelID,
		"sample_rate_hz":     ttsSampleRate,
		"has_inworld_key":    strings.TrimSpace(lkCfg.InworldAPIKey()) != "",
		"has_cartesia_key":   strings.TrimSpace(lkCfg.CartesiaAPIKey()) != "",
		"has_elevenlabs_key": strings.TrimSpace(cfg.Voice.ElevenLabsAPIKey) != "",
		"tts_enabled":        ttsProvider != nil,
	})

	bridgeFactory := func() *livekit.AgentBridge {
		bridge, err := livekit.NewAgentBridge(livekit.AgentBridgeConfig{
			Config:         cfg,
			Provider:       provider,
			ModelID:        modelID,
			Sessions:       agentInstance.Sessions,
			Tools:          agentInstance.Tools,
			ContextBuilder: agentInstance.ContextBuilder,
			MaxIterations:  cfg.Agents.Defaults.MaxToolIterations,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating agent bridge: %v\n", err)
			return nil
		}
		return bridge
	}

	var worker *livekit.Worker
	workerCfg := livekit.WorkerConfig{
		AgentName:     *agentName,
		ServerURL:     lkCfg.ServerURL,
		APIKey:        lkCfg.APIKey(),
		APISecret:     lkCfg.APISecret(),
		BridgeFactory: bridgeFactory,
		RoomFactory: func(job *livekitproto.Job, assignment *livekitproto.JobAssignment, bridge *livekit.AgentBridge) (*livekit.RoomSession, error) {
			serverURL := lkCfg.ServerURL
			if assignment != nil && assignment.Url != nil && *assignment.Url != "" {
				serverURL = *assignment.Url
			}
			token := ""
			if assignment != nil {
				token = assignment.Token
			}
			return livekit.NewRoomSession(livekit.RoomSessionConfig{
				Worker:      worker,
				JobID:       job.Id,
				RoomInfo:    job.Room,
				Bridge:      bridge,
				ServerURL:   serverURL,
				Token:       token,
				Deepgram:    dg,
				TTS:         ttsProvider,
				APIKey:      lkCfg.APIKey(),
				APISecret:   lkCfg.APISecret(),
				AgentName:   *agentName,
				SampleRate:  ttsSampleRate,
				FillerWords: lkCfg.TTS.FillerWords,
			})
		},
	}
	worker = livekit.NewWorker(workerCfg)

	logger.InfoCF("livekit", "Starting LiveKit worker", map[string]any{
		"agent_name": *agentName,
		"server_url": lkCfg.ServerURL,
		"log_level":  *logLevel,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		worker.Shutdown()
		cancel()
	}()

	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
		os.Exit(1)
	}
}

func defaultConfigPath() string {
	if configPath := os.Getenv(config.EnvConfig); configPath != "" {
		return configPath
	}

	home := os.Getenv(config.EnvHome)
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, pkg.DefaultPicoClawHome)
	}
	return filepath.Join(home, "config.json")
}

func parsePCMOutputSampleRate(format string) int {
	format = strings.TrimSpace(format)
	if format == "" {
		return 24000
	}
	if strings.HasPrefix(format, "pcm_") {
		value := strings.TrimPrefix(format, "pcm_")
		switch value {
		case "16000":
			return 16000
		case "22050":
			return 22050
		case "24000":
			return 24000
		case "44100":
			return 44100
		case "48000":
			return 48000
		}
	}
	return 24000
}

func buildTTSProvider(cfg *config.Config, lkCfg config.LiveKitServiceConfig) (tts.Provider, int) {
	factory := tts.NewFactory()
	factory.Register("elevenlabs", elevenlabs_tts.NewBuilder())
	factory.Register("inworld", inworld_tts.NewBuilder())
	factory.Register("cartesia", cartesia_tts.NewBuilder())

	return factory.Create(cfg, lkCfg)
}
