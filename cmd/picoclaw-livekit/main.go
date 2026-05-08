package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/joho/godotenv"
	livekitproto "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
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
	normalizeLiveKitRuntimeConfig(&lkCfg.Runtime)
	logger.InfoCF("livekit", "LiveKit runtime policy", map[string]any{
		"greeting_mode":                     lkCfg.Runtime.GreetingMode,
		"async_announce_mode":               lkCfg.Runtime.AsyncAnnounceMode,
		"vad_threshold":                     lkCfg.Runtime.VADThreshold,
		"vad_endpoint_ms":                   lkCfg.Runtime.VADEndpointMS,
		"rate_limit_cooldown_seconds":       lkCfg.Runtime.RateLimitCooldownSeconds,
		"provider_failure_cooldown_seconds": lkCfg.Runtime.ProviderFailureCooldownSec,
	})

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
		roomName, roomMetadata, metadataSource := resolveLiveKitJobBootstrapContext(job)
		if strings.TrimSpace(roomMetadata) == "" {
			logger.WarnCF("livekit", "No metadata available for LiveKit job bootstrap", map[string]any{
				"room": roomName,
			})
		} else if metadataSource != "room_metadata" {
			logger.InfoCF("livekit", "Using non-room metadata source for LiveKit bootstrap", map[string]any{
				"room":            roomName,
				"metadata_source": metadataSource,
				"metadata_bytes":  len(roomMetadata),
			})
		}

		lifecycle := resolveLiveKitWorkspaceLifecycle(roomName, roomMetadata, lkCfg.ManagerAPI)
		deviceMAC := lifecycle.DeviceMAC
		persistentAgentID := lifecycle.AgentID
		workspaceIdentity := lifecycle.WorkspaceIdentity
		preserveWorkspace := lifecycle.PreserveWorkspace

		agentCfg := &config.AgentConfig{
			ID:     workspaceIdentity,
			Name:   "LiveKit-" + workspaceIdentity,
			Skills: append([]string(nil), lkCfg.Skills...),
		}

		// 1. Calculate the workspace path for this job identity exactly like NewAgentInstance.
		// This ensures we drop the personalized prompt precisely where this room identity reads it.
		var workspace string
		var baseWorkspace string
		workspaceFirstTime := false
		if cfg.Agents.Defaults.Workspace != "" {
			home := os.Getenv(config.EnvHome)
			userHome, _ := os.UserHomeDir()
			if home == "" {
				home = filepath.Join(userHome, pkg.DefaultPicoClawHome)
			}
			baseWorkspace = strings.Replace(cfg.Agents.Defaults.Workspace, "~", userHome, 1)
			if !filepath.IsAbs(baseWorkspace) && strings.Contains(cfg.Agents.Defaults.Workspace, "~") {
				baseWorkspace = strings.Replace(cfg.Agents.Defaults.Workspace, "~", userHome, 1)
			}
			id := routing.NormalizeAgentID(agentCfg.ID)
			workspace = filepath.Join(baseWorkspace, "..", "workspace-"+id)
			if _, statErr := os.Stat(workspace); os.IsNotExist(statErr) {
				workspaceFirstTime = true
			}
		}

		// 2. Fetch and decode room metadata payload from MQTT gateway.
		// We keep this for child/memory hydration, but AGENT.md prompt prefers DB system_prompt.
		bootstrap := roomMetadataBootstrap{Source: bootstrapSourceManagerAPIFallback}
		renderedIdentity := ""
		workspaceBootstrapSource := bootstrap.Source
		if roomMetadata != "" {
			var err error
			bootstrap, err = parseRoomMetadataBootstrap(roomMetadata)
			if err == nil {
				md := bootstrap.Metadata
				// 3. Load the Go template we built
				tmplPath := filepath.Join(".", "prompts", "cheeko.tmpl")
				if tmplBytes, readErr := os.ReadFile(tmplPath); readErr == nil {
					if tmpl, parseErr := template.New("cheeko").Parse(string(tmplBytes)); parseErr == nil {
						var buf bytes.Buffer
						if execErr := tmpl.Execute(&buf, md); execErr == nil {
							renderedIdentity = buf.String()
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
				logger.ErrorCF("livekit", "Invalid job metadata payload", map[string]any{
					"error":            err.Error(),
					"bootstrap_source": bootstrap.Source,
					"metadata_source":  metadataSource,
				})
			}
		}
		hydrationOptions := buildLiveKitWorkspaceHydrationOptions(baseWorkspace, bootstrap, renderedIdentity)
		if strings.TrimSpace(deviceMAC) != "" && managerSessionStoreEnabled(lkCfg.ManagerAPI) {
			managerBootstrap, err := fetchManagerWorkspaceBootstrap(
				context.Background(),
				lkCfg.ManagerAPI,
				deviceMAC,
				managerAPIServiceKey(),
			)
			if err != nil {
				logger.WarnCF("livekit", "Manager API workspace bootstrap hydration failed", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
					"error":      err.Error(),
				})
			} else {
				hydrationOptions = mergeManagerHydrationOptions(hydrationOptions, managerBootstrap, baseWorkspace)
				workspaceBootstrapSource = bootstrapSourceManagerAPIFallback
				logger.InfoCF("livekit", "Merged manager API memory into LiveKit workspace hydration", map[string]any{
					"room":              roomName,
					"device_mac":        deviceMAC,
					"agent_name":        managerBootstrap.Agent.AgentName,
					"recent_messages":   len(managerBootstrap.RecentMessages),
					"session_summaries": len(managerBootstrap.SessionSummaries),
					"recent_sessions":   len(managerBootstrap.RecentSessions),
				})
			}
		} else if bootstrap.Source == bootstrapSourceRoomMetadata {
			workspaceBootstrapSource = bootstrap.Source
		}
		if workspace != "" {
			hydration, err := hydrateLiveKitWorkspaceSkeleton(
				workspace,
				hydrationOptions,
			)
			if err != nil {
				logger.WarnCF("livekit", "Failed to hydrate LiveKit workspace skeleton", map[string]any{
					"room":               roomName,
					"workspace_identity": workspaceIdentity,
					"error":              err.Error(),
				})
			} else {
				logger.InfoCF("livekit", "Hydrated LiveKit workspace skeleton", map[string]any{
					"room":               roomName,
					"child":              bootstrap.Metadata.ChildProfile.Name,
					"workspace_identity": workspaceIdentity,
					"persistent":         preserveWorkspace,
					"bootstrap_source":   workspaceBootstrapSource,
					"identity_rendered":  strings.TrimSpace(hydrationOptions.IdentityContent) != "",
					"memory_written":     hydration.MemoryWritten,
					"skills_copied":      hydration.SkillsCopied,
				})
				installedSkills, missingSkills := validateLiveKitActiveSkills(workspace, lkCfg.Skills)
				logger.InfoCF("livekit", "Validated LiveKit active skills", map[string]any{
					"room":             roomName,
					"workspace":        workspace,
					"active_skills":    lkCfg.Skills,
					"installed_skills": installedSkills,
					"missing_skills":   missingSkills,
				})
				if len(missingSkills) > 0 {
					logger.WarnCF("livekit", "LiveKit active skills are missing from workspace", map[string]any{
						"room":           roomName,
						"workspace":      workspace,
						"missing_skills": missingSkills,
					})
				}
			}
		}
		if strings.TrimSpace(deviceMAC) != "" && managerAPIBaseURL(lkCfg.ManagerAPI) != "" && workspace != "" {
			if err := downloadWorkspaceFilesFastPath(context.Background(), lkCfg.ManagerAPI, deviceMAC, workspace); err != nil {
				logger.WarnCF("livekit", "workspace-files fast-path download from manager failed", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
					"error":      err.Error(),
				})
			} else if userContent := strings.TrimSpace(hydrationOptions.UserContent); userContent != "" {
				userPath := filepath.Join(workspace, "USER.md")
				if shouldWrite, reason := shouldRefreshUserFromMetadata(userPath, workspaceFirstTime); shouldWrite {
					if err := os.WriteFile(userPath, []byte(ensureTrailingNewline(userContent)), 0o644); err != nil {
						logger.WarnCF("livekit", "Failed to reapply USER.md from room metadata child profile", map[string]any{
							"room":       roomName,
							"device_mac": deviceMAC,
							"path":       userPath,
							"error":      err.Error(),
						})
					} else {
						logger.InfoCF("livekit", "Reapplied USER.md from room metadata child profile", map[string]any{
							"room":       roomName,
							"device_mac": deviceMAC,
							"path":       userPath,
							"reason":     reason,
						})
					}
				} else {
					desiredTimezone := extractTimezoneFromUserMarkdown(userContent)
					if desiredTimezone == "" {
						desiredTimezone = "Asia/Kolkata"
					}
					if changed, syncReason, err := syncUserTimezoneInFile(userPath, desiredTimezone); err != nil {
						logger.WarnCF("livekit", "Failed to sync USER.md timezone from child profile metadata", map[string]any{
							"room":       roomName,
							"device_mac": deviceMAC,
							"path":       userPath,
							"timezone":   desiredTimezone,
							"error":      err.Error(),
						})
					} else if changed {
						logger.InfoCF("livekit", "Synchronized USER.md timezone from child profile metadata", map[string]any{
							"room":       roomName,
							"device_mac": deviceMAC,
							"path":       userPath,
							"timezone":   desiredTimezone,
							"reason":     syncReason,
						})
					}
				}
			}
			go func(room string, mac string, dir string) {
				bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := downloadWorkspaceFiles(bgCtx, lkCfg.ManagerAPI, mac, dir); err != nil {
					logger.WarnCF("livekit", "workspace background full restore failed", map[string]any{
						"room":       room,
						"device_mac": mac,
						"error":      err.Error(),
					})
					return
				}
				logger.InfoCF("livekit", "workspace background full restore completed", map[string]any{
					"room":       room,
					"device_mac": mac,
				})
			}(roomName, deviceMAC, workspace)
		}

		agentInstance := agent.NewAgentInstance(agentCfg, &cfg.Agents.Defaults, cfg, provider)
		artifactStore := buildManagerArtifactStore(lkCfg, deviceMAC)
		if artifactStore != nil {
			hydrated, err := hydrateWorkspaceArtifacts(context.Background(), artifactStore, agentInstance.Workspace, lkCfg.ManagerAPI.RecentLimit)
			if err != nil {
				logger.WarnCF("livekit", "Failed to hydrate manager-backed workspace artifacts", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
					"error":      err.Error(),
				})
			} else if hydrated > 0 {
				logger.InfoCF("livekit", "Hydrated manager-backed workspace artifacts", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
					"count":      hydrated,
				})
			}
		}
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
		var cronService *cron.CronService
		var cronTool *tools.CronTool
		if cfg.Tools.IsToolEnabled("cron") {
			cronStorePath := filepath.Join(agentInstance.Workspace, "cron", "jobs.json")
			cronService = cron.NewCronService(cronStorePath, nil)
			if err := cronService.Start(); err != nil {
				logger.WarnCF("livekit", "Failed to start cron service for LiveKit agent", map[string]any{
					"room":  roomName,
					"error": err.Error(),
				})
				cronService = nil
			} else {
				cronTool, err = tools.NewCronTool(
					cronService,
					nil,
					nil,
					agentInstance.Workspace,
					cfg.Agents.Defaults.RestrictToWorkspace,
					time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes)*time.Minute,
					cfg,
				)
				if err != nil {
					logger.WarnCF("livekit", "Failed to register cron tool for LiveKit agent", map[string]any{
						"room":  roomName,
						"error": err.Error(),
					})
				} else {
					agentInstance.Tools.Register(cronTool)
				}
			}
		}
		mcpCfg := scopedMCPConfigForWorkspace(cfg, agentInstance.Workspace)
		mcpManager, err := agent.RegisterMCPToolsForInstances(
			context.Background(),
			mcpCfg,
			agentInstance.Workspace,
			agentInstance,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error initializing MCP tools for LiveKit agent: %v\n", err)
			return nil
		}

		bridge, err := livekit.NewAgentBridge(livekit.AgentBridgeConfig{
			Config:             cfg,
			Provider:           provider,
			ModelID:            modelID,
			AgentInstance:      agentInstance,
			PreserveWorkspace:  preserveWorkspace,
			MaxIterations:      cfg.Agents.Defaults.MaxToolIterations,
			WorkspaceArtifacts: artifactStore,
			MCPManager:         mcpManager,
			OnClose: func() {
				if cronService != nil {
					cronService.Stop()
				}
				if strings.TrimSpace(deviceMAC) == "" || managerAPIBaseURL(lkCfg.ManagerAPI) == "" || workspace == "" {
					return
				}
				uploadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := uploadWorkspaceFiles(uploadCtx, lkCfg.ManagerAPI, deviceMAC, workspace); err != nil {
					logger.WarnCF("livekit", "workspace-files upload to manager failed", map[string]any{
						"room":       roomName,
						"device_mac": deviceMAC,
						"error":      err.Error(),
					})
				}
			},
		})
		if err != nil {
			if mcpManager != nil {
				_ = mcpManager.Close()
			}
			fmt.Fprintf(os.Stderr, "Error creating agent bridge: %v\n", err)
			return nil
		}
		if cronService != nil && cronTool != nil {
			cronSessionKey := livekitCronSessionKey(deviceMAC, persistentAgentID, roomName)
			cronExecutor := &livekitCronExecutor{
				bridge:     bridge,
				sessionKey: cronSessionKey,
			}
			cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
				if job == nil {
					return "", errors.New("cron job is nil")
				}
				cronTool.SetExecutor(cronExecutor)
				output := strings.TrimSpace(cronTool.ExecuteJob(context.Background(), job))

				// In LiveKit sessions, deliver=true reminders can legitimately return "ok"
				// after publishing to MessageBus. Ensure they are still spoken in-room.
				announcement := output
				if announcement == "" || strings.EqualFold(announcement, "ok") {
					if job.Payload.Deliver {
						if reminder := strings.TrimSpace(job.Payload.Message); reminder != "" {
							announcement = reminder
						}
					}
				}

				if announcement == "" || strings.EqualFold(announcement, "ok") {
					return output, nil
				}
				queued := bridge.EnqueueAsyncEvent(livekit.AsyncEvent{
					SessionKey: cronSessionKey,
					ToolName:   "cron",
					Result:     tools.SilentResult(announcement),
				})
				if !queued {
					logger.WarnCF("livekit", "Cron result async queue is full; dropping announcement", map[string]any{
						"room": roomName,
					})
				}
				return output, nil
			})
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
			_, metadataPayload, metadataSource := resolveLiveKitJobBootstrapContext(job)
			if strings.TrimSpace(metadataPayload) != "" {
				if md, err := parseRoomMetadataBootstrap(metadataPayload); err == nil {
					primaryLanguage = strings.TrimSpace(md.Metadata.PrimaryLanguage)
				} else {
					logger.WarnCF("livekit", "Failed to parse metadata for primary language", map[string]any{
						"error":           err.Error(),
						"metadata_source": metadataSource,
					})
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
				Runtime:         lkCfg.Runtime,
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

func normalizeLiveKitRuntimeConfig(rt *config.LiveKitServiceRuntimeConfig) {
	if rt == nil {
		return
	}

	greetingMode := strings.ToLower(strings.TrimSpace(rt.GreetingMode))
	switch greetingMode {
	case "", "dynamic":
		rt.GreetingMode = "dynamic"
	case "fallback", "disabled":
		rt.GreetingMode = greetingMode
	default:
		logger.WarnCF("livekit", "Invalid runtime greeting mode, defaulting to dynamic", map[string]any{
			"value": rt.GreetingMode,
		})
		rt.GreetingMode = "dynamic"
	}

	asyncMode := strings.ToLower(strings.TrimSpace(rt.AsyncAnnounceMode))
	switch asyncMode {
	case "", "immediate":
		rt.AsyncAnnounceMode = "immediate"
	case "queue", "silent_append":
		rt.AsyncAnnounceMode = asyncMode
	default:
		logger.WarnCF("livekit", "Invalid async announce mode, defaulting to immediate", map[string]any{
			"value": rt.AsyncAnnounceMode,
		})
		rt.AsyncAnnounceMode = "immediate"
	}

	if rt.VADThreshold <= 0 || rt.VADThreshold > 1 {
		if rt.VADThreshold != 0 {
			logger.WarnCF("livekit", "Invalid VAD threshold, defaulting to 0.7", map[string]any{
				"value": rt.VADThreshold,
			})
		}
		rt.VADThreshold = 0.7
	}
	if rt.VADEndpointMS <= 0 {
		rt.VADEndpointMS = 1000
	}
	if rt.VADEndpointMS < 200 {
		logger.WarnCF("livekit", "VAD endpoint too low; clamped to 200ms", map[string]any{"value": rt.VADEndpointMS})
		rt.VADEndpointMS = 200
	}
	if rt.VADEndpointMS > 5000 {
		logger.WarnCF("livekit", "VAD endpoint too high; clamped to 5000ms", map[string]any{"value": rt.VADEndpointMS})
		rt.VADEndpointMS = 5000
	}

	if rt.RateLimitCooldownSeconds <= 0 {
		rt.RateLimitCooldownSeconds = 120
	}
	if rt.ProviderFailureCooldownSec <= 0 {
		rt.ProviderFailureCooldownSec = 30
	}
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

func looksLikeUnrenderedTemplate(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return false
	}
	return strings.Contains(prompt, "{{") || strings.Contains(prompt, "{%")
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

type livekitCronExecutor struct {
	bridge     *livekit.AgentBridge
	sessionKey string
}

func (e *livekitCronExecutor) ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID string) (string, error) {
	if e == nil || e.bridge == nil {
		return "", errors.New("livekit cron executor bridge is nil")
	}
	key := strings.TrimSpace(e.sessionKey)
	if key == "" {
		key = strings.TrimSpace(sessionKey)
	}
	if key == "" {
		key = "livekit:cron"
	}

	var out strings.Builder
	done := make(chan struct{})
	_, err := e.bridge.ChatStream(ctx, key, content, func(chunk string) {
		out.WriteString(chunk)
	}, func() {
		close(done)
	})
	if err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-done:
	}

	result := strings.TrimSpace(out.String())
	if result == "" {
		result = "Scheduled task completed."
	}
	return result, nil
}

func livekitCronSessionKey(deviceMAC, agentID, roomName string) string {
	if mac := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(deviceMAC), ":", "")); mac != "" {
		return "livekit:device:" + mac
	}
	if aid := strings.TrimSpace(agentID); aid != "" {
		return "livekit:agent:" + routing.NormalizeAgentID(aid)
	}
	return "livekit:" + strings.TrimSpace(roomName) + ":cron"
}

func resolveLiveKitJobBootstrapContext(job *livekitproto.Job) (roomName, metadata, source string) {
	if job == nil {
		return "", "", "none"
	}

	if job.Room != nil {
		roomName = strings.TrimSpace(job.Room.Name)
		if roomMetadata := strings.TrimSpace(job.Room.Metadata); roomMetadata != "" {
			return roomName, roomMetadata, "room_metadata"
		}
	}

	if dispatchMetadata := strings.TrimSpace(job.Metadata); dispatchMetadata != "" {
		return roomName, dispatchMetadata, "job_metadata"
	}

	return roomName, "", "none"
}
