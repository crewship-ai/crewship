//go:build !windows

package devcontainer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// debianSkelProfile is the relevant tail of Debian bookworm /etc/skel/.profile
// (Ubuntu ships the same lines).
const debianSkelProfile = `# ~/.profile: executed by the command interpreter for login shells.
if [ -n "$BASH_VERSION" ]; then
    if [ -f "$HOME/.bashrc" ]; then
	. "$HOME/.bashrc"
    fi
fi

# set PATH so it includes user's private bin if it exists
if [ -d "$HOME/bin" ] ; then
    PATH="$HOME/bin:$PATH"
fi

# set PATH so it includes user's private bin if it exists
if [ -d "$HOME/.local/bin" ] ; then
    PATH="$HOME/.local/bin:$PATH"
fi
`

func runGuard(t *testing.T, files ...string) {
	t.Helper()
	if out, err := exec.Command("/bin/sh", "-c", HomeProfileGuardScript(files...)).CombinedOutput(); err != nil {
		t.Fatalf("guard script: %v: %s", err, out)
	}
}

// #5543: a login shell runs the seeded ~/.profile last. After the guard, the
// order the login hook restored survives it, so a stale ~/.local/bin binary
// cannot shadow the declared pin; a PATH without the dir still gains it.
func TestHomeProfileGuardKeepsDeclaredPinsFirst(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	profile := filepath.Join(home, ".profile")
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte(debianSkelProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	runGuard(t, profile, filepath.Join(root, "missing", ".profile"))

	login := func(path string) string {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", `. "$HOME/.profile"; printf %s "$PATH"`)
		cmd.Env = []string{"HOME=" + home, "PATH=" + path}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	hookOrder := "/opt/crewship/bin:/opt/mise/data/shims:" + home + "/.local/bin:/usr/bin:/bin"
	if got := login(hookOrder); got != hookOrder {
		t.Fatalf("guarded profile reordered PATH:\n got  %s\n want %s", got, hookOrder)
	}
	if got := login("/usr/bin:/bin"); got != home+"/.local/bin:/usr/bin:/bin" {
		t.Fatalf("guarded profile no longer adds a missing home bin: %s", got)
	}
	data, _ := os.ReadFile(profile)
	if strings.Count(string(data), "Crewship: keep declared mise pins first") != 1 {
		t.Fatalf("guard line not written exactly once:\n%s", data)
	}
	// Idempotent: a second provisioning pass changes nothing.
	runGuard(t, profile)
	again, _ := os.ReadFile(profile)
	if string(again) != string(data) {
		t.Fatal("second guard pass changed the profile")
	}
	// Only that line changed.
	if want := strings.Replace(debianSkelProfile, `    PATH="$HOME/.local/bin:$PATH"`, "    "+homeProfileGuardLine, 1); string(data) != want {
		t.Fatalf("guard touched other lines:\n%s", data)
	}
}

// Any other profile shape, including an operator's own deliberate prepend, is
// left byte-identical.
func TestHomeProfileGuardLeavesOtherProfilesUntouched(t *testing.T) {
	for _, content := range []string{
		"export PATH=\"$HOME/.local/bin:$PATH\"\n",
		"PATH=\"$HOME/.local/bin:$PATH\" # mine\n",
		"PATH=$HOME/.local/bin:$PATH\n",
		"# empty\n",
	} {
		file := filepath.Join(t.TempDir(), ".profile")
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGuard(t, file)
		if got, _ := os.ReadFile(file); string(got) != content {
			t.Fatalf("unmatched profile changed: %q -> %q", content, got)
		}
	}
}

func TestHomeProfileGuardTargetsImageSeedsOnly(t *testing.T) {
	want := []string{"/etc/skel/.profile", "/home/agent/.profile"}
	if strings.Join(HomeProfileSeeds, ",") != strings.Join(want, ",") {
		t.Fatalf("seed files %v, want %v", HomeProfileSeeds, want)
	}
}

// The provisioning command stays valid shell, and a failure before the guard
// still fails the step (the guard's own exit status must not mask it).
func TestWriteAggregatedContainerEnvRunsGuardAfterHook(t *testing.T) {
	var got []string
	capture := func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		got = cmd
		return "", 0, nil
	}
	if err := (&Provisioner{}).writeAggregatedContainerEnv(context.Background(), "cid", map[string]string{"PATH": "/usr/bin"}, capture); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "bash" {
		t.Fatalf("unexpected command %q", got)
	}
	script := got[2]
	if out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("provisioning script is not valid shell: %v: %s", err, out)
	}
	hook := strings.Index(script, "zz-crewship-tools.sh")
	guard := strings.Index(script, "/etc/skel/.profile")
	if hook < 0 || guard < hook || !strings.Contains(script, "chmod 0644 /etc/profile.d/zz-crewship-tools.sh && {") {
		t.Fatalf("guard must run only after the hook was written:\n%s", script)
	}
	// Replacing the hook write with a failing command must fail the script.
	failing := strings.Replace(script, "mkdir -p /etc/profile.d", "false", 1)
	failing = strings.Replace(failing, "printf '%s' ", "true ", 1)
	if err := exec.Command("bash", "-c", "cd \"$(mktemp -d)\" && "+strings.SplitN(failing, ">> /etc/environment", 2)[1][len(" && "):]).Run(); err == nil {
		t.Fatal("a failed hook write was masked by the guard")
	}
}
