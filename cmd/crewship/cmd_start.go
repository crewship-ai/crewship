//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"

	api "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
	"github.com/crewship-ai/crewship/internal/quartermaster"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/secrets"
	"github.com/crewship-ai/crewship/internal/server"
	bundledSkills "github.com/crewship-ai/crewship/internal/skills/bundled"
	"github.com/crewship-ai/crewship/internal/update"
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

		// Resolve the data dir BEFORE loading config: config.Load reads
		// NEXTAUTH_SECRET / ENCRYPTION_KEY via os.Getenv, and the
		// downstream Server.New panics if NEXTAUTH_SECRET is still
		// empty when it gets called. secrets.LoadOrGenerate seeds those
		// env vars from <dataDir>/secrets.env on first run (generating
		// + persisting them when absent), so the rest of startup keeps
		// using its existing os.Getenv reads with no changes.
		//
		// Bootstrap runs under a 5 s ctx — the only thing it does is
		// small file I/O in the data dir, so a multi-second hang is
		// almost certainly a stuck filesystem and the operator wants a
		// clean error rather than a wedged startup.
		dataDir, err := database.DefaultDataDir()
		if err != nil {
			return fmt.Errorf("failed to create data directory: %w", err)
		}
		bootCtx, bootCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := secrets.LoadOrGenerate(bootCtx, dataDir.Root, bootstrapLogger); err != nil {
			bootCancel()
			return fmt.Errorf("bootstrap secrets: %w", err)
		}
		bootCancel()

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
			databaseURL = dataDir.DatabaseURL()
			cfg.Storage.BasePath = dataDir.OutputDir()
			cfg.Storage.LogPath = dataDir.LogsDir()
			// Workspace-tier memory lives under DataDir.Root/memory.
			// Per-workspace subdirs are lazy-created by the
			// WorkspaceMemoryRegistry on first agent run that asks
			// for the workspace tier in its prompt.
			cfg.Storage.MemoryRoot = filepath.Join(dataDir.Root, "memory")
			// bbolt state lives next to the SQLite DB in the data dir
			// too. The package default is /var/lib/crewship/state.db,
			// which a non-root user on a fresh box can't create — and
			// `crewship start` for end users is decidedly non-root.
			//
			// Two narrow predicates protect operator intent:
			//   * `cfgBoltPathFromEnv()` — env var pin via
			//     CREWSHIP_BOLT_PATH (applyEnvOverrides ran first and
			//     already set BoltPath from it).
			//   * a YAML config can also set state.bolt_path; if that
			//     value is anything other than the package default
			//     ("" or "/var/lib/crewship/state.db"), the operator
			//     made an explicit choice and we leave it alone.
			//
			// The literal `/var/lib/crewship/state.db` is the one in
			// internal/config/config.go:190. If that default ever
			// moves, this check needs to follow it; a regression test
			// in internal/config covers that the rewritten path
			// actually creates and the default does not.
			defaulted := cfg.State.BoltPath == "" || cfg.State.BoltPath == "/var/lib/crewship/state.db"
			if !cfgBoltPathFromEnv() && defaulted {
				cfg.State.BoltPath = filepath.Join(dataDir.Root, "state.db")
			}
		}

		db, err := database.Open(databaseURL)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer db.Close()

		if err := database.SnapshotBeforeMigrate(context.Background(), db, logger); err != nil {
			return fmt.Errorf("failed to snapshot database before migrations: %w", err)
		}
		if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
			return fmt.Errorf("failed to run migrations: %w", err)
		}
		if err := database.SeedBundledSkills(context.Background(), db.DB, logger); err != nil {
			logger.Warn("failed to seed bundled skills", "error", err)
		}
		if err := bundledSkills.Install(context.Background(), db.DB, logger); err != nil {
			logger.Warn("failed to install bundled anthropic skills", "error", err)
		}

		// First-run welcome: detected as "no users in the DB after
		// migrations completed." Prints a short banner pointing at the
		// browser-side onboarding wizard. Best-effort — a query error is
		// surfaced as a warning, not a startup failure, because a stale or
		// half-migrated DB is the symptom we WANT visible via the regular
		// migration log, not silently retried here.
		printFirstRunWelcome(db.DB, logger)

		// Telemetry: ENABLED by default for v0.1 beta. crashreport.Init
		// writes "1" to app_settings on first start (no prompt). The
		// operator can disable any time with `crewship telemetry off`.
		// The first-run TTY prompt previously called here is deprecated
		// for the beta default-on stance; see project memory
		// telemetry-beta-default-on. Revert to prompted opt-in when
		// flipping the Init default back for v1.0.
		if err := crashreport.Init(context.Background(), db.DB, version, logger); err != nil {
			logger.Warn("crashreport init failed", "error", err)
		}
		defer crashreport.Flush(2 * time.Second)

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

		// Fire-and-forget update check. Result is logged on the next line
		// after the network call returns; never blocks the boot path. The
		// internal/update package caches for 24h so this is at most one
		// GitHub API hit per day per install.
		go func() {
			r, err := update.Check(context.Background(), version)
			if err != nil {
				logger.Debug("update check failed", "error", err)
				return
			}
			if r != nil && r.Newer {
				logger.Info("crewship update available", "current", r.Current, "latest", r.Latest, "url", r.URL)
				fmt.Fprint(os.Stderr, update.FormatBanner(r))
			}
		}()

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
			apiRouter.SetVersion(version)
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

		// Wire the pipeline AgentRunner.
		//
		// Two implementations exist; selection logic:
		//
		//   1. CREWSHIP_PIPELINE_RUNNER=llm_direct (explicit override)  → LLMRunner
		//   2. deps.Container == nil (no Docker provider available)     → LLMRunner
		//   3. otherwise (default; production)                          → OrchestratorRunner
		//
		// OrchestratorRunner is the production path: pipelines route
		// every step through the same orchestrator the scheduler uses,
		// the agent runs in its real container with its real CLI
		// adapter (Claude Code / Codex / Gemini / etc.), no raw LLM
		// API key required. This is the "reuse the firm's own
		// employees" model.
		//
		// LLMRunner is the eval / CI / no-Docker fallback. It calls
		// the workspace's Anthropic credential directly via
		// internal/llm, skipping the container + adapter layer. Used
		// for cross-tier eval scenarios (where skills + MCP loops are
		// out of scope by design) and for `--no-docker` smoke runs.
		// See internal/pipeline/runner_llm.go for the trade-off
		// analysis vs. the orchestrator path.
		//
		// Wired here (not in server.go) because OrchestratorRunner
		// needs chatbridge.ChatResolver, which is constructed in this
		// file. The router exposes PipelinesHandler so either runner
		// can be plugged in post-router-construction.
		if deps.DB != nil && srv.APIRouter() != nil && srv.APIRouter().PipelinesHandler != nil {
			runnerMode := os.Getenv("CREWSHIP_PIPELINE_RUNNER")
			useLLMDirect := runnerMode == "llm_direct" || deps.Container == nil
			switch {
			case useLLMDirect:
				// LLMRunner takes a journal Emitter. The router's
				// Emitter() accessor returns the wired writer; in
				// boot order it's already set by the time we get
				// here (server.go constructs the writer before
				// returning).
				// LLMRunner takes journal.Emitter for the middleware
				// stack (paymaster cost ledger). PipelinesHandler.Emitter()
				// returns the narrower pipeline.Emitter; we pass the full
				// Server.JournalWriter() so cost ledger entries land in
				// the same buffer as the rest of the server. Falls back
				// to a no-op writer when journalWriter is nil (test path
				// — should never happen in `crewship start`).
				pipeRunner := pipeline.NewLLMRunner(deps.DB, srv.JournalWriter(), logger)
				srv.APIRouter().PipelinesHandler.SetRunner(pipeRunner)
				reason := "no Docker provider"
				if runnerMode == "llm_direct" {
					reason = "CREWSHIP_PIPELINE_RUNNER=llm_direct"
				}
				logger.Info("pipeline runner wired (LLM-direct mode — bypasses container/adapter layer)",
					"reason", reason)
			default:
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
			}

			// Wire HMAC secret for save_token signing. Reuses the
			// process internal token — it's already required to be
			// set + stable for the lifetime of the binary, so it
			// satisfies the "process-stable secret" contract without
			// adding new config surface. Token validity is bound to
			// 5 minutes regardless, so internal-token rotation
			// invalidates outstanding tokens by design.
			if cfg.Auth.InternalToken == "" {
				// Fail-fast: silent degrade to body-trust would defeat
				// the threat-model closure that PIPELINES.md §17 calls
				// out as a hard requirement. Better to refuse to start
				// than to ship a server that quietly keeps the
				// loophole open.
				return fmt.Errorf("crewship start: cfg.Auth.InternalToken is required for save_token HMAC signing — set CREWSHIP_INTERNAL_TOKEN or auth.internal_token in config")
			}
			srv.APIRouter().PipelinesHandler.SetSaveTokenSecret([]byte(cfg.Auth.InternalToken))
			logger.Info("pipeline save_token signing enabled (HMAC-SHA256 over internal token)")

			// Wire production WaitpointStore so StepWait approvals
			// persist across restarts and the inbox UI can fire
			// /pipelines/waitpoints/{token}/approve. Without this,
			// approval steps timeout after 60s in-memory only.
			if deps.DB != nil {
				wpStore := pipeline.NewSQLWaitpointStore(deps.DB)
				defer wpStore.Close()
				srv.APIRouter().PipelinesHandler.SetWaitpointStore(wpStore)
				// Recovery scan: pending waitpoints from the previous
				// process lifetime have no goroutine parked on them
				// (run state is in-memory only). Sweep elapsed-timeout
				// rows eagerly and log how many remain so abnormal
				// accumulation is observable at boot.
				if timedOut, pending, err := wpStore.RecoverPending(ctx); err != nil {
					logger.Warn("pipeline waitpoint recovery scan failed", "error", err)
				} else {
					logger.Info("pipeline waitpoint store wired (DB-backed; survives restart)",
						"recovered_timed_out", timedOut, "stranded_pending", pending)
				}

				// Wire RunStore (migration v83) so the executor
				// persists per-run state alongside journal_entries
				// and the boot recovery scan can mark previously
				// in-flight runs interrupted. The PipelinesHandler
				// holds the store reference; newExecutor() picks it
				// up via WithRunStore on every handler path.
				runStore := pipeline.NewRunStore(deps.DB)
				srv.APIRouter().PipelinesHandler.SetRunStore(runStore)
				if interrupted, err := runStore.RecoverInterruptedAtBoot(ctx); err != nil {
					logger.Warn("pipeline_runs recovery scan failed", "error", err)
				} else {
					logger.Info("pipeline_runs store wired (persistent per-run state; v83)",
						"interrupted_recovered", interrupted)
				}
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

			// Webhook store — event-driven triggers. Dispatch is
			// public (no auth middleware); auth comes from the token
			// in the URL plus optional HMAC.
			if deps.DB != nil {
				webhookStore := pipeline.NewWebhookStore(deps.DB)
				srv.APIRouter().PipelinesHandler.SetWebhookStore(webhookStore)
				logger.Info("pipeline webhooks wired (event-driven triggers; HMAC + per-token rate limit)")
			}

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
					WithIdempotencyStore(pipeline.NewIdempotencyStore(deps.DB)).
					WithRunStore(pipeline.NewRunStore(deps.DB))
				// Without WithRunStore here, scheduled runs would
				// never land in the pipeline_runs projection — the
				// /run-records endpoint and boot-time interrupted
				// recovery would silently skip cron-triggered runs.
				scheduler := pipeline.NewPipelineScheduler(schedStore, schedPipelineStore, schedExec, logger)
				scheduler.Start(ctx)
				defer scheduler.Stop()
				logger.Info("pipeline scheduler wired (cron triggers; 30s tick)")
			}
		}

		// Online eval sampler — watches completed pipeline_runs and
		// queues a configurable percentage for rubric grading via
		// eval_runs(kind='online'). The DSL resolver wraps pipeline.Store
		// so the sampler can read each routine's eval.online.sample_rate
		// without importing the executor. Sampler is in-memory + singleton
		// (sync.Once inside Start); the partial UNIQUE INDEX from
		// migration v97 makes accidental double-wiring harmless.
		if deps.DB != nil {
			samplerPipeStore := pipeline.NewStore(deps.DB)
			samplerResolver := &samplerDSLResolver{store: samplerPipeStore}
			sampler, samplerErr := quartermaster.NewOnlineSampler(quartermaster.SamplerConfig{
				DB:          deps.DB,
				Emitter:     srv.JournalWriter(),
				Logger:      logger,
				Interval:    time.Minute,
				DSLResolver: samplerResolver,
			})
			if samplerErr != nil {
				logger.Warn("online eval sampler init failed; continuous grading disabled",
					"err", samplerErr)
			} else {
				go sampler.Start(ctx)
				logger.Info("online eval sampler wired (1m tick; per-routine sample_rate)")
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

		// PR-E F6 — PeerCardSync routine. Walks every workspace once
		// per day at ~04:00 UTC (offset chosen to avoid colliding
		// with EphemeralExpiry, which lands at ~03:00 in PR-D), runs
		// the per-(agent, user) eligibility tree, and writes / purges
		// peer cards on disk under cfg.Storage.BasePath. Without this
		// worker, peer cards would never be generated and F6 ("agent
		// reacts differently to different operators") is paper-only.
		// Extractor defaults to NoopExtractor — production wiring of
		// the aux-LLM-driven extractor lands in PR-F.
		if deps.DB != nil {
			peerStop := make(chan struct{})
			var peerWg sync.WaitGroup
			consolidate.StartPeerCardSyncWorker(
				deps.DB, logger,
				consolidate.PeerCardWorkerConfig{BasePath: cfg.Storage.BasePath},
				peerStop, &peerWg,
			)
			defer func() {
				close(peerStop)
				peerWg.Wait()
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

// cfgBoltPathFromEnv reports whether CREWSHIP_BOLT_PATH supplied the
// current State.BoltPath value. The default-data-dir branch in start
// rewrites BoltPath to live under the data dir for non-root installs,
// but only when the operator hasn't explicitly pinned it. We don't
// have a separate "value came from env" flag, so we ask the env
// directly — applyEnvOverrides already trimmed-and-applied any value
// it found, so seeing it set here means the override path won.
func cfgBoltPathFromEnv() bool {
	return strings.TrimSpace(os.Getenv("CREWSHIP_BOLT_PATH")) != ""
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

// printFirstRunWelcome detects a fresh install ("no rows in `users`")
// and writes a short stdout banner pointing the operator at the
// browser-side onboarding wizard. Query errors are logged as warnings
// and swallowed — a stale or half-migrated database is already
// surfaced via the migration log, and a missing `users` table would
// indicate the upstream migration runner skipped a step we want
// visible, not silently re-handled here.
//
// Suppressed when stdout is not a TTY (CI, redirected starts) so the
// banner doesn't pollute structured log files or systemd journal output.
func printFirstRunWelcome(db *sql.DB, logger *slog.Logger) {
	fi, err := os.Stdout.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return
	}
	var n int
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		logger.Warn("first-run welcome: count(users) failed", "error", err)
		return
	}
	if n > 0 {
		return
	}
	port := os.Getenv("CREWSHIP_PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Println()
	fmt.Printf("  %sWelcome to Crewship!%s  %s%s%s\n",
		cli.Bold, cli.Reset, cli.Dim, version, cli.Reset)
	fmt.Println()
	fmt.Printf("  This is a fresh install. To finish setup:\n")
	fmt.Println()
	fmt.Printf("    1. open  %shttp://localhost:%s%s\n", cli.Green, port, cli.Reset)
	fmt.Printf("    2. work through the 6-step wizard\n")
	fmt.Printf("       workspace → crew → agent → credentials → done\n")
	fmt.Println()
	fmt.Printf("  Prefer the CLI? Run %screwship init --email you@example.com --name \"You\"%s\n", cli.Dim, cli.Reset)
	fmt.Printf("  then follow https://docs.crewship.ai/guides/onboarding\n")
	fmt.Println()
}

func init() {
	startCmd.Flags().String("config", "", "Path to config file (YAML)")
	startCmd.Flags().String("db", "", "Database URL (default: ~/.crewship/crewship.db)")
	startCmd.Flags().Bool("no-docker", false, "Start without Docker (dashboard only)")
}
