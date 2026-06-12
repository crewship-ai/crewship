package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnvOverrides_FullSurface sets every env override applyEnvOverrides
// honours and asserts each lands on the right Config field. The package's
// TestMain already exports CREWSHIP_SKIP_SIDECAR=1, so Load never fails on
// sidecar autodetection.
func TestEnvOverrides_FullSurface(t *testing.T) {
	t.Setenv("CREWSHIP_HOST", "127.0.0.9")
	t.Setenv("CREWSHIP_PORT", "9123")
	t.Setenv("CREWSHIP_SOCKET_PATH", "/tmp/other.sock")
	t.Setenv("CREWSHIP_CONTAINER_PROVIDER", "auto")
	t.Setenv("CREWSHIP_CONTAINER_NETWORK", "net-x")
	t.Setenv("CREWSHIP_CONTAINER_PREFIX", "ci-")
	t.Setenv("CREWSHIP_STORAGE_PROVIDER", "s3")
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", "/data/base")
	t.Setenv("CREWSHIP_LOG_PATH", "/data/logs")
	t.Setenv("CREWSHIP_STATE_PROVIDER", "bbolt")
	t.Setenv("CREWSHIP_BOLT_PATH", "/data/state.db")
	t.Setenv("CREWSHIP_LOG_LEVEL", "debug")
	t.Setenv("CREWSHIP_LOG_FORMAT", "text")
	t.Setenv("NEXTAUTH_SECRET", "jwt-secret-from-env")
	t.Setenv("CREWSHIP_RUNTIME_IMAGE", "ubuntu:24.04")
	t.Setenv("CREWSHIP_SIDECAR_ENABLED", "yes")
	t.Setenv("CREWSHIP_SIDECAR_PATH", "/opt/crewship-sidecar")
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", "/opt/entrypoint.sh")
	t.Setenv("CREWSHIP_NEXTJS_URL", "http://nextjs.example:3000")
	t.Setenv("CREWSHIP_INTERNAL_TOKEN", "tok-internal")
	t.Setenv("CREWSHIP_ALLOW_SIGNUP", "on")
	t.Setenv("GOOGLE_CLIENT_ID", "gcid")
	t.Setenv("GOOGLE_CLIENT_SECRET", "gsecret")
	t.Setenv("KEEPER_ENABLED", "true")
	t.Setenv("KEEPER_OLLAMA_URL", "http://ollama:11434")
	t.Setenv("KEEPER_MODEL", "claude-haiku-4-5")
	t.Setenv("CREWSHIP_LICENSE_FILE", "/etc/crewship/license.key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Server.Host", cfg.Server.Host, "127.0.0.9"},
		{"Server.Port", cfg.Server.Port, 9123},
		{"IPC.SocketPath", cfg.IPC.SocketPath, "/tmp/other.sock"},
		{"Container.Provider", cfg.Container.Provider, "auto"},
		{"Container.Network", cfg.Container.Network, "net-x"},
		{"Container.ContainerPrefix", cfg.Container.ContainerPrefix, "ci-"},
		{"Storage.Provider", cfg.Storage.Provider, "s3"},
		{"Storage.BasePath", cfg.Storage.BasePath, "/data/base"},
		{"Storage.LogPath", cfg.Storage.LogPath, "/data/logs"},
		{"State.Provider", cfg.State.Provider, "bbolt"},
		{"State.BoltPath", cfg.State.BoltPath, "/data/state.db"},
		{"Logging.Level", cfg.Logging.Level, "debug"},
		{"Logging.Format", cfg.Logging.Format, "text"},
		{"Auth.JWTSecret", cfg.Auth.JWTSecret, "jwt-secret-from-env"},
		{"Container.RuntimeImage", cfg.Container.RuntimeImage, "ubuntu:24.04"},
		{"Container.SidecarEnabled", cfg.Container.SidecarEnabled, true},
		{"Container.SidecarBinaryPath", cfg.Container.SidecarBinaryPath, "/opt/crewship-sidecar"},
		{"Container.EntrypointPath", cfg.Container.EntrypointPath, "/opt/entrypoint.sh"},
		{"Auth.NextjsURL", cfg.Auth.NextjsURL, "http://nextjs.example:3000"},
		{"Auth.InternalToken", cfg.Auth.InternalToken, "tok-internal"},
		{"Auth.AllowSignup", cfg.Auth.AllowSignup, true},
		{"Auth.GoogleClientID", cfg.Auth.GoogleClientID, "gcid"},
		{"Auth.GoogleSecret", cfg.Auth.GoogleSecret, "gsecret"},
		{"Keeper.Enabled", cfg.Keeper.Enabled, true},
		{"Keeper.OllamaURL", cfg.Keeper.OllamaURL, "http://ollama:11434"},
		{"Keeper.Model", cfg.Keeper.Model, "claude-haiku-4-5"},
		{"License.FilePath", cfg.License.FilePath, "/etc/crewship/license.key"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestEnvOverrides_InvalidPortIgnored(t *testing.T) {
	t.Setenv("CREWSHIP_PORT", "not-a-number")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Port = %d, want default 8080 when CREWSHIP_PORT is garbage", cfg.Server.Port)
	}
}

func TestSidecarAutoEnable_WhenBinaryPathConfigured(t *testing.T) {
	// No CREWSHIP_SIDECAR_ENABLED, but a binary path set → auto-enable.
	t.Setenv("CREWSHIP_SIDECAR_PATH", "/opt/crewship-sidecar")
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", "/opt/entrypoint.sh")
	cfg := Default()
	// Path must be visible before the auto-enable check runs; the env
	// var is applied later in applyEnvOverrides, so model the YAML case.
	cfg.Container.SidecarBinaryPath = "/opt/crewship-sidecar"
	applyEnvOverrides(cfg)
	if !cfg.Container.SidecarEnabled {
		t.Error("SidecarEnabled = false, want auto-enable when binary path is set")
	}

	// Explicit opt-out wins over auto-enable.
	t.Setenv("CREWSHIP_SIDECAR_ENABLED", "false")
	cfg2 := Default()
	cfg2.Container.SidecarBinaryPath = "/opt/crewship-sidecar"
	applyEnvOverrides(cfg2)
	if cfg2.Container.SidecarEnabled {
		t.Error("SidecarEnabled = true despite explicit CREWSHIP_SIDECAR_ENABLED=false")
	}
}

func TestKeeperAutoEnable_FromOllamaURL(t *testing.T) {
	t.Setenv("KEEPER_OLLAMA_URL", "http://ollama:11434")
	t.Setenv("KEEPER_MODEL", "claude-haiku-4-5")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Keeper.Enabled {
		t.Error("Keeper.Enabled = false, want auto-enable when KEEPER_OLLAMA_URL set")
	}

	// Explicit disable suppresses auto-enable.
	t.Setenv("KEEPER_ENABLED", "0")
	cfg2, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Keeper.Enabled {
		t.Error("Keeper.Enabled = true despite KEEPER_ENABLED=0")
	}

	// An unrecognised KEEPER_ENABLED value is ignored — auto-enable
	// still fires (the envBool gate, not raw Getenv emptiness).
	t.Setenv("KEEPER_ENABLED", "maybe")
	cfg3, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg3.Keeper.Enabled {
		t.Error("Keeper.Enabled = false; unparseable KEEPER_ENABLED must not suppress auto-enable")
	}
}

func TestValidate_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"port zero", func(c *Config) { c.Server.Port = 0 }, "server.port"},
		{"port too high", func(c *Config) { c.Server.Port = 70000 }, "server.port"},
		{"empty socket", func(c *Config) { c.IPC.SocketPath = "" }, "ipc.socket_path"},
		{"bad container provider", func(c *Config) { c.Container.Provider = "podman" }, "container.provider"},
		{"bad storage provider", func(c *Config) { c.Storage.Provider = "nfs" }, "storage.provider"},
		{"bad state provider", func(c *Config) { c.State.Provider = "postgres" }, "state.provider"},
		{"bad runtime", func(c *Config) { c.Container.DefaultRuntime = "youki" }, "container.default_runtime"},
		{"empty nextjs url", func(c *Config) { c.Auth.NextjsURL = "" }, "auth.nextjs_url"},
		{"keeper enabled no model", func(c *Config) { c.Keeper.Enabled = true; c.Keeper.Model = "  " }, "keeper.model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Auth.NextjsURL = "http://localhost:8080"
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want mention of %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidate_TrimsKeeperModel(t *testing.T) {
	cfg := Default()
	cfg.Auth.NextjsURL = "http://localhost:8080"
	cfg.Keeper.Enabled = true
	cfg.Keeper.Model = "  claude-haiku-4-5  "
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Keeper.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want whitespace trimmed", cfg.Keeper.Model)
	}
}

