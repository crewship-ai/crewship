//go:build !windows

package devcontainer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// legacyDefaultContainerPath is the containerEnv.PATH literal Crewship wrote
// into DefaultCrewDevcontainerConfig and the seeded crews before #5459.
const legacyDefaultContainerPath = "/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// rootedPath maps every absolute PATH entry under root, so lookup runs against
// real directories with the same relative order as inside the container.
func rootedPath(root, path string) string {
	parts := strings.Split(path, ":")
	for i, dir := range parts {
		parts[i] = filepath.Join(root, dir)
	}
	return strings.Join(parts, ":") + ":/usr/bin:/bin"
}

func writeTool(t *testing.T, root, dir, name, version string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho '" + name + " " + version + "'\n"
	if err := os.WriteFile(filepath.Join(full, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func lookupVersion(t *testing.T, path, shellCmd string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", shellCmd)
	cmd.Env = []string{"PATH=" + path}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", shellCmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

// #5459: an older executable in the persistent home must not shadow the
// version a crew declares through mise, for both a fresh config and a crew
// whose stored config still carries Crewship's pre-#5459 PATH literal. A tool
// mise does not declare still resolves from the home bin.
func TestDeclaredMisePinPrecedesPersistentHomeBinary(t *testing.T) {
	for _, tc := range []struct{ name, configured string }{
		{"no configured PATH", ""},
		{"stored pre-#5459 default literal", legacyDefaultContainerPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTool(t, root, agentLocalBinDir, "glab", "0.0.1-persistent-home")
			writeTool(t, root, MiseShimsDir, "glab", "1.131.0-declared")
			writeTool(t, root, agentLocalBinDir, "only-home", "agent-installed")

			env := ensureAgentToolPath(map[string]string{"PATH": tc.configured})
			built := expandEnvRefs(env["PATH"], map[string]string{"PATH": "/usr/bin:/bin"})
			// Runtime creation completes the image's PATH the same way.
			runtime := CreationToolPath(built)
			if runtime != NativeToolPath(runtime) {
				t.Fatalf("created runtime PATH is not a terminal fixed point: %s", runtime)
			}
			path := rootedPath(root, runtime)

			if got := lookupVersion(t, path, "glab"); got != "glab 1.131.0-declared" {
				t.Fatalf("declared pin shadowed: %q (PATH %s)", got, runtime)
			}
			if got := lookupVersion(t, path, "sh -c glab"); got != "glab 1.131.0-declared" {
				t.Fatalf("child process selected %q (PATH %s)", got, runtime)
			}
			if got := lookupVersion(t, path, "only-home"); got != "only-home agent-installed" {
				t.Fatalf("undeclared home tool lost: %q", got)
			}
		})
	}
}

// Only the exact sequence Crewship generated is migrated; an operator who
// deliberately orders the home bin first, or interleaves other entries, keeps
// that order.
func TestCreationToolPathKeepsOperatorOrder(t *testing.T) {
	for _, operator := range []string{
		"/home/agent/.local/bin:/usr/bin:/opt/mise/data/shims",
		"/home/agent/.local/bin:/opt/mise/data/shims:/opt/crewship/bin:/usr/bin",
	} {
		got := CreationToolPath(operator)
		if !strings.HasSuffix(got, operator) {
			t.Fatalf("operator order changed: %q -> %q", operator, got)
		}
	}
	if got := CreationToolPath("${containerEnv:PATH}:" + legacyDefaultContainerPath); !strings.Contains(got, "${containerEnv:PATH}:/opt/crewship/bin:/opt/mise/data/shims:/home/agent/.local/bin:/usr/local/sbin") {
		t.Fatalf("migration did not keep references and tail: %s", got)
	}
}

// The POSIX login hook restores the same declared-first order after a system
// profile resets PATH.
func TestAgentToolProfileRestoresDeclaredPinFirst(t *testing.T) {
	script := "PATH=/usr/local/bin:/usr/bin:/bin\n" + AgentToolProfile() + "printf %s \"$PATH\""
	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := ":" + string(out) + ":"
	if strings.Index(got, ":"+MiseShimsDir+":") > strings.Index(got, ":"+agentLocalBinDir+":") {
		t.Fatalf("login hook puts persistent home before declared pins: %s", out)
	}
}
