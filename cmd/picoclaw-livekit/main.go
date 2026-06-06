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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/joho/godotenv"
	livekitproto "github.com/livekit/protocol/livekit"
	lklogger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
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
	strictStartup := liveKitStrictConfigEnabled()
	if strictStartup {
		if err := validateLiveKitStartupConfigFiles(cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error validating startup config files: %v\n", err)
			os.Exit(1)
		}
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	if err := validateLiveKitStartupCredentials(cfg, strictStartup); err != nil {
		fmt.Fprintf(os.Stderr, "Error validating startup config credentials: %v\n", err)
		os.Exit(1)
	}
	logger.SetLevelFromString(*logLevel)
	if changed, originalWorkspace, normalizedWorkspace, reason := normalizeLiveKitStartupWorkspace(cfg); changed {
		logger.WarnCF("livekit", "Normalized startup workspace path for current OS", map[string]any{
			"reason":               reason,
			"original_workspace":   originalWorkspace,
			"normalized_workspace": normalizedWorkspace,
			"goos":                 runtime.GOOS,
		})
	}
	configureLiveKitSDKLogger()
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

	lkCfg := cfg.LiveKitService
	if lkCfg.ServerURL == "" {
		fmt.Fprintln(os.Stderr, "Error: livekit_service.server_url is required")
		os.Exit(1)
	}
	startupProvider, startupModelID, err := providers.CreateProvider(cfg)
	if err != nil {
		if strings.TrimSpace(managerAPIBaseURL(lkCfg.ManagerAPI)) != "" {
			logger.WarnCF("livekit", "Startup provider initialization failed; manager DB-first mode enabled", map[string]any{
				"error": err.Error(),
			})
			startupProvider = nil
			startupModelID = ""
		} else {
			fmt.Fprintf(os.Stderr, "Error creating provider: %v\n", err)
			os.Exit(1)
		}
	} else {
		logger.InfoCF("livekit", "Finished provider initialization", map[string]any{
			"model": startupModelID,
		})
	}

	normalizeLiveKitRuntimeConfig(&lkCfg.Runtime)
	voiceMaxTokens := liveKitVoiceMaxTokens()
	logger.InfoCF("livekit", "LiveKit runtime policy", map[string]any{
		"greeting_mode":                     lkCfg.Runtime.GreetingMode,
		"async_announce_mode":               lkCfg.Runtime.AsyncAnnounceMode,
		"vad_threshold":                     lkCfg.Runtime.VADThreshold,
		"vad_endpoint_ms":                   lkCfg.Runtime.VADEndpointMS,
		"voice_max_tokens":                  voiceMaxTokens,
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
	managerAPIConfigured := strings.TrimSpace(managerAPIBaseURL(lkCfg.ManagerAPI)) != ""
	logger.InfoCF("livekit", "Configured fallback TTS provider", map[string]any{
		"fallback_provider":             lkCfg.TTS.Provider,
		"fallback_voice_id":             lkCfg.TTS.VoiceID,
		"fallback_model_id":             lkCfg.TTS.ModelID,
		"fallback_sample_rate_hz":       ttsSampleRate,
		"manager_api_configured":        managerAPIConfigured,
		"manager_api_overrides_session": managerAPIConfigured,
		"has_inworld_key":               strings.TrimSpace(lkCfg.InworldAPIKey()) != "",
		"has_cartesia_key":              strings.TrimSpace(lkCfg.CartesiaAPIKey()) != "",
		"has_elevenlabs_key":            strings.TrimSpace(cfg.Voice.ElevenLabsAPIKey) != "",
		"fallback_tts_enabled":          ttsProvider != nil,
	})
	logStartupManagerActiveTTSProvider(lkCfg)
	type roomRuntimeSelection struct {
		ttsProvider   tts.Provider
		ttsSampleRate int
	}
	var roomRuntimeByJobID sync.Map

	bridgeFactory := func(job *livekitproto.Job) *livekit.AgentBridge {
		sessionCfg := cfg
		sessionProvider := startupProvider
		sessionModelID := startupModelID
		sessionSelectionSource := "startup_config_fallback"
		if resolvedCfg, source, err := resolveLiveKitProviderConfigForSession(cfg, lkCfg.ManagerAPI); resolvedCfg != nil {
			sessionCfg = resolvedCfg
			sessionSelectionSource = source
			resolvedProvider, resolvedModelID, providerErr := providers.CreateProvider(sessionCfg)
			if providerErr != nil {
				if startupProvider != nil {
					logger.WarnCF("livekit", "Manager provider selection failed; using startup LLM provider fallback", map[string]any{
						"source": source,
						"error":  providerErr.Error(),
					})
				} else {
					logger.ErrorCF("livekit", "Manager provider selection failed and no startup fallback provider exists", map[string]any{
						"source": source,
						"error":  providerErr.Error(),
					})
					if err != nil {
						logger.ErrorCF("livekit", "Manager provider fetch warning", map[string]any{
							"source": source,
							"error":  err.Error(),
						})
					}
					return nil
				}
				if err != nil {
					logger.WarnCF("livekit", "Manager provider fetch warning", map[string]any{
						"source": source,
						"error":  err.Error(),
					})
				}
			} else {
				sessionProvider = resolvedProvider
				sessionModelID = resolvedModelID
			}
		}
		if sessionProvider == nil {
			logger.ErrorCF("livekit", "No LLM provider available for LiveKit room", map[string]any{
				"source": sessionSelectionSource,
			})
			return nil
		}
		sessionTTSProvider, sessionTTSSampleRate := buildTTSProvider(sessionCfg, sessionCfg.LiveKitService)
		if sessionTTSProvider == nil {
			sessionTTSProvider = ttsProvider
			sessionTTSSampleRate = ttsSampleRate
		}
		if job != nil && strings.TrimSpace(job.Id) != "" {
			roomRuntimeByJobID.Store(job.Id, roomRuntimeSelection{
				ttsProvider:   sessionTTSProvider,
				ttsSampleRate: sessionTTSSampleRate,
			})
		}
		logger.InfoCF("livekit", "Resolved per-session provider selection", map[string]any{
			"source":             sessionSelectionSource,
			"llm_model_name":     sessionCfg.Agents.Defaults.ModelName,
			"llm_model":          sessionModelID,
			"llm_api_base":       resolvedModelAPIBase(sessionCfg, sessionCfg.Agents.Defaults.ModelName),
			"stt_provider":       sessionCfg.LiveKitService.STT.Provider,
			"stt_model":          sessionCfg.LiveKitService.STT.Model,
			"stt_language":       sessionCfg.LiveKitService.STT.Language,
			"tts_provider":       sessionCfg.LiveKitService.TTS.Provider,
			"tts_model_id":       sessionCfg.LiveKitService.TTS.ModelID,
			"tts_voice_id":       sessionCfg.LiveKitService.TTS.VoiceID,
			"tts_output_format":  sessionCfg.LiveKitService.TTS.OutputFormat,
			"tts_sample_rate_hz": sessionTTSSampleRate,
		})

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
		reconnectLockTimeout := liveKitWorkspaceReconnectLockTimeout(lkCfg.ManagerAPI)
		reconnectLockStaleAfter := liveKitWorkspaceReconnectLockStaleAfter(lkCfg.ManagerAPI)
		lockTTLSeconds := liveKitWorkspaceLockLeaseTTL(lkCfg.ManagerAPI)
		var wsLock *workspaceLock
		var managerLockLease *managerWorkspaceLockLease
		var wsLockReleaseOnce sync.Once
		releaseWorkspaceLock := func(reason string) {
			wsLockReleaseOnce.Do(func() {
				if managerLockLease != nil {
					managerLockLease.Release(reason)
					managerLockLease = nil
				}
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
			if managerAPIBaseURL(lkCfg.ManagerAPI) != "" {
				lockCtx, lockCancel := context.WithTimeout(context.Background(), lockTimeout)
				lease, err := acquireManagerWorkspaceLockWithRetry(
					lockCtx,
					lkCfg.ManagerAPI,
					deviceMAC,
					lockOwner,
					lockTimeout,
					lockTTLSeconds,
				)
				lockCancel()
				if err != nil {
					logger.WarnCF("livekit", "Failed to acquire manager distributed workspace lock", map[string]any{
						"room":             roomName,
						"device_mac":       deviceMAC,
						"workspace":        workspace,
						"lock_owner":       lockOwner,
						"lock_timeout_ms":  lockTimeout.Milliseconds(),
						"lock_ttl_seconds": lockTTLSeconds,
						"error":            err.Error(),
					})
					return nil
				}
				managerLockLease = lease
				if lease != nil {
					logger.InfoCF("livekit", "Acquired manager distributed workspace lock", map[string]any{
						"room":             roomName,
						"device_mac":       deviceMAC,
						"workspace":        workspace,
						"lock_owner":       lockOwner,
						"lock_timeout_ms":  lockTimeout.Milliseconds(),
						"lock_ttl_seconds": lockTTLSeconds,
						"fencing_token":    lease.fencingToken,
					})
				}
			}
			lock, err := acquireWorkspaceLock(workspace, lockOwner, lockTimeout, lockStaleAfter)
			if err != nil && strings.Contains(err.Error(), "workspace lock busy") {
				// Reconnect races are expected when the previous room is still flushing
				// post-session persistence. Give handoff one extra grace window before
				// abandoning this assignment.
				if ok, hintOwner := livekit.HasRecentWorkspaceReconnectHint(workspace, 2*time.Minute); ok &&
					strings.TrimSpace(hintOwner) == lockOwner {
					logger.InfoCF("livekit", "Retrying per-device workspace lock acquisition during reconnect handoff", map[string]any{
						"room":               roomName,
						"device_mac":         deviceMAC,
						"workspace":          workspace,
						"lock_owner":         lockOwner,
						"retry_timeout_ms":   reconnectLockTimeout.Milliseconds(),
						"stale_reclaim_ms":   reconnectLockStaleAfter.Milliseconds(),
						"initial_timeout_ms": lockTimeout.Milliseconds(),
					})
					lock, err = acquireWorkspaceLock(workspace, lockOwner, reconnectLockTimeout, reconnectLockStaleAfter)
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
			firstTimeWorkspace := false
			if _, err := os.Stat(filepath.Join(workspace, "USER.md")); os.IsNotExist(err) {
				firstTimeWorkspace = true
			}
			hydrationOptions.FirstTimeWorkspace = firstTimeWorkspace
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

		agentInstance := agent.NewAgentInstance(agentCfg, &sessionCfg.Agents.Defaults, sessionCfg, sessionProvider)
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
		singleAgentRegistry := agent.NewAgentRegistry(sessionCfg, sessionProvider)
		agent.RegisterSharedTools(agent.SharedToolDependencies{
			Config:   sessionCfg,
			Registry: singleAgentRegistry,
			Provider: sessionProvider,
		})

		if defaultAgent := singleAgentRegistry.GetDefaultAgent(); defaultAgent != nil {
			for _, toolName := range defaultAgent.Tools.List() {
				if !isLiveKitVoiceAllowedTool(toolName) {
					continue
				}
				if t, ok := defaultAgent.Tools.Get(toolName); ok {
					agentInstance.Tools.Register(t)
				}
			}
		}
		if added := ensureLiveKitWorkspaceFileTools(agentInstance, &sessionCfg.Agents.Defaults, sessionCfg); len(added) > 0 {
			logger.WarnCF("livekit", "Forced required workspace file tools for LiveKit agent", map[string]any{
				"room":               roomName,
				"workspace_identity": workspaceIdentity,
				"tools":              added,
			})
		}
		var cronService *cron.CronService
		var cronTool *tools.CronTool
		if sessionCfg.Tools.IsToolEnabled("cron") {
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
					sessionCfg.Agents.Defaults.RestrictToWorkspace,
					time.Duration(sessionCfg.Tools.Cron.ExecTimeoutMinutes)*time.Minute,
					sessionCfg,
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
			Config:            sessionCfg,
			Provider:          sessionProvider,
			ModelID:           sessionModelID,
			AgentInstance:     agentInstance,
			PreserveWorkspace: preserveWorkspace,
			MaxIterations:     sessionCfg.Agents.Defaults.MaxToolIterations,
			LLMOptions: map[string]any{
				"max_tokens":  voiceMaxTokens,
				"temperature": 0.3,
			},
			SessionLanguageName: sessionLanguagePolicy.DisplayName,
			SessionLanguageCode: sessionLanguagePolicy.RawCode,
			LanguageLockEnabled: lkCfg.Runtime.LanguageLockEnabled,
			AllowedToolNames:    liveKitVoiceToolAllowlist(),
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
		startLiveKitAsyncMCPInitialization(sessionCfg, agentInstance, bridge, roomName, workspaceIdentity)
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
			sessionTTSProvider := ttsProvider
			sessionTTSSampleRate := ttsSampleRate
			if job != nil {
				if selected, ok := roomRuntimeByJobID.LoadAndDelete(job.Id); ok {
					if runtimeSelection, castOK := selected.(roomRuntimeSelection); castOK {
						if runtimeSelection.ttsProvider != nil {
							sessionTTSProvider = runtimeSelection.ttsProvider
						}
						if runtimeSelection.ttsSampleRate > 0 {
							sessionTTSSampleRate = runtimeSelection.ttsSampleRate
						}
					}
				}
			}

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
				TTS:                 sessionTTSProvider,
				APIKey:              lkCfg.APIKey(),
				APISecret:           lkCfg.APISecret(),
				AgentName:           *agentName,
				SampleRate:          sessionTTSSampleRate,
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
		worker.BeginDraining()
		drainTimeout := liveKitDrainTimeout()
		if err := worker.WaitForDrain(drainTimeout); err != nil {
			logger.WarnCF("livekit", "Worker drain timeout reached; forcing shutdown", map[string]any{
				"drain_timeout_ms": drainTimeout.Milliseconds(),
				"error":            err.Error(),
			})
		}
		worker.Shutdown()
		cancel()
	}()

	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
		os.Exit(1)
	}
}

func configureLiveKitSDKLogger() {
	sdkLevel := strings.ToLower(strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_SDK_LOG_LEVEL")))
	if sdkLevel == "" {
		// Keep SDK/WebRTC internals quiet by default; app-level logs remain unchanged.
		sdkLevel = "error"
	}
	conf := &lklogger.Config{
		Level: sdkLevel,
	}
	lkLog, err := lklogger.NewZapLogger(conf)
	if err != nil {
		lksdk.SetLogger(lklogger.GetDiscardLogger())
		logger.WarnCF("livekit", "Failed to configure LiveKit SDK logger; falling back to discard logger", map[string]any{
			"error": err.Error(),
		})
		return
	}
	lksdk.SetLogger(lkLog.WithName("livekit-sdk"))
	logger.InfoCF("livekit", "Configured LiveKit SDK logger", map[string]any{
		"sdk_log_level": sdkLevel,
	})
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

func normalizeLiveKitStartupWorkspace(cfg *config.Config) (changed bool, originalWorkspace, normalizedWorkspace, reason string) {
	return normalizeLiveKitStartupWorkspaceForGOOS(cfg, runtime.GOOS)
}

func normalizeLiveKitStartupWorkspaceForGOOS(cfg *config.Config, goos string) (changed bool, originalWorkspace, normalizedWorkspace, reason string) {
	if cfg == nil {
		return false, "", "", ""
	}
	originalWorkspace = strings.TrimSpace(cfg.Agents.Defaults.Workspace)
	workspace := originalWorkspace

	if workspace == "" {
		normalizedWorkspace = defaultWorkspacePathForStartup()
		cfg.Agents.Defaults.Workspace = normalizedWorkspace
		return true, originalWorkspace, normalizedWorkspace, "empty_workspace"
	}

	if goos != "windows" && looksLikeWindowsAbsolutePath(workspace) {
		normalizedWorkspace = defaultWorkspacePathForStartup()
		cfg.Agents.Defaults.Workspace = normalizedWorkspace
		return true, originalWorkspace, normalizedWorkspace, "windows_absolute_path_on_non_windows"
	}

	return false, originalWorkspace, workspace, ""
}

func defaultWorkspacePathForStartup() string {
	home := strings.TrimSpace(os.Getenv(config.EnvHome))
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, pkg.DefaultPicoClawHome)
	}
	return filepath.Join(home, pkg.WorkspaceName)
}

func looksLikeWindowsAbsolutePath(path string) bool {
	if len(path) < 3 {
		return false
	}
	drive := path[0]
	if !((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) {
		return false
	}
	if path[1] != ':' {
		return false
	}
	sep := path[2]
	return sep == '\\' || sep == '/'
}

func liveKitStrictConfigEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_STRICT_CONFIG"))
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

func validateLiveKitStartupConfigFiles(cfgPath string) error {
	cfgPath = strings.TrimSpace(cfgPath)
	if cfgPath == "" {
		return fmt.Errorf("config path is empty")
	}
	cfgInfo, err := os.Stat(cfgPath)
	if err != nil {
		return fmt.Errorf("config file is not readable at %s: %w", cfgPath, err)
	}
	if cfgInfo.IsDir() {
		return fmt.Errorf("config path is a directory, expected file: %s", cfgPath)
	}
	secPath := filepath.Join(filepath.Dir(cfgPath), config.SecurityConfigFile)
	secInfo, err := os.Stat(secPath)
	if err != nil {
		return fmt.Errorf("security file is not readable at %s: %w", secPath, err)
	}
	if secInfo.IsDir() {
		return fmt.Errorf("security path is a directory, expected file: %s", secPath)
	}
	return nil
}

func validateLiveKitStartupCredentials(cfg *config.Config, strict bool) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	lkCfg := cfg.LiveKitService
	if strings.TrimSpace(lkCfg.ServerURL) == "" {
		return fmt.Errorf("livekit_service.server_url is required")
	}
	if strict {
		if strings.TrimSpace(lkCfg.APIKey()) == "" {
			return fmt.Errorf("livekit_service.api_key is required in strict mode")
		}
		if strings.TrimSpace(lkCfg.APISecret()) == "" {
			return fmt.Errorf("livekit_service.api_secret is required in strict mode")
		}
		if managerAPIBaseURL(lkCfg.ManagerAPI) != "" && strings.TrimSpace(managerAPIServiceKey()) == "" {
			return fmt.Errorf("manager API service key is required in strict mode when manager_api.base_url is configured")
		}
	}
	return nil
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

func logStartupManagerActiveTTSProvider(lkCfg config.LiveKitServiceConfig) {
	if strings.TrimSpace(managerAPIBaseURL(lkCfg.ManagerAPI)) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	active, err := fetchManagerActiveProviders(ctx, lkCfg.ManagerAPI, managerAPIServiceKey())
	if err != nil {
		logger.WarnCF("livekit", "Manager active TTS provider lookup failed", map[string]any{
			"source": "manager_api",
			"error":  err.Error(),
		})
		return
	}

	liveKitActiveProvidersCache.mu.Lock()
	liveKitActiveProvidersCache.data = active
	liveKitActiveProvidersCache.expiresAt = time.Now().Add(liveKitProviderCacheTTL())
	liveKitActiveProvidersCache.hasData = true
	liveKitActiveProvidersCache.mu.Unlock()

	if strings.TrimSpace(active.TTS.Provider) == "" {
		logger.WarnCF("livekit", "Manager active TTS provider is empty", map[string]any{
			"source": "manager_api",
		})
		return
	}

	logger.InfoCF("livekit", "Manager active TTS provider", map[string]any{
		"source":             "manager_api",
		"tts_provider":       active.TTS.Provider,
		"tts_model_id":       active.TTS.ModelID,
		"tts_voice_id":       active.TTS.VoiceID,
		"tts_output_format":  active.TTS.OutputFormat,
		"tts_sample_rate_hz": active.TTS.SampleRateHz,
		"tts_api_key_set":    strings.TrimSpace(active.TTS.APIKey) != "",
	})
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

func liveKitWorkspaceReconnectLockTimeout(cfg config.LiveKitServiceManagerAPIConfig) time.Duration {
	const defaultSeconds = 8
	timeout := defaultSeconds * time.Second
	if cfg.WorkspaceSync.LockTimeoutSecond > 0 {
		seconds := cfg.WorkspaceSync.LockTimeoutSecond
		if seconds > 0 && seconds < defaultSeconds {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_RECONNECT_LOCK_TIMEOUT_SECONDS"))
	if raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	if timeout < 3*time.Second {
		timeout = 3 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	return timeout
}

func liveKitWorkspaceReconnectLockStaleAfter(cfg config.LiveKitServiceManagerAPIConfig) time.Duration {
	const defaultSeconds = 8
	staleAfter := defaultSeconds * time.Second
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_RECONNECT_LOCK_STALE_SECONDS"))
	if raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			staleAfter = time.Duration(seconds) * time.Second
		}
	}
	if staleAfter < 5*time.Second {
		staleAfter = 5 * time.Second
	}
	if staleAfter > 30*time.Second {
		staleAfter = 30 * time.Second
	}
	return staleAfter
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

func liveKitDrainTimeout() time.Duration {
	const defaultSeconds = 900
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_DRAIN_TIMEOUT_SECONDS"))
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

func liveKitVoiceMaxTokens() int {
	const defaultTokens = 120
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_VOICE_MAX_TOKENS"))
	if raw == "" {
		return defaultTokens
	}
	tokens, err := strconv.Atoi(raw)
	if err != nil || tokens <= 0 {
		return defaultTokens
	}
	if tokens > 1024 {
		tokens = 1024
	}
	return tokens
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

func resolvedModelAPIBase(cfg *config.Config, modelName string) string {
	if cfg == nil {
		return ""
	}
	name := strings.TrimSpace(modelName)
	for _, item := range cfg.ModelList {
		if item == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.ModelName), name) {
			return strings.TrimSpace(item.APIBase)
		}
	}
	return ""
}
