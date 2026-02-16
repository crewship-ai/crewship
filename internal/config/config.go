package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	IPC       IPCConfig       `yaml:"ipc"`
	Container ContainerConfig `yaml:"container"`
	Storage   StorageConfig   `yaml:"storage"`
	State     StateConfig     `yaml:"state"`
	Logging   LoggingConfig   `yaml:"logging"`
	Auth      AuthConfig      `yaml:"auth"`
}

type ServerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type IPCConfig struct {
	SocketPath string `yaml:"socket_path"`
}

type ContainerConfig struct {
	Provider       string `yaml:"provider"` // "docker" | "k8s"
	RuntimeImage   string `yaml:"runtime_image"`
	DefaultRuntime string `yaml:"default_runtime"` // "runc" | "runsc"
	Network        string `yaml:"network"`
	DefaultMemoryMB int   `yaml:"default_memory_mb"`
	DefaultCPUs    float64 `yaml:"default_cpus"`
}

type StorageConfig struct {
	Provider string `yaml:"provider"` // "localfs" | "s3"
	BasePath string `yaml:"base_path"`
	LogPath  string `yaml:"log_path"`
}

type StateConfig struct {
	Provider string `yaml:"provider"` // "bbolt" | "postgres"
	BoltPath string `yaml:"bolt_path"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"` // "debug" | "info" | "warn" | "error"
	Format string `yaml:"format"` // "json" | "text"
}

type AuthConfig struct {
	JWTSecret     string        `yaml:"jwt_secret"`
	WSTokenExpiry time.Duration `yaml:"ws_token_expiry"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ShutdownTimeout: 10 * time.Second,
		},
		IPC: IPCConfig{
			SocketPath: "/tmp/crewship.sock",
		},
		Container: ContainerConfig{
			Provider:       "docker",
			RuntimeImage:   "ghcr.io/crewship-ai/agent-runtime:latest",
			DefaultRuntime: "runc",
			Network:        "crewship-agents",
			DefaultMemoryMB: 512,
			DefaultCPUs:    1.0,
		},
		Storage: StorageConfig{
			Provider: "localfs",
			BasePath: "/var/lib/crewship",
			LogPath:  "/var/log/crewship",
		},
		State: StateConfig{
			Provider: "bbolt",
			BoltPath: "/var/lib/crewship/state.db",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Auth: AuthConfig{
			WSTokenExpiry: 5 * time.Minute,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		if err := loadFromFile(cfg, path); err != nil {
			return nil, fmt.Errorf("load config file: %w", err)
		}
	}

	applyEnvOverrides(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

var (
	validContainerProviders = map[string]bool{"docker": true, "k8s": true}
	validStorageProviders   = map[string]bool{"localfs": true, "s3": true}
	validStateProviders     = map[string]bool{"bbolt": true, "postgres": true}
)

func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", c.Server.Port)
	}
	if c.IPC.SocketPath == "" {
		return fmt.Errorf("ipc.socket_path is required")
	}
	if !validContainerProviders[c.Container.Provider] {
		return fmt.Errorf("container.provider must be 'docker' or 'k8s', got %q", c.Container.Provider)
	}
	if !validStorageProviders[c.Storage.Provider] {
		return fmt.Errorf("storage.provider must be 'localfs' or 's3', got %q", c.Storage.Provider)
	}
	if !validStateProviders[c.State.Provider] {
		return fmt.Errorf("state.provider must be 'bbolt' or 'postgres', got %q", c.State.Provider)
	}
	return nil
}

func loadFromFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("CREWSHIP_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("CREWSHIP_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		} else {
			slog.Warn("ignoring invalid CREWSHIP_PORT", "value", v, "error", err)
		}
	}
	if v := os.Getenv("CREWSHIP_SOCKET_PATH"); v != "" {
		cfg.IPC.SocketPath = v
	}
	if v := os.Getenv("CREWSHIP_CONTAINER_PROVIDER"); v != "" {
		cfg.Container.Provider = v
	}
	if v := os.Getenv("CREWSHIP_STORAGE_PROVIDER"); v != "" {
		cfg.Storage.Provider = v
	}
	if v := os.Getenv("CREWSHIP_STORAGE_BASE_PATH"); v != "" {
		cfg.Storage.BasePath = v
	}
	if v := os.Getenv("CREWSHIP_LOG_PATH"); v != "" {
		cfg.Storage.LogPath = v
	}
	if v := os.Getenv("CREWSHIP_STATE_PROVIDER"); v != "" {
		cfg.State.Provider = v
	}
	if v := os.Getenv("CREWSHIP_BOLT_PATH"); v != "" {
		cfg.State.BoltPath = v
	}
	if v := os.Getenv("CREWSHIP_LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := os.Getenv("CREWSHIP_LOG_FORMAT"); v != "" {
		cfg.Logging.Format = v
	}
	if v := os.Getenv("NEXTAUTH_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	if v := os.Getenv("CREWSHIP_RUNTIME_IMAGE"); v != "" {
		cfg.Container.RuntimeImage = v
	}
}
