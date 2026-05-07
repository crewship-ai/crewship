package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	api "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/server"
	bundledSkills "github.com/crewship-ai/crewship/internal/skills/bundled"
	"github.com/crewship-ai/crewship/internal/ws"
	"github.com/crewship-ai/crewship/web"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Crewship server",
	Long:  "Start the Crewship server with optional configuration flags.",
	RunE: func(cmd *cobra.Command, args []string) error {
		configPath, _ := cmd.Flags().GetString("config")
		dbURL, _ := cmd.Flags().GetString("db")
		noDocker, _ := cmd.Flags().GetBool("no-docker")

		detectCtx, detectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer detectCancel()
		if !noDocker && !checkAnyRuntime(detectCtx) {
			// Print the help text to stderr, then return a short error.
			// Avoids ST1005 (error strings should not end with punctuation/newlines)
			// while preserving the full user-facing guidance.
			fmt.Fprintln(os.Stderr,
				"Crewship requires a container runtime to run AI agents.\n"+
					"Supported: Docker, Podman, Colima, OrbStack, Rancher Desktop, Apple Containers\n\n"+
					"Install Docker Desktop:    https://docs.docker.com/get-docker/\n"+
					"Install Podman:            https://podman.io/docs/installation\n"+
					"Install Apple Containers:  brew install container (macOS 26+)\n\n"+
					"To start without containers (dashboard only, no agents):\n"+
					"  crewship start --no-docker\n\n"+
					"Run 'crewship doctor' for full diagnostics")
			return fmt.Errorf("no container runtime found")
		}

		bootstrapLogger := logging.New("info", "json", os.Stdout)
		slog.SetDefault(bootstrapLogger)

		cfg, err := config.Load(configPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		debugBuffer := logging.NewRingBuffer(500)
		innerLogger := logging.New(cfg.Logging.Level, "json", os.Stdout)
		ringHandler := logging.NewRingHandler(innerLogger.Handler(), debugBuffer)
		logger := slog.New(ringHandler)
		slog.SetDefault(logger)

		databaseURL := dbURL
		if databaseURL == "" {
			databaseURL = os.Getenv("DATABASE_URL")
		}
		if databaseURL == "" {
			dataDir, err := database.DefaultDataDir()
			if err != nil {
				return fmt.Errorf("failed to create data directory: %w", err)
			}
			databaseURL = dataDir.DatabaseURL()
			cfg.Storage.BasePath = dataDir.OutputDir()
			cfg.Storage.LogPath = dataDir.LogsDir()
		}

		db, err := database.Open(databaseURL)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer db.Close()

		if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
			return fmt.Errorf("failed to run migrations: %w", err)
		}
		if err := database.SeedBundledSkills(context.Background(), db.DB, logger); err != nil {
			logger.Warn("failed to seed bundled skills", "error", err)
		}
		if err := bundledSkills.Install(context.Background(), db.DB, logger); err != nil {
			logger.Warn("failed to install bundled anthropic skills", "error", err)
		}

		lic := license.New()
		if cfg.License.FilePath != "" {
			if err := lic.LoadFromFile(cfg.License.FilePath); err != nil {
				logger.Warn("failed to load license file, using community defaults", "error", err, "path", cfg.License.FilePath)
			} else {
				c := lic.Claims()
				logger.Info("license loaded",
					"edition", c.Edition,
					"licensee", c.LicenseeOrg,
					"max_crews", c.MaxCrews,
					"max_agents_per_crew", c.MaxAgents,
					"max_members", c.MaxMembers,
				)
			}
		} else {
			logger.Info("no license file configured, using community defaults",
				"max_crews", lic.MaxCrews(),
				"max_agents_per_crew", lic.MaxAgentsPerCrew(),
				"max_members", lic.MaxMembers(),
			)
		}

		logger.Info("crewship starting",
			"version", version,
			"database", db.Path(),
			"container_provider", cfg.Container.Provider,
			"storage_provider", cfg.Storage.Provider,
			"state_provider", cfg.State.Provider,
			"http_addr", cfg.Server.Host+":"+strconv.Itoa(cfg.Server.Port),
			"ipc_socket", cfg.IPC.SocketPath,
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ctx = logging.WithContext(ctx, logger)

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sig
			logger.Info("received shutdown signal")
			cancel()
		}()

		deps, err := initProviders(ctx, cfg, logger, noDocker)
		if err != nil {
			return fmt.Errorf("failed to initialize providers: %w", err)
		}
		defer deps.Close()
		deps.DebugLogs = debugBuffer
		deps.DB = db.DB
		deps.License = lic

		webFS, err := web.FS()
		if err != nil {
			logger.Warn("embedded web UI not available", "error", err)
		} else {
			deps.WebFS = webFS
		}

		srv := server.New(cfg, logger, deps)

		resolver := chatbridge.NewIPCResolver(cfg.Auth.NextjsURL, cfg.Auth.InternalToken, logger)
		bridge := chatbridge.New(
			srv.Orchestrator(),
			deps.Container,
			srv.ConversationStore(),
			srv.LogWriter(),
			resolver,
			chatbridge.BridgeConfig{
				DefaultMemoryMB: cfg.Container.DefaultMemoryMB,
				DefaultCPUs:     cfg.Container.DefaultCPUs,
			},
			logger,
		)
		srv.SetChatHandler(bridge)

		// Wire the API router's ProvisioningHandler into chatbridge so the
		// "send first message at unprovisioned crew" path can auto-trigger
		// the build instead of erroring out. The result-shape adapter exists
		// purely to avoid an api → chatbridge import (api already depends on
		// chatbridge for ChatHandler, so the dep flows the other way).
		if apiRouter := srv.APIRouter(); apiRouter != nil {
			if ph := apiRouter.Provisioning(); ph != nil {
				bridge.SetProvisioningEnqueuer(provisioningAdapter{h: ph})
			}
		}

		// V-01: Wire up channel authorizer for WebSocket subscription access control
		if deps.DB != nil {
			srv.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(deps.DB))
		}

		// Start agent scheduler (cron-based scheduled runs)
		if deps.DB != nil && deps.Container != nil {
			sched := scheduler.New(
				deps.DB, srv.Orchestrator(), deps.Container,
				resolver, srv.LogWriter(), srv.ConversationStore(),
				scheduler.Config{
					DefaultMemoryMB: cfg.Container.DefaultMemoryMB,
					DefaultCPUs:     cfg.Container.DefaultCPUs,
				},
				logger,
			)
			if err := sched.Start(ctx); err != nil {
				logger.Error("scheduler failed to start", "error", err)
			} else {
				defer sched.Stop()
				if apiRouter := srv.APIRouter(); apiRouter != nil {
					apiRouter.SetScheduler(sched)
				}
			}
		}

		// Wire the pipeline AgentRunner. Pipelines route every step
		// through the same orchestrator path the scheduler uses —
		// the agent runs in its real container, with its real CLI
		// adapter (Claude Code / Codex / Gemini / etc.), no raw
		// LLM API key required. This is the "reuse the firm's own
		// employees" model: the agent the author crew already
		// configured for chat use also handles pipeline steps.
		//
		// Wired here (not in server.go) because the runner needs
		// chatbridge.ChatResolver, which is constructed in this
		// file. The router exposes PipelinesHandler so the runner
		// can be plugged in post-router-construction.
		if deps.DB != nil && deps.Container != nil && srv.APIRouter() != nil && srv.APIRouter().PipelinesHandler != nil {
			pipeRunner, err := pipeline.NewOrchestratorRunner(pipeline.OrchestratorRunnerDeps{
				DB:        deps.DB,
				Orch:      srv.Orchestrator(),
				Container: deps.Container,
				Resolver:  resolver,
				LogWriter: srv.LogWriter(),
				ConvStore: srv.ConversationStore(),
				Logger:    logger,
			})
			if err != nil {
				logger.Error("pipeline orchestrator runner construct failed", "error", err)
			} else {
				srv.APIRouter().PipelinesHandler.SetRunner(pipeRunner)
				logger.Info("pipeline runner wired (orchestrator mode — agent runs in its container via CLI adapter)")
			}

			// Wire production WaitpointStore so StepWait approvals
			// persist across restarts and the inbox UI can fire
			// /pipelines/waitpoints/{token}/approve. Without this,
			// approval steps timeout after 60s in-memory only.
			if deps.DB != nil {
				wpStore := pipeline.NewSQLWaitpointStore(deps.DB)
				defer wpStore.Close()
				srv.APIRouter().PipelinesHandler.SetWaitpointStore(wpStore)
				logger.Info("pipeline waitpoint store wired (DB-backed; survives restart)")
			}

			// Wire WS broadcaster so pipeline run + step events
			// reach subscribed frontend clients live (PipelineRunNode
			// status updates without polling). Without it, the UI
			// only catches up via journal poll. wsHub is exposed
			// via Server.WSHub() in production; tests skip this
			// branch when wsHub is nil.
			if hub := srv.WSHub(); hub != nil {
				srv.APIRouter().PipelinesHandler.SetWSBroadcaster(hub)
				logger.Info("pipeline WS broadcaster wired (live event push)")
			}

			// Run registry — process-singleton tracker for cancel +
			// concurrency. Lives for the binary's lifetime; no Stop
			// needed because runs are tracked, not goroutines.
			runRegistry := pipeline.NewRunRegistry()
			srv.APIRouter().PipelinesHandler.SetRunRegistry(runRegistry)
			logger.Info("pipeline run registry wired (cancel + concurrency_key gating)")

			// Pipeline schedules — cron triggers for saved pipelines.
			// The store backs the CRUD endpoints; the scheduler runs
			// in-process and fires due schedules every 30s.
			//
			// Single-instance: running multiple replicas would
			// double-fire (no leader election yet); deferring leader
			// election until we have a multi-replica deployment story.
			if deps.DB != nil && srv.APIRouter().PipelinesHandler != nil {
				schedStore := pipeline.NewScheduleStore(deps.DB)
				srv.APIRouter().PipelinesHandler.SetScheduleStore(schedStore)
				// Build a fresh executor for the scheduler. Reusing
				// the handler's lazy newExecutor would couple scheduler
				// lifetime to a specific HTTP request scope; wiring an
				// independent executor is cleaner.
				ph := srv.APIRouter().PipelinesHandler
				schedPipelineStore := pipeline.NewStore(deps.DB)
				schedExec := pipeline.NewExecutor(
					schedPipelineStore,
					pipeline.NewResolver(deps.DB),
					ph.Runner(),
					ph.Emitter(),
				)
				if hub := srv.WSHub(); hub != nil {
					schedExec = schedExec.WithWSBroadcaster(hub)
				}
				// Scheduler-driven runs MUST share the same registry
				// as HTTP-driven runs — otherwise a cron + manual run
				// of the same concurrency_key would slip past the gate.
				schedExec = schedExec.WithRunRegistry(runRegistry).
					WithIdempotencyStore(pipeline.NewIdempotencyStore(deps.DB))
				scheduler := pipeline.NewPipelineScheduler(schedStore, schedPipelineStore, schedExec, logger)
				scheduler.Start(ctx)
				defer scheduler.Stop()
				logger.Info("pipeline scheduler wired (cron triggers; 30s tick)")
			}
		}

		// Start OAuth token refresh worker (refreshes tokens expiring soon)
		if deps.DB != nil {
			oauthStop := make(chan struct{})
			var oauthWg sync.WaitGroup
			api.StartOAuthRefreshWorker(deps.DB, nil, logger, oauthStop, &oauthWg)
			defer func() {
				close(oauthStop)
				oauthWg.Wait()
			}()
		}

		// Start MCP registry sync worker (syncs official registry every 24h)
		if deps.DB != nil {
			registryStop := make(chan struct{})
			var registryWg sync.WaitGroup
			api.StartRegistrySyncWorker(deps.DB, logger, registryStop, &registryWg)
			defer func() {
				close(registryStop)
				registryWg.Wait()
			}()
		}

		// Credential rotation expiry worker (CONNECTIONS.md §7.1):
		// every hour, scrub old_value on rotations whose grace
		// window has passed. Runs once on startup so any rotations
		// that aged out while the server was down get cleaned up
		// before we serve traffic.
		if deps.DB != nil {
			rotationStop := make(chan struct{})
			var rotationWg sync.WaitGroup
			api.StartCredentialRotationExpiryWorker(deps.DB, logger, rotationStop, &rotationWg)
			defer func() {
				close(rotationStop)
				rotationWg.Wait()
			}()
		}

		if err := srv.Start(ctx); err != nil {
			return fmt.Errorf("server error: %w", err)
		}

		logger.Info("crewship stopped")
		return nil
	},
}

