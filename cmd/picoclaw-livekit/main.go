package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"

	"github.com/joho/godotenv"
	livekitproto "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/voice/cartesia_tts"
	"github.com/sipeed/picoclaw/pkg/voice/elevenlabs_tts"
	"github.com/sipeed/picoclaw/pkg/voice/inworld_tts"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

func main() {
	// Best-effort .env loading for local/dev runs. Existing env vars keep precedence.
	_ = godotenv.Load()

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
	configureGoogleCredentials(cfg, cfgPath)

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating provider: %v\n", err)
		os.Exit(1)
	}

	logger.InfoCF("livekit", "Finished provider initialization", map[string]any{
		"model": modelID,
	})

	lkCfg := cfg.LiveKitService
	if lkCfg.ServerURL == "" {
		fmt.Fprintln(os.Stderr, "Error: livekit_service.server_url is required")
		os.Exit(1)
	}

	// Initialize STT factory with PostgreSQL
	sttDBSource := ""
	sttDBURL := os.Getenv("STT_DATABASE_URL")
	if sttDBURL == "" {
		// Try to get from config file field if present
		sttDBURL = cfg.LiveKitService.STT.DatabaseURL
		if sttDBURL != "" {
			sttDBSource = "config.livekit_service.stt.database_url"
		}
	} else {
		sttDBSource = "env.STT_DATABASE_URL"
	}
	if sttDBURL == "" {
		// Fallback to Supabase PostgreSQL URL from environment
		sttDBURL = os.Getenv("DIRECT_URL")
		if sttDBURL != "" {
			sttDBSource = "env.DIRECT_URL"
		}
	}
	if sttDBURL == "" {
		fmt.Fprintln(os.Stderr, "Error creating STT factory: STT DB URL is empty.")
		fmt.Fprintln(os.Stderr, "Set one of: STT_DATABASE_URL, PICOCLAW_LIVEKIT_STT_DATABASE_URL, or DIRECT_URL.")
		os.Exit(1)
	}

	sttFactory, err := stt.NewFactory(sttDBURL)
	if err != nil {
		dbHost, dbUser := summarizeDBURL(sttDBURL)
		fmt.Fprintf(os.Stderr, "Error creating STT factory: %v (source=%s, host=%s, user=%s)\n",
			err, sttDBSource, dbHost, dbUser)
		os.Exit(1)
	}

	logger.InfoCF("livekit", "STT factory initialized", map[string]any{
		"db_source": sttDBSource,
		"providers": sttFactory.ListProviders(),
	})

	// Seed providers from environment variables
	if apiKey := lkCfg.DeepgramAPIKey(); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("deepgram", apiKey, "nova-2", 1); err != nil {
			logger.WarnCF("livekit", "Failed to configure Deepgram provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("groq", apiKey, "whisper-large-v3", 5); err != nil {
			logger.WarnCF("livekit", "Failed to configure Groq provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("ASSEMBLYAI_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("assemblyai", apiKey, "universal", 2); err != nil {
			logger.WarnCF("livekit", "Failed to configure AssemblyAI provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("openai", apiKey, "whisper-1", 6); err != nil {
			logger.WarnCF("livekit", "Failed to configure OpenAI provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("CARTESIA_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("cartesia", apiKey, "ink-whisper", 7); err != nil {
			logger.WarnCF("livekit", "Failed to configure Cartesia provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("ELEVENLABS_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("elevenlabs", apiKey, "scribe_v2", 8); err != nil {
			logger.WarnCF("livekit", "Failed to configure ElevenLabs provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("GRADIUM_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("gradium", apiKey, "default", 15); err != nil {
			logger.WarnCF("livekit", "Failed to configure Gradium provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("MISTRAL_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("mistral", apiKey, "voxtral-mini-latest", 16); err != nil {
			logger.WarnCF("livekit", "Failed to configure Mistral provider", map[string]any{
				"error": err.Error(),
			})
		}
		// Alias for users who want provider_name=voxtral in database.
		if err := sttFactory.SeedProviderConfig("voxtral", apiKey, "voxtral-mini-latest", 17); err != nil {
			logger.WarnCF("livekit", "Failed to configure Voxtral provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("SARVAM_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("sarvam", apiKey, "saaras:v3", 18); err != nil {
			logger.WarnCF("livekit", "Failed to configure Sarvam provider", map[string]any{
				"error": err.Error(),
			})
		}
	}

	if apiKey := os.Getenv("XAI_API_KEY"); apiKey != "" {
		if err := sttFactory.SeedProviderConfig("xai", apiKey, "stt", 19); err != nil {
			logger.WarnCF("livekit", "Failed to configure xAI provider", map[string]any{
				"error": err.Error(),
			})
		}
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

	bridgeFactory := func(job *livekitproto.Job) *livekit.AgentBridge {
		roomName := ""
		roomMetadata := ""
		if job != nil && job.Room != nil {
			roomName = strings.TrimSpace(job.Room.Name)
			roomMetadata = job.Room.Metadata
		}

		deviceMAC, persistentAgentID := livekit.ResolvePersistenceFields(roomName, roomMetadata)
		workspaceIdentity := roomName
		preserveWorkspace := false
		switch {
		case deviceMAC != "":
			workspaceIdentity = "device-" + strings.ReplaceAll(deviceMAC, ":", "")
			preserveWorkspace = true
		case strings.TrimSpace(persistentAgentID) != "":
			workspaceIdentity = "agent-" + routing.NormalizeAgentID(persistentAgentID)
			preserveWorkspace = true
		}
		if workspaceIdentity == "" {
			workspaceIdentity = "main"
		}

		agentCfg := &config.AgentConfig{
			ID:   workspaceIdentity,
			Name: "LiveKit-" + workspaceIdentity,
		}

		// 1. Calculate the workspace path for this job identity exactly like NewAgentInstance.
		// This ensures we drop the personalized prompt precisely where this room identity reads it.
		var workspace string
		if cfg.Agents.Defaults.Workspace != "" {
			home := os.Getenv(config.EnvHome)
			userHome, _ := os.UserHomeDir()
			if home == "" {
				home = filepath.Join(userHome, pkg.DefaultPicoClawHome)
			}
			baseWorkspace := strings.Replace(cfg.Agents.Defaults.Workspace, "~", userHome, 1)
			if !filepath.IsAbs(baseWorkspace) && strings.Contains(cfg.Agents.Defaults.Workspace, "~") {
				baseWorkspace = strings.Replace(cfg.Agents.Defaults.Workspace, "~", userHome, 1)
			}
			id := routing.NormalizeAgentID(agentCfg.ID)
			workspace = filepath.Join(baseWorkspace, "..", "workspace-"+id)
		}

		// 2. Fetch and decode Room Metadata payload from MQTT gateway.
		// Room metadata is the primary bootstrap path; manager API bootstrap is a future fallback.
		if roomMetadata != "" && workspace != "" {
			bootstrap, err := parseRoomMetadataBootstrap(roomMetadata)
			if err == nil {
				md := bootstrap.Metadata
				// 3. Load the Go template we built
				tmplPath := filepath.Join(".", "prompts", "cheeko.tmpl")
				if tmplBytes, readErr := os.ReadFile(tmplPath); readErr == nil {
					if tmpl, parseErr := template.New("cheeko").Parse(string(tmplBytes)); parseErr == nil {
						var buf bytes.Buffer
						if execErr := tmpl.Execute(&buf, md); execErr == nil {
							// 4. Write the rendered prompt directly into the resolved workspace
							// The AgentInstance's ContextBuilder will swallow it instantly on boot
							os.MkdirAll(workspace, 0755)
							os.WriteFile(filepath.Join(workspace, "IDENTITY.md"), buf.Bytes(), 0644)
							logger.InfoCF("livekit", "Successfully injected zero-latency dynamic IDENTITY.md", map[string]any{
								"room":               roomName,
								"child":              md.ChildProfile.Name,
								"workspace_identity": workspaceIdentity,
								"persistent":         preserveWorkspace,
								"bootstrap_source":   bootstrap.Source,
							})
						} else {
							logger.ErrorCF("livekit", "Template exec failed", map[string]any{"error": execErr.Error()})
						}
					} else {
						logger.ErrorCF("livekit", "Template parse failed", map[string]any{"error": parseErr.Error()})
					}
				} else {
					logger.ErrorCF("livekit", "Could not read cheeko.tmpl", map[string]any{"error": readErr.Error()})
				}
			} else {
				logger.ErrorCF("livekit", "Invalid job.Room.Metadata", map[string]any{
					"error":            err.Error(),
					"bootstrap_source": bootstrap.Source,
				})
			}
		}

		agentInstance := agent.NewAgentInstance(agentCfg, &cfg.Agents.Defaults, cfg, provider)
		if managerStore := buildManagerSessionStore(lkCfg, deviceMAC, persistentAgentID, roomName); managerStore != nil {
			if agentInstance.Sessions != nil {
				_ = agentInstance.Sessions.Close()
			}
			agentInstance.Sessions = managerStore
			logger.InfoCF("livekit", "Using manager-backed session store", map[string]any{
				"room":               roomName,
				"device_mac":         deviceMAC,
				"agent_id":           persistentAgentID,
				"workspace_identity": workspaceIdentity,
			})
		}

		// Register shared tools on the ephemeral agent instance
		singleAgentRegistry := agent.NewAgentRegistry(cfg, provider)
		agent.RegisterSharedTools(agent.SharedToolDependencies{
			Config:   cfg,
			Registry: singleAgentRegistry,
			Provider: provider,
		})

		if defaultAgent := singleAgentRegistry.GetDefaultAgent(); defaultAgent != nil {
			for _, toolName := range defaultAgent.Tools.List() {
				if t, ok := defaultAgent.Tools.Get(toolName); ok {
					agentInstance.Tools.Register(t)
				}
			}
		}
		agentInstance.Tools.Register(tools.NewTimerTool())
		if added := ensureLiveKitWorkspaceFileTools(agentInstance, &cfg.Agents.Defaults, cfg); len(added) > 0 {
			logger.WarnCF("livekit", "Forced required workspace file tools for LiveKit agent", map[string]any{
				"room":               roomName,
				"workspace_identity": workspaceIdentity,
				"tools":              added,
			})
		}

		bridge, err := livekit.NewAgentBridge(livekit.AgentBridgeConfig{
			Config:            cfg,
			Provider:          provider,
			ModelID:           modelID,
			AgentInstance:     agentInstance,
			PreserveWorkspace: preserveWorkspace,
			MaxIterations:     cfg.Agents.Defaults.MaxToolIterations,
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
		MaxSessions:   lkCfg.MaxSessions,
		HealthPort:    lkCfg.HealthPort,
		RoomFactory: func(job *livekitproto.Job, assignment *livekitproto.JobAssignment, bridge *livekit.AgentBridge) (*livekit.RoomSession, error) {
			serverURL := lkCfg.ServerURL
			if assignment != nil && assignment.Url != nil && *assignment.Url != "" {
				serverURL = *assignment.Url
			}
			token := ""
			if assignment != nil {
				token = assignment.Token
			}
			// Extract primaryLanguage from metadata for language-aware fallback phrases
			primaryLanguage := ""
			if job.Room != nil && job.Room.Metadata != "" {
				var mdLang struct {
					PrimaryLanguage string `json:"primary_language"`
				}
				if jsonErr := json.Unmarshal([]byte(job.Room.Metadata), &mdLang); jsonErr == nil {
					primaryLanguage = mdLang.PrimaryLanguage
				}
			}

			// Get active STT provider for this session
			sttProvider := buildSTTProvider(sttFactory)

			return livekit.NewRoomSession(livekit.RoomSessionConfig{
				Worker:          worker,
				JobID:           job.Id,
				RoomInfo:        job.Room,
				Bridge:          bridge,
				ServerURL:       serverURL,
				Token:           token,
				STT:             sttProvider,
				TTS:             ttsProvider,
				APIKey:          lkCfg.APIKey(),
				APISecret:       lkCfg.APISecret(),
				AgentName:       *agentName,
				SampleRate:      ttsSampleRate,
				FillerWords:     lkCfg.TTS.FillerWords,
				PrimaryLanguage: primaryLanguage,
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

func configureGoogleCredentials(cfg *config.Config, cfgPath string) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")) != "" {
		return
	}

	credPath := strings.TrimSpace(cfg.Voice.GoogleCredentialsFile)
	if credPath == "" {
		return
	}

	if strings.HasPrefix(credPath, "~") {
		if userHome, err := os.UserHomeDir(); err == nil {
			credPath = filepath.Join(userHome, strings.TrimPrefix(credPath, "~"))
		}
	}
	if !filepath.IsAbs(credPath) {
		credPath = filepath.Join(filepath.Dir(cfgPath), credPath)
	}

	resolved, err := filepath.Abs(credPath)
	if err == nil {
		credPath = resolved
	}
	if _, err := os.Stat(credPath); err != nil {
		logger.WarnCF("livekit", "Configured Google credentials file not found", map[string]any{
			"path":  credPath,
			"error": err.Error(),
		})
		return
	}
	if err := os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath); err != nil {
		logger.WarnCF("livekit", "Failed to export Google credentials file", map[string]any{
			"path":  credPath,
			"error": err.Error(),
		})
		return
	}

	logger.InfoCF("livekit", "Configured Google credentials file from config", map[string]any{
		"path": credPath,
	})
}

func buildSTTProvider(factory *stt.Factory) stt.Provider {
	if factory == nil {
		return nil
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		logger.WarnCF("livekit", "No active STT provider, using default", map[string]any{
			"error": err.Error(),
		})
		return nil
	}

	logger.InfoCF("livekit", "Using STT provider", map[string]any{
		"provider": provider.Name(),
	})

	return provider
}

func buildTTSProvider(cfg *config.Config, lkCfg config.LiveKitServiceConfig) (tts.Provider, int) {
	factory := tts.NewFactory()
	factory.Register("elevenlabs", elevenlabs_tts.NewBuilder())
	factory.Register("inworld", inworld_tts.NewBuilder())
	factory.Register("cartesia", cartesia_tts.NewBuilder())

	return factory.Create(cfg, lkCfg)
}

func summarizeDBURL(raw string) (host, user string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "invalid", "invalid"
	}
	host = parsed.Hostname()
	if parsed.User != nil {
		user = parsed.User.Username()
	}
	if host == "" {
		host = "unknown"
	}
	if user == "" {
		user = "unknown"
	}
	return host, user
}
