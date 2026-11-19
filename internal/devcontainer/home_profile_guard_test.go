//go:build linux

// The guard only ever runs inside a Linux build container (GNU or busybox
// text tools, both exercised here). BSD sed/grep on a macOS host leave the
// profile unchanged, which the guard's best-effort contract tolerates, so
// these executing tests are built for Linux only (#5543).

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
	before, err := os.Stat(profile)
	if err != nil {
		t.Fatal(err)
	}
	runGuard(t, profile, filepath.Join(root, "missing", ".profile"))
	after, err := os.Stat(profile)
	if err != nil {
		t.Fatal(err)
	}
	// Rewritten in place: same inode (so owner) and mode, no temp file left.
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("profile replaced instead of rewritten in place: %v -> %v", before.Mode(), after.Mode())
	}
	if _, err := os.Stat(profile + ".crewship-guard"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

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
	// The guard is joined with && to the hook write, so a failed write skips it
	// and fails the step. Rebuild that shape with a failing stand-in and the
	// real guard pointed at a temp profile; never run the real script.
	const join = "chmod 0644 /etc/profile.d/zz-crewship-tools.sh && {\n"
	tail := script[strings.Index(script, join)+len(join):]
	if tail != HomeProfileGuardScript(HomeProfileSeeds...)+"}" {
		t.Fatalf("guard block is not exactly the seed guard:\n%s", tail)
	}
	profile := filepath.Join(t.TempDir(), ".profile")
	if err := os.WriteFile(profile, []byte(debianSkelProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("bash", "-c", "false && {\n"+HomeProfileGuardScript(profile)+"}").Run(); err == nil {
		t.Fatal("a failed hook write was masked by the guard")
	}
	if got, _ := os.ReadFile(profile); string(got) != debianSkelProfile {
		t.Fatal("guard ran although the hook write failed")
	}
	// A symlinked profile is left alone rather than replaced by a copy.
	link := filepath.Join(t.TempDir(), ".profile")
	if err := os.Symlink(profile, link); err != nil {
		t.Fatal(err)
	}
	runGuard(t, link)
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlinked profile replaced: %v", err)
	}
}
