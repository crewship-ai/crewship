package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseMiseConfig(t *testing.T) {
	data := `{"tools": {"node": "22", "python": "3.12"}, "env": {"NODE_OPTIONS": "--max-old-space-size=4096"}}`
	cfg, err := ParseMiseConfig(data)
	if err != nil {
		t.Fatalf("ParseMiseConfig: %v", err)
	}
	if cfg.Tools["node"] != "22" {
		t.Errorf("Tools[node] = %q, want 22", cfg.Tools["node"])
	}
	if cfg.Tools["python"] != "3.12" {
		t.Errorf("Tools[python] = %q, want 3.12", cfg.Tools["python"])
	}
	if cfg.Env["NODE_OPTIONS"] != "--max-old-space-size=4096" {
		t.Errorf("Env[NODE_OPTIONS] = %q", cfg.Env["NODE_OPTIONS"])
	}
}

func TestParseMiseConfig_Empty(t *testing.T) {
	data := `{"tools": {}}`
	cfg, err := ParseMiseConfig(data)
	if err != nil {
		t.Fatalf("ParseMiseConfig: %v", err)
	}
	if len(cfg.Tools) != 0 {
		t.Errorf("Tools = %v, want empty", cfg.Tools)
	}
	if cfg.Env != nil && len(cfg.Env) != 0 {
		t.Errorf("Env = %v, want nil or empty", cfg.Env)
	}
}

