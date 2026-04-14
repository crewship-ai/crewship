package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain bypasses sidecar/entrypoint autodetect for the whole package —
// these tests run in environments without a built sidecar binary, and none
// of them exercise container startup.
func TestMain(m *testing.M) {
	_ = os.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	os.Exit(m.Run())
}

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Container.Provider != "docker" {
		t.Errorf("expected docker provider, got %s", cfg.Container.Provider)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected info level, got %s", cfg.Logging.Level)
	}
	if cfg.Auth.WSTokenExpiry != 5*time.Minute {
		t.Errorf("expected 5m ws token expiry, got %s", cfg.Auth.WSTokenExpiry)
	}
}

func TestLoadFromFile(t *testing.T) {
	content := `
server:
  host: "127.0.0.1"
  port: 9090
logging:
  level: "debug"
container:
  provider: "k8s"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected debug level, got %s", cfg.Logging.Level)
	}
	if cfg.Container.Provider != "k8s" {
		t.Errorf("expected k8s provider, got %s", cfg.Container.Provider)
	}
	// Defaults preserved for unset fields
	if cfg.Storage.Provider != "localfs" {
		t.Errorf("expected localfs storage, got %s", cfg.Storage.Provider)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("CREWSHIP_PORT", "7777")
	t.Setenv("CREWSHIP_CONTAINER_PROVIDER", "k8s")
	t.Setenv("CREWSHIP_LOG_LEVEL", "warn")
	t.Setenv("CREWSHIP_NEXTJS_URL", "http://nextjs:3000")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Port != 7777 {
		t.Errorf("expected port 7777, got %d", cfg.Server.Port)
	}
	if cfg.Container.Provider != "k8s" {
		t.Errorf("expected k8s, got %s", cfg.Container.Provider)
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("expected warn, got %s", cfg.Logging.Level)
	}
	if cfg.Auth.NextjsURL != "http://nextjs:3000" {
		t.Errorf("expected nextjs url, got %s", cfg.Auth.NextjsURL)
	}
}

func TestEnvOverridesFileValues(t *testing.T) {
	content := `
server:
  port: 9090
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CREWSHIP_PORT", "5555")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// Env should override file
	if cfg.Server.Port != 5555 {
		t.Errorf("expected env override port 5555, got %d", cfg.Server.Port)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadEmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port, got %d", cfg.Server.Port)
	}
}

func TestAutodetectSidecarPaths_FailFastWhenMissing(t *testing.T) {
	// Disable the package-wide skip so we see the real failure path.
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	t.Setenv("CREWSHIP_SIDECAR_PATH", "")
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", "")
	// Point to an isolated CWD so autodetect's scripts/entrypoint.sh
	// lookup cannot accidentally succeed from the repo tree.
	t.Chdir(t.TempDir())

	cfg := Default()
	err := autodetectSidecarPaths(cfg)
	if err == nil {
		t.Fatal("expected error when sidecar + entrypoint cannot be autodetected")
	}
}

func TestAutodetectSidecarPaths_HonorsExplicitPaths(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "crewship-sidecar")
	entry := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(sidecar, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Container.SidecarBinaryPath = sidecar
	cfg.Container.EntrypointPath = entry
	if err := autodetectSidecarPaths(cfg); err != nil {
		t.Fatalf("autodetect should succeed with explicit paths: %v", err)
	}
}

func TestAutodetectSidecarPaths_SkipEnvBypassesCheck(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	t.Chdir(t.TempDir())
	cfg := Default()
	if err := autodetectSidecarPaths(cfg); err != nil {
		t.Fatalf("CREWSHIP_SKIP_SIDECAR=1 must bypass check, got: %v", err)
	}
}

func TestValidationInvalidPort(t *testing.T) {
	cfg := Default()
	cfg.Server.Port = 0

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for port 0")
	}
}

func TestValidationInvalidPortHigh(t *testing.T) {
	cfg := Default()
	cfg.Server.Port = 99999

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for port 99999")
	}
}

func TestValidationInvalidContainerProvider(t *testing.T) {
	cfg := Default()
	cfg.Container.Provider = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid container provider")
	}
}

func TestValidationContainerProviders(t *testing.T) {
	t.Parallel()
	for _, prov := range []string{"apple", "auto"} {
		t.Run(prov, func(t *testing.T) {
			t.Parallel()
			cfg := Default()
			cfg.Container.Provider = prov
			if err := cfg.Validate(); err != nil {
				t.Errorf("expected %s provider to be valid, got: %v", prov, err)
			}
		})
	}
}

func TestValidationInvalidStorageProvider(t *testing.T) {
	cfg := Default()
	cfg.Storage.Provider = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid storage provider")
	}
}

func TestValidationInvalidStateProvider(t *testing.T) {
	cfg := Default()
	cfg.State.Provider = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid state provider")
	}
}

func TestValidationEmptySocketPath(t *testing.T) {
	cfg := Default()
	cfg.IPC.SocketPath = ""

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for empty socket path")
	}
}

func TestDefaultNextjsURL(t *testing.T) {
	cfg := Default()
	if cfg.Auth.NextjsURL != "http://localhost:8080" {
		t.Errorf("expected default NextjsURL http://localhost:8080, got %q", cfg.Auth.NextjsURL)
	}
}

func TestInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yml")
	if err := os.WriteFile(path, []byte("{{invalid yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestAutoGenerateInternalToken(t *testing.T) {
	os.Unsetenv("CREWSHIP_INTERNAL_TOKEN")
	defer os.Unsetenv("CREWSHIP_INTERNAL_TOKEN")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.InternalToken == "" {
		t.Fatal("expected auto-generated internal token, got empty")
	}
	if cfg.Auth.InternalToken == "crewshipd" {
		t.Fatal("internal token must not be the old hardcoded default")
	}
	if len(cfg.Auth.InternalToken) < 32 {
		t.Errorf("expected token >= 32 chars, got %d", len(cfg.Auth.InternalToken))
	}

	// Ensure each Load generates a different token
	cfg2, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.InternalToken == cfg2.Auth.InternalToken {
		t.Error("two separate loads should generate different tokens")
	}
}

func TestExplicitInternalToken(t *testing.T) {
	os.Setenv("CREWSHIP_INTERNAL_TOKEN", "my-custom-token-12345")
	defer os.Unsetenv("CREWSHIP_INTERNAL_TOKEN")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.InternalToken != "my-custom-token-12345" {
		t.Errorf("expected explicit token, got %q", cfg.Auth.InternalToken)
	}
}
