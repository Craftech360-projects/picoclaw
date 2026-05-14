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
	"strconv"
	"strings"
	"sync"
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

	workspaceBootstrap, err := ensureLiveKitDefaultWorkspaceTemplate(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error preparing default workspace template: %v\n", err)
		os.Exit(1)
	}
	logger.InfoCF("livekit", "Default workspace template ready", map[string]any{
		"workspace":       workspaceBootstrap.Workspace,
		"seeded_files":    workspaceBootstrap.SeededFiles,
		"skills_copied":   workspaceBootstrap.SkillsCopied,
		"existing_skills": workspaceBootstrap.ExistingSkills,
	})

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
		"language_lock_enabled":             lkCfg.Runtime.LanguageLockEnabled,
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
		}
		lockTimeout := liveKitWorkspaceLockTimeout(lkCfg.ManagerAPI)
		lockStaleAfter := 2 * time.Minute
		if lockTimeout > time.Minute {
			lockStaleAfter = lockTimeout * 2
		}
		var wsLock *workspaceLock
		var wsLockReleaseOnce sync.Once
		releaseWorkspaceLock := func(reason string) {
			wsLockReleaseOnce.Do(func() {
				if wsLock == nil {
					return
				}
				if err := wsLock.Release(); err != nil {
					logger.WarnCF("livekit", "Failed to release workspace lock", map[string]any{
						"room":       roomName,
						"device_mac": deviceMAC,
						"workspace":  workspace,
						"reason":     reason,
						"error":      err.Error(),
					})
					return
				}
				logger.InfoCF("livekit", "Released per-device workspace lock", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
					"workspace":  workspace,
					"reason":     reason,
				})
			})
		}
		if workspace != "" && strings.TrimSpace(deviceMAC) != "" {
			jobID := ""
			if job != nil {
				jobID = strings.TrimSpace(job.Id)
			}
			lockOwner := fmt.Sprintf("room=%s job=%s pid=%d", roomName, jobID, os.Getpid())
			lock, err := acquireWorkspaceLock(workspace, lockOwner, lockTimeout, lockStaleAfter)
			if err != nil && strings.Contains(err.Error(), "workspace lock busy") {
				// Reconnect races are expected when the previous room is still flushing
				// post-session persistence. Give handoff one extra grace window before
				// abandoning this assignment.
				if ok, hintOwner := livekit.HasRecentWorkspaceReconnectHint(workspace, 2*time.Minute); ok &&
					strings.TrimSpace(hintOwner) == lockOwner {
					retryTimeout := lockTimeout
					if retryTimeout < 20*time.Second {
						retryTimeout = 20 * time.Second
					}
					if retryTimeout > 120*time.Second {
						retryTimeout = 120 * time.Second
					}
					logger.InfoCF("livekit", "Retrying per-device workspace lock acquisition during reconnect handoff", map[string]any{
						"room":               roomName,
						"device_mac":         deviceMAC,
						"workspace":          workspace,
						"lock_owner":         lockOwner,
						"retry_timeout_ms":   retryTimeout.Milliseconds(),
						"initial_timeout_ms": lockTimeout.Milliseconds(),
					})
					lock, err = acquireWorkspaceLock(workspace, lockOwner, retryTimeout, lockStaleAfter)
				}
			}
			if err != nil {
				logger.WarnCF("livekit", "Failed to acquire per-device workspace lock", map[string]any{
					"room":            roomName,
					"device_mac":      deviceMAC,
					"workspace":       workspace,
					"lock_owner":      lockOwner,
					"lock_timeout_ms": lockTimeout.Milliseconds(),
					"error":           err.Error(),
				})
				return nil
			}
			wsLock = lock
			logger.InfoCF("livekit", "Acquired per-device workspace lock", map[string]any{
				"room":            roomName,
				"device_mac":      deviceMAC,
				"workspace":       workspace,
				"lock_owner":      lockOwner,
				"lock_timeout_ms": lockTimeout.Milliseconds(),
			})
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
		if bootstrap.Source == bootstrapSourceRoomMetadata {
			workspaceBootstrapSource = bootstrap.Source
		}
		sessionLanguagePolicy := livekit.NormalizeSessionLanguagePolicy(
			bootstrap.Metadata.SessionLanguageName,
			bootstrap.Metadata.SessionLanguageCode,
		)
		if strings.TrimSpace(bootstrap.Metadata.SessionLanguageName) == "" &&
			strings.TrimSpace(bootstrap.Metadata.SessionLanguageCode) == "" &&
			strings.TrimSpace(bootstrap.Metadata.PrimaryLanguage) != "" {
			sessionLanguagePolicy = livekit.NormalizeSessionLanguagePolicy(
				bootstrap.Metadata.PrimaryLanguage,
				bootstrap.Metadata.PrimaryLanguage,
			)
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
			fastPathTimeout := liveKitWorkspaceFastPathTimeout(lkCfg.ManagerAPI)
			fastCtx, fastCancel := context.WithTimeout(context.Background(), fastPathTimeout)
			defer fastCancel()
			if err := downloadWorkspaceFilesFastPath(fastCtx, lkCfg.ManagerAPI, deviceMAC, workspace); err != nil {
				logger.WarnCF("livekit", "workspace-files fast-path download from manager failed", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
					"timeout_ms": fastPathTimeout.Milliseconds(),
					"error":      err.Error(),
				})
			}
			if liveKitWorkspaceBackgroundRestoreEnabled(lkCfg.ManagerAPI) {
				go func(room string, mac string, dir string) {
					startedAt := time.Now()
					bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := downloadWorkspaceFiles(bgCtx, lkCfg.ManagerAPI, mac, dir); err != nil {
						logger.WarnCF("livekit", "workspace background full restore failed", map[string]any{
							"room":                            room,
							"device_mac":                      mac,
							"workspace_restore_background_ms": time.Since(startedAt).Milliseconds(),
							"error":                           err.Error(),
						})
						return
					}
					logger.InfoCF("livekit", "workspace background full restore completed", map[string]any{
						"room":                            room,
						"device_mac":                      mac,
						"workspace_restore_background_ms": time.Since(startedAt).Milliseconds(),
					})
				}(roomName, deviceMAC, workspace)
			} else {
				logger.InfoCF("livekit", "workspace background full restore disabled by config", map[string]any{
					"room":       roomName,
					"device_mac": deviceMAC,
				})
			}
		}
		stopWorkspaceSyncLoop := func() {}

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
		bridge, err := livekit.NewAgentBridge(livekit.AgentBridgeConfig{
			Config:              cfg,
			Provider:            provider,
			ModelID:             modelID,
			AgentInstance:       agentInstance,
			PreserveWorkspace:   preserveWorkspace,
			MaxIterations:       cfg.Agents.Defaults.MaxToolIterations,
			SessionLanguageName: sessionLanguagePolicy.DisplayName,
			SessionLanguageCode: sessionLanguagePolicy.RawCode,
			LanguageLockEnabled: lkCfg.Runtime.LanguageLockEnabled,
			WorkspaceArtifacts:  artifactStore,
			MCPManager:          nil,
			OnClose: func() {
				stopWorkspaceSyncLoop()
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
			OnAfterClose: func() {
				releaseWorkspaceLock("bridge_close")
			},
		})
		if err != nil {
			releaseWorkspaceLock("bridge_create_failed")
			fmt.Fprintf(os.Stderr, "Error creating agent bridge: %v\n", err)
			return nil
		}
		startLiveKitAsyncMCPInitialization(cfg, agentInstance, bridge, roomName, workspaceIdentity)
		if strings.TrimSpace(deviceMAC) != "" && managerAPIBaseURL(lkCfg.ManagerAPI) != "" && workspace != "" {
			stopWorkspaceSyncLoop = startWorkspaceSyncLoop(lkCfg.ManagerAPI, deviceMAC, workspace, roomName)
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
			if bridge == nil {
				return nil, errors.New("agent bridge is nil")
			}
			serverURL := lkCfg.ServerURL
			if assignment != nil && assignment.Url != nil && *assignment.Url != "" {
				serverURL = *assignment.Url
			}
			token := ""
			if assignment != nil {
				token = assignment.Token
			}
			sessionLanguagePolicy := bridge.SessionLanguagePolicy()

			// Get active STT provider for this session
			sttProvider := buildSTTProvider(sttFactory)

			return livekit.NewRoomSession(livekit.RoomSessionConfig{
				Worker:              worker,
				JobID:               job.Id,
				RoomInfo:            job.Room,
				Bridge:              bridge,
				ServerURL:           serverURL,
				Token:               token,
				STT:                 sttProvider,
				TTS:                 ttsProvider,
				APIKey:              lkCfg.APIKey(),
				APISecret:           lkCfg.APISecret(),
				AgentName:           *agentName,
				SampleRate:          ttsSampleRate,
				FillerWords:         lkCfg.TTS.FillerWords,
				PrimaryLanguage:     sessionLanguagePolicy.DisplayName,
				SessionLanguageName: sessionLanguagePolicy.DisplayName,
				SessionLanguageCode: sessionLanguagePolicy.RawCode,
				Runtime:             lkCfg.Runtime,
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

func startLiveKitAsyncMCPInitialization(
	cfg *config.Config,
	agentInstance *agent.AgentInstance,
	bridge *livekit.AgentBridge,
	roomName string,
	workspaceIdentity string,
) {
	if cfg == nil || agentInstance == nil || bridge == nil {
		return
	}
	if !cfg.Tools.IsToolEnabled("mcp") {
		return
	}

	workspacePath := strings.TrimSpace(agentInstance.Workspace)
	if workspacePath == "" {
		return
	}

	logger.InfoCF("livekit", "Starting async MCP initialization for LiveKit agent", map[string]any{
		"room":               roomName,
		"workspace_identity": workspaceIdentity,
		"workspace":          workspacePath,
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		mcpCfg := scopedMCPConfigForWorkspace(cfg, workspacePath)
		mcpManager, err := agent.RegisterMCPToolsForInstances(
			ctx,
			mcpCfg,
			workspacePath,
			agentInstance,
		)
		if err != nil {
			logger.WarnCF("livekit", "Async MCP initialization failed for LiveKit agent", map[string]any{
				"room":               roomName,
				"workspace_identity": workspaceIdentity,
				"error":              err.Error(),
			})
			return
		}
		if mcpManager == nil {
			logger.InfoCF("livekit", "Async MCP initialization skipped/no manager for LiveKit agent", map[string]any{
				"room":               roomName,
				"workspace_identity": workspaceIdentity,
			})
			return
		}
		if !bridge.AttachMCPManager(mcpManager) {
			logger.InfoCF("livekit", "Async MCP manager ready after bridge close; manager closed", map[string]any{
				"room":               roomName,
				"workspace_identity": workspaceIdentity,
			})
			return
		}

		logger.InfoCF("livekit", "Async MCP initialization completed for LiveKit agent", map[string]any{
			"room":               roomName,
			"workspace_identity": workspaceIdentity,
		})
	}()
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

func liveKitWorkspaceLockTimeout(cfg config.LiveKitServiceManagerAPIConfig) time.Duration {
	const defaultSeconds = 30
	if cfg.WorkspaceSync.LockTimeoutSecond > 0 {
		seconds := cfg.WorkspaceSync.LockTimeoutSecond
		if seconds > 300 {
			seconds = 300
		}
		return time.Duration(seconds) * time.Second
	}
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_WORKSPACE_LOCK_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultSeconds * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultSeconds * time.Second
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func liveKitWorkspaceSyncInterval(cfg config.LiveKitServiceManagerAPIConfig) time.Duration {
	const defaultSeconds = 180
	if cfg.WorkspaceSync.IntervalSeconds > 0 {
		seconds := cfg.WorkspaceSync.IntervalSeconds
		if seconds < 30 {
			seconds = 30
		}
		if seconds > 3600 {
			seconds = 3600
		}
		return time.Duration(seconds) * time.Second
	}
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_WORKSPACE_SYNC_INTERVAL_SECONDS"))
	if raw == "" {
		return defaultSeconds * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultSeconds * time.Second
	}
	if seconds < 30 {
		seconds = 30
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func liveKitWorkspaceSyncRetryInterval(cfg config.LiveKitServiceManagerAPIConfig) time.Duration {
	const defaultSeconds = 30
	if cfg.WorkspaceSync.OutboxRetrySecond > 0 {
		seconds := cfg.WorkspaceSync.OutboxRetrySecond
		if seconds < 5 {
			seconds = 5
		}
		if seconds > 600 {
			seconds = 600
		}
		return time.Duration(seconds) * time.Second
	}
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_WORKSPACE_SYNC_RETRY_SECONDS"))
	if raw == "" {
		return defaultSeconds * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultSeconds * time.Second
	}
	if seconds < 5 {
		seconds = 5
	}
	if seconds > 600 {
		seconds = 600
	}
	return time.Duration(seconds) * time.Second
}

func liveKitWorkspaceFastPathTimeout(cfg config.LiveKitServiceManagerAPIConfig) time.Duration {
	const defaultMs = 1200
	timeoutMS := cfg.WorkspaceRestore.FastPathTimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = defaultMs
	}
	if timeoutMS < 200 {
		timeoutMS = 200
	}
	if timeoutMS > 10_000 {
		timeoutMS = 10_000
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func liveKitWorkspaceBackgroundRestoreEnabled(cfg config.LiveKitServiceManagerAPIConfig) bool {
	// default enabled when unset
	if !cfg.WorkspaceRestore.BackgroundEnabled &&
		cfg.WorkspaceRestore.FastPathTimeoutMS == 0 &&
		cfg.WorkspaceRestore.HistoryPageSize == 0 &&
		cfg.WorkspaceRestore.MaxHistoryPagesOnIdle == 0 {
		return true
	}
	return cfg.WorkspaceRestore.BackgroundEnabled
}

func hasPendingWorkspaceSync(workspace string) bool {
	path := workspaceSyncPendingPath(workspace)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func startWorkspaceSyncLoop(
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspace string,
	roomName string,
) func() {
	if !workspaceSyncEnabled(&cfg) {
		logger.InfoCF("livekit", "workspace sync loop disabled by config", map[string]any{
			"room":       roomName,
			"device_mac": deviceMAC,
		})
		return func() {}
	}
	interval := liveKitWorkspaceSyncInterval(cfg)
	retryInterval := liveKitWorkspaceSyncRetryInterval(cfg)
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		checkpointTicker := time.NewTicker(interval)
		defer checkpointTicker.Stop()
		retryTicker := time.NewTicker(retryInterval)
		defer retryTicker.Stop()

		syncNow := func(trigger string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := uploadWorkspaceFiles(ctx, cfg, deviceMAC, workspace); err != nil {
				outboxSize := len(getWorkspaceOutboxEntries(workspace))
				logger.WarnCF("livekit", "Workspace sync loop upload failed", map[string]any{
					"room":                       roomName,
					"device_mac":                 deviceMAC,
					"workspace":                  workspace,
					"trigger":                    trigger,
					"workspace_sync_outbox_size": outboxSize,
					"error":                      err.Error(),
				})
				return
			}
			outboxSize := len(getWorkspaceOutboxEntries(workspace))
			logger.InfoCF("livekit", "Workspace sync loop upload completed", map[string]any{
				"room":                       roomName,
				"device_mac":                 deviceMAC,
				"workspace":                  workspace,
				"trigger":                    trigger,
				"workspace_sync_outbox_size": outboxSize,
			})
		}

		if hasPendingWorkspaceSync(workspace) {
			syncNow("startup_pending")
		}

		for {
			select {
			case <-stopCh:
				return
			case <-checkpointTicker.C:
				syncNow("periodic_checkpoint")
			case <-retryTicker.C:
				if hasPendingWorkspaceSync(workspace) {
					syncNow("pending_retry")
				}
			}
		}
	}()

	return func() {
		close(stopCh)
		<-doneCh
	}
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
