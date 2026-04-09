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
	Server    string `yaml:"server,omitempty"`
	Workspace string `yaml:"workspace,omitempty"`
	Token     string `yaml:"token,omitempty"`
	Format    string `yaml:"format,omitempty"`
}

// DefaultConfigDir returns the path to ~/.crewship.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".crewship"), nil
}

// DefaultConfigPath returns the path to ~/.crewship/cli-config.yaml.
func DefaultConfigPath() (string, error) {
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

// marshalConfig serializes config to YAML bytes.
func marshalConfig(cfg *CLIConfig) ([]byte, error) {
	return yaml.Marshal(cfg)
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