func checkAnyRuntime(ctx context.Context) bool {
	if _, err := docker.Detect(ctx); err == nil {
		return true
	}
	if _, err := apple.Detect(ctx); err == nil {
		return true
	}
	return false
}

func initProviders(ctx context.Context, cfg *config.Config, logger *slog.Logger, skipDocker bool) (*server.Deps, error) {
	deps := &server.Deps{}

	switch cfg.Container.Provider {
	case "docker":
		if skipDocker {
			logger.Info("docker provider disabled via --no-docker")
			break
		}
		d, err := docker.New(ctx, docker.Config{
			RuntimeImage:      cfg.Container.RuntimeImage,
			DefaultRuntime:    cfg.Container.DefaultRuntime,
			Network:           cfg.Container.Network,
			OutputBasePath:    cfg.Storage.BasePath,
			ContainerPrefix:   cfg.Container.ContainerPrefix,
			SidecarBinaryPath: cfg.Container.SidecarBinaryPath,
			EntrypointPath:    cfg.Container.EntrypointPath,
		}, logger)
		if err != nil {
			logger.Warn("docker provider unavailable, running without containers", "error", err)
		} else {
			deps.Container = d
		}

	case "apple":
		if skipDocker {
			logger.Info("apple container provider disabled via --no-docker")
			break
		}
		a, err := apple.New(ctx, apple.Config{
			RuntimeImage:    cfg.Container.RuntimeImage,
			Network:         cfg.Container.Network,
			OutputBasePath:  cfg.Storage.BasePath,
			ContainerPrefix: cfg.Container.ContainerPrefix,
		}, logger)
		if err != nil {
			logger.Warn("apple container provider unavailable, running without containers", "error", err)
		} else {
			deps.Container = a
		}

	case "auto":
		if skipDocker {
			logger.Info("container provider disabled via --no-docker")
			break
		}
		// Try Apple Containers first (native, lighter on macOS), fall back to Docker
		a, appleErr := apple.New(ctx, apple.Config{
			RuntimeImage:    cfg.Container.RuntimeImage,
			Network:         cfg.Container.Network,
			OutputBasePath:  cfg.Storage.BasePath,
			ContainerPrefix: cfg.Container.ContainerPrefix,
		}, logger)
		if appleErr == nil {
			logger.Info("auto-detected Apple Containers as container provider")
			deps.Container = a
			break
		}
		logger.Debug("apple containers not available, trying docker", "error", appleErr)
		d, dockerErr := docker.New(ctx, docker.Config{
			RuntimeImage:      cfg.Container.RuntimeImage,
			DefaultRuntime:    cfg.Container.DefaultRuntime,
			Network:           cfg.Container.Network,
			OutputBasePath:    cfg.Storage.BasePath,
			ContainerPrefix:   cfg.Container.ContainerPrefix,
			SidecarBinaryPath: cfg.Container.SidecarBinaryPath,
			EntrypointPath:    cfg.Container.EntrypointPath,
		}, logger)
		if dockerErr == nil {
			logger.Info("auto-detected Docker as container provider")
			deps.Container = d
			break
		}
		logger.Warn("no container provider available (tried Apple Containers and Docker)", "apple_error", appleErr, "docker_error", dockerErr)

	default:
		if cfg.Container.Provider != "" && cfg.Container.Provider != "k8s" {
			logger.Warn("unknown container provider", "provider", cfg.Container.Provider)
		}
	}

	switch cfg.Storage.Provider {
	case "localfs":
		fs, err := localfs.New(cfg.Storage.BasePath)
		if err != nil {
			return nil, fmt.Errorf("init localfs provider: %w", err)
		}
		deps.Storage = fs
	default:
		if cfg.Storage.Provider != "" {
			logger.Warn("unknown storage provider", "provider", cfg.Storage.Provider)
		}
	}

	switch cfg.State.Provider {
	case "bbolt":
		b, err := bbolt.New(cfg.State.BoltPath)
		if err != nil {
			return nil, fmt.Errorf("init bbolt provider: %w", err)
		}
		deps.State = b
	default:
		if cfg.State.Provider != "" {
			logger.Warn("unknown state provider", "provider", cfg.State.Provider)
		}
	}

	return deps, nil
}

func init() {
	startCmd.Flags().String("config", "", "Path to config file (YAML)")
	startCmd.Flags().String("db", "", "Database URL (default: ~/.crewship/crewship.db)")
	startCmd.Flags().Bool("no-docker", false, "Start without Docker (dashboard only)")
}
