package cli

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveServer(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		env    string
		config *CLIConfig
		want   string
	}{
		{"flag wins", "http://flag:1234", "", &CLIConfig{Server: "http://config:5678"}, "http://flag:1234"},
		{"env wins over config", "", "http://env:1234", &CLIConfig{Server: "http://config:5678"}, "http://env:1234"},
		{"config fallback", "", "", &CLIConfig{Server: "http://config:5678"}, "http://config:5678"},
		{"default when all empty", "", "", &CLIConfig{}, "http://localhost:8080"},
		{"nil config", "", "", nil, "http://localhost:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("CREWSHIP_SERVER")
			if tt.env != "" {
				t.Setenv("CREWSHIP_SERVER", tt.env)
			}
			got := ResolveServer(tt.flag, tt.config)
			if got != tt.want {
				t.Errorf("ResolveServer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		env    string
		config *CLIConfig
		want   string
	}{
		{"flag wins", "ws-flag", "", &CLIConfig{Workspace: "ws-config"}, "ws-flag"},
		{"env wins over config", "", "ws-env", &CLIConfig{Workspace: "ws-config"}, "ws-env"},
		{"config fallback", "", "", &CLIConfig{Workspace: "ws-config"}, "ws-config"},
		{"empty when all empty", "", "", &CLIConfig{}, ""},
		{"nil config", "", "", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("CREWSHIP_WORKSPACE")
			if tt.env != "" {
				t.Setenv("CREWSHIP_WORKSPACE", tt.env)
			}
			got := ResolveWorkspace(tt.flag, tt.config)
			if got != tt.want {
				t.Errorf("ResolveWorkspace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDefaultAgent(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		env    string
		config *CLIConfig
		want   string
	}{
		{"flag wins", "viktor", "", &CLIConfig{DefaultAgent: "eva"}, "viktor"},
		{"env wins over config", "", "piotr", &CLIConfig{DefaultAgent: "eva"}, "piotr"},
		{"config fallback", "", "", &CLIConfig{DefaultAgent: "eva"}, "eva"},
		{"empty when all empty", "", "", &CLIConfig{}, ""},
		{"nil config", "", "", nil, ""},
		{"flag beats env", "from-flag", "from-env", &CLIConfig{DefaultAgent: "from-cfg"}, "from-flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("CREWSHIP_DEFAULT_AGENT")
			if tt.env != "" {
				t.Setenv("CREWSHIP_DEFAULT_AGENT", tt.env)
			}
			got := ResolveDefaultAgent(tt.flag, tt.config)
			if got != tt.want {
				t.Errorf("ResolveDefaultAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		config *CLIConfig
		want   string
	}{
		{"flag wins", "json", &CLIConfig{Format: "yaml"}, "json"},
		{"config fallback", "", &CLIConfig{Format: "yaml"}, "yaml"},
		{"default table", "", &CLIConfig{}, "table"},
		{"nil config", "", nil, "table"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveFormat(tt.flag, tt.config)
			if got != tt.want {
				t.Errorf("ResolveFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cli-config.yaml")

	original := &CLIConfig{
		Server:    "http://test:9090",
		Workspace: "test-ws",
		Token:     "test-token-123",
		Format:    "json",
	}

	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := loadConfigFrom(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.Server != original.Server {
		t.Errorf("Server = %q, want %q", loaded.Server, original.Server)
	}
	if loaded.Workspace != original.Workspace {
		t.Errorf("Workspace = %q, want %q", loaded.Workspace, original.Workspace)
	}
	if loaded.Token != original.Token {
		t.Errorf("Token = %q, want %q", loaded.Token, original.Token)
	}
	if loaded.Format != original.Format {
		t.Errorf("Format = %q, want %q", loaded.Format, original.Format)
	}
}

func TestConfigLoadNonexistent(t *testing.T) {
	cfg, err := loadConfigFrom("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server != "" || cfg.Token != "" {
		t.Errorf("expected empty config, got %+v", cfg)
	}
}

func TestConfigFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cli-config.yaml")

	cfg := &CLIConfig{Token: "secret-token"}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}
