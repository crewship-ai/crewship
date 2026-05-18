package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// config.go — DefaultConfigDir, DefaultConfigPath, LoadConfig, SaveConfig.
//
// Existing config_test.go covers loadConfigFrom + marshalConfig (the path-
// explicit variants used by unit tests). This file covers the four
// public entry points that hit the real ~/.crewship path — exercised
// hermetically by pointing $HOME at t.TempDir().
// ---------------------------------------------------------------------------

func TestDefaultConfigDir_PointsAtHomeCrewship(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := DefaultConfigDir()
	if err != nil {
		t.Fatalf("DefaultConfigDir: %v", err)
	}
	want := filepath.Join(tmp, ".crewship")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultConfigPath_AppendsConfigFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	want := filepath.Join(tmp, ".crewship", "cli-config.yaml")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadConfig_NoFile_ReturnsEmptyConfigWithoutError(t *testing.T) {
	// Fresh HOME with no ~/.crewship/cli-config.yaml. Source contract:
	// "returns an empty config if the file does not exist". Pin that
	// callers don't have to special-case the missing-file case before
	// every read — the loader does it for them.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil, want pointer to zero-value CLIConfig")
	}
	// All zero-valued fields → empty config.
	if cfg.Server != "" || cfg.Workspace != "" || cfg.Token != "" || cfg.Format != "" {
		t.Errorf("cfg = %+v, want zero-value", cfg)
	}
}

func TestLoadConfig_PresentFile_Parses(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := `server: http://api.example.com:8080
workspace: ws-1
token: tok-abc
format: json
default_agent: alice
notifications: true
plan_by_default: true
`
	if err := os.WriteFile(filepath.Join(dir, "cli-config.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Server != "http://api.example.com:8080" {
		t.Errorf("Server = %q", cfg.Server)
	}
	if cfg.Workspace != "ws-1" {
		t.Errorf("Workspace = %q", cfg.Workspace)
	}
	if cfg.Token != "tok-abc" {
		t.Errorf("Token = %q", cfg.Token)
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q", cfg.Format)
	}
	if cfg.DefaultAgent != "alice" {
		t.Errorf("DefaultAgent = %q", cfg.DefaultAgent)
	}
	if !cfg.Notifications {
		t.Error("Notifications = false, want true")
	}
	if !cfg.PlanByDefault {
		t.Error("PlanByDefault = false, want true")
	}
}

func TestLoadConfig_MalformedYAML_Errors(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// `:` with no value at the wrong indent makes YAML parsing fail.
	bad := "server: : :\n  not: indented properly\n :::"
	if err := os.WriteFile(filepath.Join(dir, "cli-config.yaml"), []byte(bad), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected parse error on malformed YAML")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Errorf("err = %v, want \"parse config\" prefix", err)
	}
}

func TestLoadConfig_UnreadableFile_Errors(t *testing.T) {
	// File exists but is unreadable (perms 000). Source contract: read
	// errors that aren't IsNotExist surface as "read config" errors —
	// the user needs to know their config is broken, not silently get
	// an empty config that loses their settings.
	if os.Geteuid() == 0 {
		t.Skip("root reads everything; can't simulate unreadable file")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "cli-config.yaml")
	if err := os.WriteFile(path, []byte("server: x"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) }) // restore for TempDir cleanup

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error on unreadable file")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Errorf("err = %v, want \"read config\" prefix", err)
	}
}

func TestSaveConfig_WritesFileWithSecurePerms(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := &CLIConfig{
		Server:    "http://x.example.com",
		Workspace: "ws-1",
		Token:     "secret-token",
		Format:    "json",
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// File must exist with 0600 (token is sensitive).
	path := filepath.Join(tmp, ".crewship", "cli-config.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perms = %o, want 0600 (token is in here)", perm)
	}

	// Directory must exist with 0700 (no leak to other users on shared
	// systems). The dir was created by SaveConfig — pin that perm bit
	// because the source explicitly chose 0700.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("dir stat: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perms = %o, want 0700", perm)
	}
}

func TestSaveConfig_RoundTripsThroughLoadConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	orig := &CLIConfig{
		Server:        "http://x:9090",
		Workspace:     "ws-rt",
		Token:         "tok",
		Format:        "yaml",
		DefaultAgent:  "bob",
		Notifications: true,
		PlanByDefault: true,
		Markdown:      "off",
	}
	if err := SaveConfig(orig); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if *got != *orig {
		t.Errorf("round-trip lost data:\n  saved: %+v\n  loaded: %+v", orig, got)
	}
}

func TestSaveConfig_OverwritesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// First write.
	if err := SaveConfig(&CLIConfig{Server: "first.example.com"}); err != nil {
		t.Fatalf("first SaveConfig: %v", err)
	}
	// Second write with different content must overwrite, not append.
	if err := SaveConfig(&CLIConfig{Server: "second.example.com"}); err != nil {
		t.Fatalf("second SaveConfig: %v", err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Server != "second.example.com" {
		t.Errorf("Server = %q, want second.example.com (must overwrite)", cfg.Server)
	}
}

func TestSaveConfig_DirAlreadyExists_NoError(t *testing.T) {
	// MkdirAll is a no-op if the dir already exists. Pin that calling
	// SaveConfig twice doesn't error on the second invocation when the
	// directory carries over.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("preseed dir: %v", err)
	}
	if err := SaveConfig(&CLIConfig{}); err != nil {
		t.Errorf("SaveConfig with pre-existing dir: %v", err)
	}
}