func TestParseMiseConfig_Invalid(t *testing.T) {
	_, err := ParseMiseConfig(`{not json}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMiseConfig_ToTOML(t *testing.T) {
	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22", "python": "3.12"},
		Env:   map[string]string{"NODE_OPTIONS": "--max-old-space-size=4096"},
	}
	toml := cfg.ToTOML()

	// Verify tools section.
	if !strings.Contains(toml, "[tools]") {
		t.Error("missing [tools] section")
	}
	if !strings.Contains(toml, `node = "22"`) {
		t.Errorf("missing node tool in:\n%s", toml)
	}
	if !strings.Contains(toml, `python = "3.12"`) {
		t.Errorf("missing python tool in:\n%s", toml)
	}

	// Verify env section.
	if !strings.Contains(toml, "[env]") {
		t.Error("missing [env] section")
	}
	if !strings.Contains(toml, `NODE_OPTIONS = "--max-old-space-size=4096"`) {
		t.Errorf("missing NODE_OPTIONS env in:\n%s", toml)
	}

	// Verify tools come before env.
	toolsIdx := strings.Index(toml, "[tools]")
	envIdx := strings.Index(toml, "[env]")
	if toolsIdx >= envIdx {
		t.Error("[tools] should appear before [env]")
	}
}

func TestMiseConfig_ToTOML_NoEnv(t *testing.T) {
	cfg := &MiseConfig{
		Tools: map[string]string{"go": "1.22"},
	}
	toml := cfg.ToTOML()

	if !strings.Contains(toml, "[tools]") {
		t.Error("missing [tools] section")
	}
	if !strings.Contains(toml, `go = "1.22"`) {
		t.Errorf("missing go tool in:\n%s", toml)
	}
	if strings.Contains(toml, "[env]") {
		t.Errorf("should not contain [env] section:\n%s", toml)
	}
}

func TestMiseConfig_Validate(t *testing.T) {
	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22", "python": "3.12.3", "ruby": "stable"},
		Env:   map[string]string{"NODE_OPTIONS": "--max-old-space-size=4096"},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestMiseConfig_Validate_BadToolName(t *testing.T) {
	cases := []string{
		"node js",       // space
		"node@latest",   // @
		"node/lts",      // /
		"no$de",         // $
		"",              // empty
		strings.Repeat("a", 51), // too long
	}
	for _, name := range cases {
		cfg := &MiseConfig{Tools: map[string]string{name: "22"}}
		err := cfg.Validate()
		if !errors.Is(err, ErrMiseInvalidToolName) {
			t.Errorf("Validate tool name %q: got %v, want ErrMiseInvalidToolName", name, err)
		}
	}
}

func TestMiseConfig_Validate_BadVersion(t *testing.T) {
	cases := []string{
		"22 lts",        // space
		"",              // empty
		"v3.12; rm -rf", // semicolon
		strings.Repeat("1", 31), // too long
	}
	for _, ver := range cases {
		cfg := &MiseConfig{Tools: map[string]string{"node": ver}}
		err := cfg.Validate()
		if !errors.Is(err, ErrMiseInvalidVersion) {
			t.Errorf("Validate version %q: got %v, want ErrMiseInvalidVersion", ver, err)
		}
	}
}

func TestMiseConfig_Validate_TooManyTools(t *testing.T) {
	tools := make(map[string]string, 21)
	for i := 0; i < 21; i++ {
		tools[fmt.Sprintf("tool%d", i)] = "1"
	}
	cfg := &MiseConfig{Tools: tools}
	err := cfg.Validate()
	if !errors.Is(err, ErrMiseTooManyTools) {
		t.Errorf("Validate: got %v, want ErrMiseTooManyTools", err)
	}
}

func TestMiseConfig_IsEmpty(t *testing.T) {
	empty := &MiseConfig{Tools: map[string]string{}}
	if !empty.IsEmpty() {
		t.Error("IsEmpty should return true for no tools")
	}

	notEmpty := &MiseConfig{Tools: map[string]string{"node": "22"}}
	if notEmpty.IsEmpty() {
		t.Error("IsEmpty should return false when tools exist")
	}
}

func TestInstallMise(t *testing.T) {
	type call struct {
		cmd  string
		user string
	}
	var calls []call

	mockExec := func(_ context.Context, containerID string, cmd []string, user string, env []string) (string, int, error) {
		if containerID != "test-container" {
			t.Errorf("unexpected containerID: %s", containerID)
		}
		calls = append(calls, call{cmd: strings.Join(cmd, " "), user: user})
		return "ok", 0, nil
	}

	err := InstallMise(context.Background(), "test-container", mockExec)
	if err != nil {
		t.Fatalf("InstallMise: %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 exec calls, got %d", len(calls))
	}

	// First call: download mise as root.
	if !strings.Contains(calls[0].cmd, "mise.jdx.dev/install.sh") {
		t.Errorf("call 0: expected install script, got %q", calls[0].cmd)
	}
	if calls[0].user != "0:0" {
		t.Errorf("call 0: user = %q, want 0:0", calls[0].user)
	}

	// Second call: symlink as root.
	if !strings.Contains(calls[1].cmd, "ln -sf") {
		t.Errorf("call 1: expected symlink, got %q", calls[1].cmd)
	}
	if calls[1].user != "0:0" {
		t.Errorf("call 1: user = %q, want 0:0", calls[1].user)
	}

	// Third call: verify as root.
	if !strings.Contains(calls[2].cmd, "mise --version") {
		t.Errorf("call 2: expected version check, got %q", calls[2].cmd)
	}
	if calls[2].user != "0:0" {
		t.Errorf("call 2: user = %q, want 0:0", calls[2].user)
	}
}

func TestInstallMiseTools(t *testing.T) {
	type call struct {
		cmd  string
		user string
	}
	var calls []call

	mockExec := func(_ context.Context, containerID string, cmd []string, user string, env []string) (string, int, error) {
		if containerID != "test-container" {
			t.Errorf("unexpected containerID: %s", containerID)
		}
		calls = append(calls, call{cmd: strings.Join(cmd, " "), user: user})
		return "ok", 0, nil
	}

	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22", "python": "3.12"},
	}

	err := InstallMiseTools(context.Background(), "test-container", cfg, mockExec)
	if err != nil {
		t.Fatalf("InstallMiseTools: %v", err)
	}

	if len(calls) != 4 {
		t.Fatalf("expected 4 exec calls, got %d", len(calls))
	}

	// Call 0: write config as root.
	if !strings.Contains(calls[0].cmd, "config.toml") {
		t.Errorf("call 0: expected config write, got %q", calls[0].cmd)
	}
	if calls[0].user != "0:0" {
		t.Errorf("call 0: user = %q, want 0:0", calls[0].user)
	}
	// Verify TOML content is embedded in the command.
	if !strings.Contains(calls[0].cmd, `node = "22"`) {
		t.Errorf("call 0: missing node tool in config write command")
	}
	if !strings.Contains(calls[0].cmd, `python = "3.12"`) {
		t.Errorf("call 0: missing python tool in config write command")
	}

	// Call 1: chown as root.
	if !strings.Contains(calls[1].cmd, "chown") {
		t.Errorf("call 1: expected chown, got %q", calls[1].cmd)
	}
	if calls[1].user != "0:0" {
		t.Errorf("call 1: user = %q, want 0:0", calls[1].user)
	}

	// Call 2: mise install as agent.
	if !strings.Contains(calls[2].cmd, "mise install --yes") {
		t.Errorf("call 2: expected mise install, got %q", calls[2].cmd)
	}
	if calls[2].user != "1001:1001" {
		t.Errorf("call 2: user = %q, want 1001:1001", calls[2].user)
	}

	// Call 3: mise reshim as agent.
	if !strings.Contains(calls[3].cmd, "mise reshim") {
		t.Errorf("call 3: expected mise reshim, got %q", calls[3].cmd)
	}
	if calls[3].user != "1001:1001" {
		t.Errorf("call 3: user = %q, want 1001:1001", calls[3].user)
	}
}

func TestInstallMiseTools_Empty(t *testing.T) {
	called := false
	mockExec := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		called = true
		return "", 0, nil
	}

	cfg := &MiseConfig{Tools: map[string]string{}}
	err := InstallMiseTools(context.Background(), "test-container", cfg, mockExec)
	if err != nil {
		t.Fatalf("InstallMiseTools: %v", err)
	}
	if called {
		t.Error("exec should not be called for empty config")
	}
}

func TestInstallMise_Failure(t *testing.T) {
	mockExec := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		return "connection refused", 1, nil
	}

	err := InstallMise(context.Background(), "test-container", mockExec)
	if !errors.Is(err, ErrMiseInstallFailed) {
		t.Errorf("expected ErrMiseInstallFailed, got %v", err)
	}
}

func TestMiseConfig_Validate_BadEnvKey(t *testing.T) {
	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22"},
		Env:   map[string]string{"lower_case": "value"},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrMiseInvalidEnvKey) {
		t.Errorf("Validate: got %v, want ErrMiseInvalidEnvKey", err)
	}
}

func TestMiseConfig_Validate_NullByteEnvValue(t *testing.T) {
	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22"},
		Env:   map[string]string{"MY_VAR": "value\x00injected"},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrMiseInvalidEnvValue) {
		t.Errorf("Validate: got %v, want ErrMiseInvalidEnvValue", err)
	}
}

func TestMiseConfig_Validate_TooManyEnvVars(t *testing.T) {
	env := make(map[string]string, 21)
	for i := 0; i < 21; i++ {
		env[fmt.Sprintf("VAR_%d", i)] = "value"
	}
	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22"},
		Env:   env,
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrMiseTooManyEnvVars) {
		t.Errorf("Validate: got %v, want ErrMiseTooManyEnvVars", err)
	}
}
