package devcontainer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A real Mise shim needs its managed config and data locations, even when
// command -v finds it. The previous printf-only version fixture missed this.
func TestVerifyBinariesResolveRunsManagedMiseShim(t *testing.T) {
	dir := t.TempDir()
	shim := `#!/bin/sh
[ "${MISE_CONFIG_DIR:-}" = /opt/mise/config ] || exit 71
[ "${MISE_DATA_DIR:-}" = /opt/mise/data ] || exit 72
[ "${MISE_GLOBAL_CONFIG_FILE:-}" = /opt/mise/config/config.toml ] || exit 73
[ "${MISE_STATE_DIR:-}" = /home/agent/.local/state/mise ] || exit 74
[ "${MISE_CACHE_DIR:-}" = /home/agent/.cache/mise ] || exit 75
printf '%s\n' 2.3.0
`
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, _ string, args []string, user string, env []string) (string, int, error) {
		if user != "1001:1001" {
			t.Fatalf("unexpected probe user %q", user)
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		// Deliberately exclude the host's Mise configuration. Docker exec must
		// receive the contract explicitly, not accidentally inherit our host.
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			var exited *exec.ExitError
			if errors.As(err, &exited) {
				return string(output), exited.ExitCode(), nil
			}
			return string(output), -1, err
		}
		return string(output), 0, nil
	}
	if err := VerifyBinariesResolve(t.Context(), "probe", []string{"opencode"}, dir+":/usr/bin:/bin", run); err != nil {
		t.Fatalf("managed OpenCode shim could not load its installed toolchain: %v", err)
	}
}

func TestVerifyRequiredBinariesRestrictsProbeEnvironment(t *testing.T) {
	for _, dataDir := range []string{"/declared/mise/data", ""} {
		t.Run(dataDir, func(t *testing.T) {
			runtimeEnv := map[string]string{
				"PATH": "/declared/bin:${containerEnv:PATH}",
				"HOME": "/unexpected/home", "USER": "unexpected-user",
				"MISE_DATA_DIR": dataDir,
				"SECRET_TOKEN":  "must-not-cross-probe-boundary",
				"ENV":           "/unexpected/env", "BASH_ENV": "/unexpected/bash-env",
				"LD_PRELOAD": "/unexpected/preload",
			}
			var captured []string
			execute := func(_ context.Context, _ string, args []string, user string, env []string) (string, int, error) {
				captured = append([]string(nil), env...)
				if user != "1001:1001" || len(args) != 3 {
					t.Fatal("probe execution contract changed")
				}
				return "", 0, nil
			}
			if err := (&Provisioner{}).verifyRequiredBinaries(t.Context(), "probe", []string{"opencode"}, runtimeEnv, execute); err != nil {
				t.Fatal(err)
			}
			want := []string{
				"HOME=/home/agent", "USER=agent", "MISE_CONFIG_DIR=/opt/mise/config",
				"MISE_DATA_DIR=" + dataDir, "MISE_STATE_DIR=/home/agent/.local/state/mise",
				"MISE_CACHE_DIR=/home/agent/.cache/mise", "MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml",
			}
			if !reflect.DeepEqual(captured, want) {
				t.Fatalf("probe env = %q, want only %q", captured, want)
			}
			if runtimeEnv["HOME"] != "/unexpected/home" || runtimeEnv["MISE_DATA_DIR"] != dataDir {
				t.Fatal("probe modified the caller's runtime configuration")
			}
		})
	}
}

func TestVerifyRequiredBinariesRecordsOnlyManagedEnvironment(t *testing.T) {
	recorder := &dockerfileRecorder{}
	env := map[string]string{"PATH": "/declared/bin:${containerEnv:PATH}", "MISE_CONFIG_DIR": "/declared/config", "SECRET_TOKEN": "must-not-enter-image-history", "BASH_ENV": "/untrusted/bootstrap"}
	if err := (&Provisioner{}).verifyRequiredBinaries(t.Context(), "", []string{"opencode"}, env, recorder.exec); err != nil {
		t.Fatal(err)
	}
	if len(recorder.groups) != 1 || recorder.groups[0].user != "1001:1001" {
		t.Fatal("probe must remain an agent-user build layer")
	}
	line := strings.Join(recorder.groups[0].cmds, "\n")
	for _, required := range []string{"MISE_CONFIG_DIR=/declared/config", "MISE_DATA_DIR=/opt/mise/data", "MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml"} {
		if !strings.Contains(line, required) {
			t.Fatalf("recorded probe omits %s", required)
		}
	}
	for _, forbidden := range []string{"SECRET_TOKEN", "must-not-enter-image-history", "BASH_ENV", "/untrusted/bootstrap"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("unrelated container environment entered recorded probe: %s", forbidden)
		}
	}
}