func TestLoad_BadYAMLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("server: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "load config file") {
		t.Errorf("err = %v, want load-config-file error", err)
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Error("Load with nonexistent path succeeded, want error")
	}
}

func TestLoad_ValidationFailureSurfaces(t *testing.T) {
	t.Setenv("CREWSHIP_CONTAINER_PROVIDER", "bogus")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "config validation") {
		t.Errorf("err = %v, want config-validation error", err)
	}
}

func TestLoad_DerivesNextjsURLFromPort(t *testing.T) {
	t.Setenv("CREWSHIP_PORT", "9555")
	t.Setenv("CREWSHIP_NEXTJS_URL", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.NextjsURL != "http://localhost:9555" {
		t.Errorf("NextjsURL = %q, want derived http://localhost:9555", cfg.Auth.NextjsURL)
	}
}

func TestLoad_GeneratesInternalToken(t *testing.T) {
	t.Setenv("CREWSHIP_INTERNAL_TOKEN", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Auth.InternalToken) != 64 { // 32 bytes hex-encoded
		t.Errorf("InternalToken length = %d, want 64 hex chars", len(cfg.Auth.InternalToken))
	}
	if cfg.Auth.InternalToken == "crewshipd" {
		t.Error("InternalToken fell back to the legacy hardcoded value")
	}
}

func TestEnvBool_UnrecognizedValueIgnored(t *testing.T) {
	t.Setenv("CREWSHIP_ALLOW_SIGNUP", "maybe")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.AllowSignup {
		t.Error("AllowSignup = true from unrecognised boolean, want default false")
	}
}

// TestAutodetect_FindsEntrypointInCwdScripts proves the cwd-relative
// scripts/entrypoint.sh candidate is picked up when no explicit path is
// configured. Sidecar path is pinned via env so detection can't fail on
// machines without a built sidecar.
func TestAutodetect_FindsEntrypointInCwdScripts(t *testing.T) {
	dir := t.TempDir()
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(scripts, "entrypoint.sh")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg := Default()
	cfg.Container.SidecarBinaryPath = "/opt/crewship-sidecar"
	if err := autodetectSidecarPaths(cfg); err != nil {
		t.Fatalf("autodetectSidecarPaths: %v", err)
	}
	if cfg.Container.EntrypointPath != entry {
		t.Errorf("EntrypointPath = %q, want autodetected %q", cfg.Container.EntrypointPath, entry)
	}
}
