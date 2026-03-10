package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
	"github.com/crewship-ai/crewship/internal/server"
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
			return fmt.Errorf("no container runtime found.\n\n" +
				"Crewship requires a container runtime to run AI agents.\n" +
				"Supported: Docker, Podman, Colima, OrbStack, Rancher Desktop, Apple Containers\n\n" +
				"Install Docker Desktop:    https://docs.docker.com/get-docker/\n" +
				"Install Podman:            https://podman.io/docs/installation\n" +
				"Install Apple Containers:  brew install container (macOS 26+)\n\n" +
				"To start without containers (dashboard only, no agents):\n" +
				"  crewship start --no-docker\n\n" +
				"Run 'crewship doctor' for full diagnostics.")
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
			RuntimeImage:    cfg.Container.RuntimeImage,
			DefaultRuntime:  cfg.Container.DefaultRuntime,
			Network:         cfg.Container.Network,
			OutputBasePath:  cfg.Storage.BasePath,
			ContainerPrefix: cfg.Container.ContainerPrefix,
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
			RuntimeImage:    cfg.Container.RuntimeImage,
			DefaultRuntime:  cfg.Container.DefaultRuntime,
			Network:         cfg.Container.Network,
			OutputBasePath:  cfg.Storage.BasePath,
			ContainerPrefix: cfg.Container.ContainerPrefix,
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
