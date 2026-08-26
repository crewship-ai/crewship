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
	if len(cfg.Env) != 0 {
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
		"node js",               // space
		"node@latest",           // @
		"node/lts",              // /
		"no$de",                 // $
		"",                      // empty
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
		"22 lts",                // space
		"",                      // empty
		"v3.12; rm -rf",         // semicolon
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

	if len(calls) != 2 {
		t.Fatalf("expected 2 exec calls, got %d", len(calls))
	}

	// First call: install mise as root, straight to /usr/local/bin. The
	// separate symlink step is gone — it pointed into root's 0700 home, which
	// the agent cannot traverse (#1779).
	if !strings.Contains(calls[0].cmd, "mise.jdx.dev/install.sh") {
		t.Errorf("call 0: expected install script, got %q", calls[0].cmd)
	}
	// The assignment has to sit on `bash`. On `curl` it reaches the fetch and
	// not the installer, which then falls back to $HOME/.local/bin and the
	// chmod fails with "No such file or directory".
	if !strings.Contains(calls[0].cmd, "| MISE_INSTALL_PATH=/usr/local/bin/mise bash") {
		t.Errorf("call 0: install path must be pinned on the shell running the script, got %q", calls[0].cmd)
	}
	if calls[0].user != "0:0" {
		t.Errorf("call 0: user = %q, want 0:0", calls[0].user)
	}

	// Second call: verify as root.
	if !strings.Contains(calls[1].cmd, "mise --version") {
		t.Errorf("call 1: expected version check, got %q", calls[1].cmd)
	}
	if calls[1].user != "0:0" {
		t.Errorf("call 1: user = %q, want 0:0", calls[1].user)
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

	if len(calls) != 5 {
		t.Fatalf("expected 5 exec calls, got %d", len(calls))
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

	// Call 4: the shims actually resolve. reshim exiting 0 does not mean
	// they do — see verifyMiseShims.
	if !strings.Contains(calls[4].cmd, miseShimsDir) {
		t.Errorf("call 4: expected a shim check, got %q", calls[4].cmd)
	}
	if calls[4].user != "1001:1001" {
		t.Errorf("call 4: user = %q, want 1001:1001", calls[4].user)
	}
}

// A dangling shim is the ONE failure this step exists to catch, and the
// reason it is worth a step at all: `mise reshim` reports success while
// writing a symlink to a target the agent cannot execute, PATH lookup skips
// it in silence, and the pin is served by whatever the base image ships
// instead. Measured on a crew provisioned before #1787: pinned terraform
// 1.9, running v1.15.7, nothing logged anywhere.
//
// So provisioning must fail, and the message must name what actually went
// wrong — not "reshim failed", which it did not.
func TestInstallMiseTools_FailsOnDanglingShims(t *testing.T) {
	mockExec := func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		joined := strings.Join(cmd, " ")
		if strings.Contains(joined, miseShimsDir) {
			return " terraform node\n", 1, nil
		}
		return "ok", 0, nil
	}

	cfg := &MiseConfig{Tools: map[string]string{"terraform": "1.9"}}
	err := InstallMiseTools(context.Background(), "test-container", cfg, mockExec)
	if err == nil {
		t.Fatal("expected provisioning to fail on dangling shims, got nil")
	}
	if !errors.Is(err, ErrMiseInstallFailed) {
		t.Errorf("error = %v, want it to wrap ErrMiseInstallFailed", err)
	}
	// The names, so the operator knows WHICH pins are not in effect.
	if !strings.Contains(err.Error(), "terraform") {
		t.Errorf("error must name the broken shims; got %q", err.Error())
	}
	// And the consequence, which is the part nobody would guess.
	if !strings.Contains(err.Error(), "silently replaced") {
		t.Errorf("error must say what the failure costs; got %q", err.Error())
	}
}

// The check must not invent a failure when there is nothing to check.
//
// With no shims the glob stays literal, so the loop variable is the pattern
// itself. A naive `[ -e "$s" ]` reports that as a broken shim named "*" and
// fails the whole provision — which would be a worse bug than the silent
// wrong version this check exists to catch. The shell guards it with
// `[ -L ]`; this pins that the Go side treats exit 0 as success and does not
// depend on any output.
func TestInstallMiseTools_PassesWhenThereAreNoShims(t *testing.T) {
	mockExec := func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		if strings.Contains(strings.Join(cmd, " "), miseShimsDir) {
			// What the shell returns for an empty or missing directory.
			return "", 0, nil
		}
		return "ok", 0, nil
	}

	cfg := &MiseConfig{Tools: map[string]string{"terraform": "1.9"}}
	if err := InstallMiseTools(context.Background(), "test-container", cfg, mockExec); err != nil {
		t.Fatalf("no shims must not fail provisioning, got %v", err)
	}
}

// The guard is a shell one-liner, so pin its shape rather than trusting the
// prose: `[ -L ]` is the difference between "there are no shims" and "the
// shims are broken", and dropping it is a silent regression.
func TestVerifyMiseShims_GuardsTheUnexpandedGlob(t *testing.T) {
	var got string
	mockExec := func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		joined := strings.Join(cmd, " ")
		if strings.Contains(joined, miseShimsDir) {
			got = joined
		}
		return "ok", 0, nil
	}

	cfg := &MiseConfig{Tools: map[string]string{"jq": "1.7"}}
	if err := InstallMiseTools(context.Background(), "test-container", cfg, mockExec); err != nil {
		t.Fatalf("InstallMiseTools: %v", err)
	}
	for _, want := range []string{`[ -L "$s" ] || continue`, `[ -e "$s" ] && continue`} {
		if !strings.Contains(got, want) {
			t.Errorf("shim check missing %q; got:\n%s", want, got)
		}
	}
}

