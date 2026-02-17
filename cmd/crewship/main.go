package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
	"github.com/crewship-ai/crewship/internal/server"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		cmdStart(os.Args[2:])
	case "version":
		cmdVersion()
	case "doctor":
		cmdDoctor()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Crewship -- AI Agent Orchestration Platform

Usage: crewship <command> [flags]

Commands:
  start      Start the Crewship server
  version    Print version information
  doctor     Check system requirements and health

Flags:
  -h, --help    Show this help message`)
}

func cmdVersion() {
	fmt.Printf("crewship %s\n", version)
	fmt.Printf("  commit:  %s\n", commit)
	fmt.Printf("  built:   %s\n", date)
	fmt.Printf("  go:      %s\n", runtime.Version())
	fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func cmdDoctor() {
	fmt.Println("Crewship Doctor")
	fmt.Println("===============")
	fmt.Println()

	allOK := true

	fmt.Printf("  Go runtime:    %s\n", runtime.Version())

	if checkDocker() {
		fmt.Println("  Docker:        OK")
	} else {
		fmt.Println("  Docker:        NOT FOUND (agent containers will not work)")
		allOK = false
	}

	dataDir, err := database.DefaultDataDir()
	if err != nil {
		fmt.Printf("  Data dir:      ERROR (%v)\n", err)
		allOK = false
	} else {
		fmt.Printf("  Data dir:      %s\n", dataDir.Root)
		dbPath := dataDir.DatabasePath()
		if _, err := os.Stat(dbPath); err == nil {
			fmt.Printf("  Database:      %s (exists)\n", dbPath)
		} else {
			fmt.Printf("  Database:      %s (will be created on start)\n", dbPath)
		}
	}

	fmt.Println()
	if allOK {
		fmt.Println("All checks passed.")
	} else {
		fmt.Println("Some checks failed. Crewship may work with reduced functionality.")
	}
}

func checkDocker() bool {
	_, err := docker.New(docker.Config{}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return err == nil
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (YAML)")
	dbURL := fs.String("db", "", "database URL (default: ~/.crewship/crewship.db)")
	fs.Parse(args)

	bootstrapLogger := logging.New("info", "json", os.Stdout)
	slog.SetDefault(bootstrapLogger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	debugBuffer := logging.NewRingBuffer(500)
	innerLogger := logging.New(cfg.Logging.Level, "json", os.Stdout)
	ringHandler := logging.NewRingHandler(innerLogger.Handler(), debugBuffer)
	logger := slog.New(ringHandler)
	slog.SetDefault(logger)

	// Resolve database URL
	databaseURL := *dbURL
	if databaseURL == "" {
		databaseURL = os.Getenv("DATABASE_URL")
	}
	if databaseURL == "" {
		dataDir, err := database.DefaultDataDir()
		if err != nil {
			logger.Error("failed to create data directory", "error", err)
			os.Exit(1)
		}
		databaseURL = dataDir.DatabaseURL()
		cfg.Storage.BasePath = dataDir.OutputDir()
		cfg.Storage.LogPath = dataDir.LogsDir()
	}

	// Open and migrate SQLite
	db, err := database.Open(databaseURL)
	if err != nil {
		logger.Error("failed to open database", "error", err, "url", databaseURL)
		os.Exit(1)
	}
	defer db.Close()

	if err := database.Migrate(db.DB, logger); err != nil {
		logger.Error("failed to run migrations", "error", err)
		os.Exit(1)
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

	deps, err := initProviders(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize providers", "error", err)
		os.Exit(1)
	}
	defer deps.Close()
	deps.DebugLogs = debugBuffer
	deps.DB = db.DB

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
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

	logger.Info("crewship stopped")
}

func initProviders(cfg *config.Config, logger *slog.Logger) (*server.Deps, error) {
	deps := &server.Deps{}

	switch cfg.Container.Provider {
	case "docker":
		d, err := docker.New(docker.Config{
			RuntimeImage:   cfg.Container.RuntimeImage,
			DefaultRuntime: cfg.Container.DefaultRuntime,
			Network:        cfg.Container.Network,
			OutputBasePath: cfg.Storage.BasePath,
		}, logger)
		if err != nil {
			logger.Warn("docker provider unavailable, running without containers", "error", err)
		} else {
			deps.Container = d
		}
	default:
		if cfg.Container.Provider != "" {
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
