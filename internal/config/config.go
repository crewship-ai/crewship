package config

import (
	"crypto/rand"
	"encoding/hex"
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
	LLMProxy  LLMProxyConfig  `yaml:"llm_proxy"`
	Keeper    KeeperConfig    `yaml:"keeper"`
	License   LicenseConfig   `yaml:"license"`
}

type LicenseConfig struct {
	FilePath string `yaml:"file_path"`
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
	Provider        string  `yaml:"provider"` // "docker" | "k8s"
	RuntimeImage    string  `yaml:"runtime_image"`
	DefaultRuntime  string  `yaml:"default_runtime"` // "runc" | "runsc" (gVisor) | "kata-runtime" | "sysbox-runc"
	Network         string  `yaml:"network"`
	ContainerPrefix string  `yaml:"container_prefix"` // Container name prefix for multi-instance isolation
	DefaultMemoryMB int     `yaml:"default_memory_mb"`
	DefaultCPUs     float64 `yaml:"default_cpus"`
	SidecarEnabled  bool    `yaml:"sidecar_enabled"` // enable sidecar proxy for credential injection
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
	JWTSecret       string        `yaml:"jwt_secret"`
	WSTokenExpiry   time.Duration `yaml:"ws_token_expiry"`
	NextjsURL       string        `yaml:"nextjs_url"`
	InternalToken   string        `yaml:"internal_token"`
	AllowSignup     bool          `yaml:"allow_signup"`
	GoogleClientID  string        `yaml:"google_client_id"`
	GoogleSecret    string        `yaml:"google_client_secret"`
}

type LLMProxyConfig struct {
	Enabled          bool          `yaml:"enabled"`
	TokenSyncInterval time.Duration `yaml:"token_sync_interval"`
	HealthCheckInterval time.Duration `yaml:"health_check_interval"`
}

type KeeperConfig struct {
	Enabled   bool   `yaml:"enabled"`
	OllamaURL string `yaml:"ollama_url"`
	Model     string `yaml:"model"`
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
			NextjsURL:     "http://localhost:8080",
			AllowSignup:   false,
		},
		LLMProxy: LLMProxyConfig{
			Enabled:             true,
			TokenSyncInterval:   30 * time.Second,
			HealthCheckInterval: 60 * time.Second,
		},
		Keeper: KeeperConfig{
			Enabled:   false,
			OllamaURL: "http://localhost:11434",
			Model:     "phi3:mini",
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

	// Auto-derive NextjsURL from server port if not explicitly overridden.
	// In single binary mode, the internal resolver calls itself on the same port.
	if os.Getenv("CREWSHIP_NEXTJS_URL") == "" {
		cfg.Auth.NextjsURL = fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	}

	// Auto-generate a cryptographically random internal token if none was
	// configured. This eliminates the hardcoded "crewshipd" default that
	// anyone knowing the source code could use to access decrypted credentials.
	if cfg.Auth.InternalToken == "" {
		token, err := generateRandomToken(32)
		if err != nil {
			return nil, fmt.Errorf("generate internal token: %w", err)
		}
		cfg.Auth.InternalToken = token
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

var (
	validContainerProviders = map[string]bool{"docker": true, "apple": true, "auto": true, "k8s": true}
	validStorageProviders   = map[string]bool{"localfs": true, "s3": true}
	validStateProviders     = map[string]bool{"bbolt": true, "postgres": true}
	validContainerRuntimes  = map[string]bool{
		"runc": true, "runsc": true, "kata-runtime": true, "sysbox-runc": true,
	}
)

func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", c.Server.Port)
	}
	if c.IPC.SocketPath == "" {
		return fmt.Errorf("ipc.socket_path is required")
	}
	if !validContainerProviders[c.Container.Provider] {
		return fmt.Errorf("container.provider must be 'docker', 'apple', 'auto', or 'k8s', got %q", c.Container.Provider)
	}
	if !validStorageProviders[c.Storage.Provider] {
		return fmt.Errorf("storage.provider must be 'localfs' or 's3', got %q", c.Storage.Provider)
	}
	if !validStateProviders[c.State.Provider] {
		return fmt.Errorf("state.provider must be 'bbolt' or 'postgres', got %q", c.State.Provider)
	}
	if v := c.Container.DefaultRuntime; v != "" && !validContainerRuntimes[v] {
		return fmt.Errorf("container.default_runtime must be one of runc, runsc, kata-runtime, sysbox-runc; got %q", v)
	}
	if c.Auth.NextjsURL == "" {
		return fmt.Errorf("auth.nextjs_url is required (set CREWSHIP_NEXTJS_URL)")
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
	if v := os.Getenv("CREWSHIP_CONTAINER_NETWORK"); v != "" {
		cfg.Container.Network = v
	}
	if v := os.Getenv("CREWSHIP_CONTAINER_PREFIX"); v != "" {
		cfg.Container.ContainerPrefix = v
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
	if v := os.Getenv("CREWSHIP_SIDECAR_ENABLED"); v == "true" || v == "1" {
		cfg.Container.SidecarEnabled = true
	}
	if v := os.Getenv("CREWSHIP_NEXTJS_URL"); v != "" {
		cfg.Auth.NextjsURL = v
	}
	if v := os.Getenv("CREWSHIP_INTERNAL_TOKEN"); v != "" {
		cfg.Auth.InternalToken = v
	}
	if v := os.Getenv("CREWSHIP_ALLOW_SIGNUP"); v != "" {
		cfg.Auth.AllowSignup = v == "true" || v == "1"
	}
	if v := os.Getenv("GOOGLE_CLIENT_ID"); v != "" {
		cfg.Auth.GoogleClientID = v
	}
	if v := os.Getenv("GOOGLE_CLIENT_SECRET"); v != "" {
		cfg.Auth.GoogleSecret = v
	}
	if v := os.Getenv("KEEPER_ENABLED"); v != "" {
		cfg.Keeper.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("KEEPER_OLLAMA_URL"); v != "" {
		cfg.Keeper.OllamaURL = v
		// Auto-enable when URL is set, unless explicitly disabled
		if os.Getenv("KEEPER_ENABLED") == "" {
			cfg.Keeper.Enabled = true
		}
	}
	if v := os.Getenv("KEEPER_MODEL"); v != "" {
		cfg.Keeper.Model = v
	}
	if v := os.Getenv("CREWSHIP_LICENSE_FILE"); v != "" {
		cfg.License.FilePath = v
	}
}

func generateRandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