// mise writes to three XDG roots besides its config: tool payloads to
// $XDG_DATA_HOME/mise, tracked-config state to $XDG_STATE_HOME/mise and
// downloads to $XDG_CACHE_HOME/mise. Only .config/mise used to be created and
// chowned, so on a base image where /home/agent/.local is root-owned (a bare
// debian runtime image plus common-utils, as the E2E provisioning test uses)
// `mise install` running as 1001 died with
//
//	create_dir_all: ~/.local/state/mise/tracked-configs: Permission denied
//
// Pin that every dir mise needs is prepared and handed to the agent user
// before the install runs, and that the XDG vars are set so mise cannot fall
// back to a path we did not prepare.
func TestInstallMiseTools_PreparesAgentOwnedXDGDirs(t *testing.T) {
	type call struct {
		cmd  string
		user string
		env  []string
	}
	var calls []call

	mockExec := func(_ context.Context, _ string, cmd []string, user string, env []string) (string, int, error) {
		calls = append(calls, call{cmd: strings.Join(cmd, " "), user: user, env: env})
		return "ok", 0, nil
	}

	cfg := &MiseConfig{Tools: map[string]string{"node": "22"}}
	if err := InstallMiseTools(context.Background(), "test-container", cfg, mockExec); err != nil {
		t.Fatalf("InstallMiseTools: %v", err)
	}

	// Every dir mise writes to must be created before the install, and the
	// creation must happen as root (the agent user cannot mkdir under a
	// root-owned home).
	wantDirs := []string{
		"/home/agent/.config/mise",
		"/home/agent/.local/share/mise",
		"/home/agent/.local/state/mise",
		"/home/agent/.cache/mise",
	}
	var mkdirIdx, chownIdx, installIdx = -1, -1, -1
	for i, c := range calls {
		switch {
		case strings.Contains(c.cmd, "mkdir -p") && mkdirIdx < 0:
			mkdirIdx = i
		case strings.Contains(c.cmd, "chown") && chownIdx < 0:
			chownIdx = i
		case strings.Contains(c.cmd, "mise install") && installIdx < 0:
			installIdx = i
		}
	}
	if mkdirIdx < 0 || chownIdx < 0 || installIdx < 0 {
		t.Fatalf("missing mkdir/chown/install call in %v", calls)
	}
	if mkdirIdx > installIdx || chownIdx > installIdx {
		t.Errorf("dirs prepared after install (mkdir=%d chown=%d install=%d)", mkdirIdx, chownIdx, installIdx)
	}
	for _, dir := range wantDirs {
		if !strings.Contains(calls[mkdirIdx].cmd, dir) {
			t.Errorf("mkdir call does not create %s: %q", dir, calls[mkdirIdx].cmd)
		}
		if !strings.Contains(calls[chownIdx].cmd, dir) {
			t.Errorf("chown call does not cover %s: %q", dir, calls[chownIdx].cmd)
		}
	}
	if calls[mkdirIdx].user != "0:0" || calls[chownIdx].user != "0:0" {
		t.Errorf("dir preparation must run as root, got mkdir=%q chown=%q",
			calls[mkdirIdx].user, calls[chownIdx].user)
	}
	if !strings.Contains(calls[chownIdx].cmd, "1001:1001") {
		t.Errorf("chown does not hand the dirs to the agent user: %q", calls[chownIdx].cmd)
	}

	// Both agent-side mise invocations must pin the XDG roots.
	wantEnv := []string{
		"HOME=/home/agent",
		"XDG_CONFIG_HOME=/home/agent/.config",
		"XDG_DATA_HOME=/home/agent/.local/share",
		"XDG_STATE_HOME=/home/agent/.local/state",
		"XDG_CACHE_HOME=/home/agent/.cache",
	}
	for _, c := range calls {
		if c.user != "1001:1001" {
			continue
		}
		joined := strings.Join(c.env, " ")
		for _, want := range wantEnv {
			if !strings.Contains(joined, want) {
				t.Errorf("agent call %q missing env %s (got %v)", c.cmd, want, c.env)
			}
		}
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

// mise installs itself under $HOME/.local/bin, and as root that is
// /root/.local/bin — which is mode 0700 on the devcontainer base images. The
// symlink into /usr/local/bin therefore pointed somewhere uid 1001 cannot
// traverse, and `mise install` (which runs as the agent) died with exit 126,
// "permission denied". Found on the Apple build path (#1779); the same
// dependency was there on the commit path.
func TestInstallMise_DoesNotLeaveTheBinaryBehindRootsHome(t *testing.T) {
	var cmds []string
	rec := func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		cmds = append(cmds, strings.Join(cmd, " "))
		return "", 0, nil
	}

	if err := InstallMise(context.Background(), "cid", rec); err != nil {
		t.Fatalf("InstallMise: %v", err)
	}

	all := strings.Join(cmds, "\n")
	if strings.Contains(all, "/root/.local/bin/mise") {
		t.Errorf("mise must not be reachable only through root's home:\n%s", all)
	}
	if !strings.Contains(all, "/usr/local/bin/mise") {
		t.Errorf("expected mise to be installed somewhere every user can execute:\n%s", all)
	}
}
