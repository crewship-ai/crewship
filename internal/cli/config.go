package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CLIConfig holds persisted CLI settings including server URL, workspace,
// auth token, and output format.
type CLIConfig struct {
	Server       string `yaml:"server,omitempty"`
	Workspace    string `yaml:"workspace,omitempty"`
	Token        string `yaml:"token,omitempty"`
	Format       string `yaml:"format,omitempty"`
	DefaultAgent string `yaml:"default_agent,omitempty"`
	// Markdown enables ANSI markdown rendering for streamed agent text.
	// "auto" (default) renders only when stdout is a TTY; "on" forces;
	// "off" disables. Overridden by --no-markdown / --markdown flags.
	Markdown string `yaml:"markdown,omitempty"`
	// Notifications enables desktop notifications for long-running run
	// completions, escalations, and pending approvals. Off by default
	// so the CLI never starts pinging without explicit opt-in.
	Notifications bool `yaml:"notifications,omitempty"`
	// PlanByDefault toggles the `--plan` flag default to true for run/ask.
	// Useful for teams that want plan-first to be the norm.
	PlanByDefault bool `yaml:"plan_by_default,omitempty"`
}

// DefaultConfigDir returns the path to ~/.crewship.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".crewship"), nil
}

// DefaultConfigPath returns the path to the CLI config file.
//
// $CREWSHIP_CONFIG, when set, points directly at the file the CLI
// should load/save. This is the multi-instance escape hatch from issue
// #544 — without it, parallel work on /opt/crewship_1 and
// /opt/crewship_2 silently clobbers each other's ~/.crewship/cli-
// config.yaml (same user account on the dev VM) and the other
// instance starts returning `session_invalid` for tokens that were
// minted against a different bootstrap. Setting CREWSHIP_CONFIG=
// /opt/crewship_2/.cli-config.yaml in that instance's shell pins
// the file to a per-instance path so the two sessions stay isolated.
// Falls back to ~/.crewship/cli-config.yaml otherwise so single-
// instance setups stay byte-identical.
func DefaultConfigPath() (string, error) {
	if v := os.Getenv("CREWSHIP_CONFIG"); v != "" {
		return v, nil
	}
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cli-config.yaml"), nil
}

// LoadConfig reads the CLI configuration from the default config file,
// returning an empty config if the file does not exist.
func LoadConfig() (*CLIConfig, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return &CLIConfig{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CLIConfig{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg CLIConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// SaveConfig writes the CLI configuration to the default config file.
func SaveConfig(cfg *CLIConfig) error {
	path, err := DefaultConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// loadConfigFrom loads config from a specific path.
func loadConfigFrom(path string) (*CLIConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CLIConfig{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg CLIConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// ResolveServer returns the effective server URL from flag > env > config > default.
func ResolveServer(flagVal string, cfg *CLIConfig) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("CREWSHIP_SERVER"); v != "" {
		return v
	}
	if cfg != nil && cfg.Server != "" {
		return cfg.Server
	}
	return "http://localhost:8080"
}

// ResolveWorkspace returns the effective workspace from flag > env > config.
func ResolveWorkspace(flagVal string, cfg *CLIConfig) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("CREWSHIP_WORKSPACE"); v != "" {
		return v
	}
	if cfg != nil && cfg.Workspace != "" {
		return cfg.Workspace
	}
	return ""
}

// ResolveDefaultAgent returns the agent slug to use by default for `crewship ask`
// when no --agent flag is given. Order of precedence:
//
//	flag > CREWSHIP_DEFAULT_AGENT env var > config.default_agent
//
// Returns empty if none are set; callers decide whether to error or
// open the interactive picker.
//
// The env var slot exists so users with multiple shells or shell-scoped
// agent contexts (e.g. a frontend project shell vs a backend project
// shell) can override the persisted default without `crewship config set`
// every time.
func ResolveDefaultAgent(flagVal string, cfg *CLIConfig) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("CREWSHIP_DEFAULT_AGENT"); v != "" {
		return v
	}
	if cfg != nil && cfg.DefaultAgent != "" {
		return cfg.DefaultAgent
	}
	return ""
}

// ResolveFormat returns the effective output format from flag > config > default.
func ResolveFormat(flagVal string, cfg *CLIConfig) string {
	if flagVal != "" {
		return flagVal
	}
	if cfg != nil && cfg.Format != "" {
		return cfg.Format
	}
	return "table"
}
