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
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
	"github.com/sipeed/picoclaw/pkg/voice/elevenlabs_tts"
)

func main() {
	agentName := flag.String("agent-name", "", "LiveKit named agent identifier (required)")
	configPath := flag.String("config", "", "Path to config.json (default: ~/.picoclaw/config.json)")
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

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating provider: %v\n", err)
		os.Exit(1)
	}

	agentInstance := agent.NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)
	defer agentInstance.Close()

	lkCfg := cfg.LiveKitService
	if lkCfg.ServerURL == "" {
		fmt.Fprintln(os.Stderr, "Error: livekit_service.server_url is required")
		os.Exit(1)
	}

	var dg *deepgram.DeepgramTranscriber
	if lkCfg.DeepgramAPIKey() != "" {
		dg = deepgram.NewDeepgramTranscriber(lkCfg.DeepgramAPIKey())
	}

	ttsCfg := elevenlabs_tts.TTSConfig{
		APIKey:       cfg.Voice.ElevenLabsAPIKey,
		VoiceID:      lkCfg.TTS.VoiceID,
		ModelID:      lkCfg.TTS.ModelID,
		OutputFormat: lkCfg.TTS.OutputFormat,
	}
	var tts *elevenlabs_tts.ElevenLabsTTS
	if strings.TrimSpace(ttsCfg.APIKey) != "" && strings.TrimSpace(ttsCfg.VoiceID) != "" {
		tts = elevenlabs_tts.NewElevenLabsTTS(ttsCfg)
	}

	sampleRate := parsePCMOutputSampleRate(ttsCfg.OutputFormat)

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
				Worker:     worker,
				JobID:      job.Id,
				RoomInfo:   job.Room,
				Bridge:     bridge,
				ServerURL:  serverURL,
				Token:      token,
				Deepgram:   dg,
				TTS:        tts,
				APIKey:     lkCfg.APIKey(),
				APISecret:  lkCfg.APISecret(),
				AgentName:  *agentName,
				SampleRate: sampleRate,
			})
		},
	}
	worker = livekit.NewWorker(workerCfg)

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
