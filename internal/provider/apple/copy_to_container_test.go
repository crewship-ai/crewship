package apple

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CopyToContainer was an outright stub on this provider, and that is what kept
// agents from running on macOS even once the container started: the
// orchestrator writes /crew/agents/<slug>/.mcp.json through it, the write
// failed, and claude-code exited 1 with "MCP config file not found" (#1779).
//
// The contract hands over a TAR archive for a destination directory, which is
// Docker's API shape. Apple's CLI has no tar input — `container cp` moves paths
// — so the archive is unpacked on the host first and its entries copied in.
func tarOf(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestCopyToContainer_CopiesEveryEntryIntoTheDestination(t *testing.T) {
	fake := installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	err := p.CopyToContainer(context.Background(), "crew-container", "/crew/agents/casey",
		tarOf(t, map[string]string{".mcp.json": `{"mcpServers":{}}`}))
	if err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}

	calls := strings.Join(fake.calls(t), "\n")
	if !strings.Contains(calls, "cp ") {
		t.Errorf("expected a cp invocation, got:\n%s", calls)
	}
	if !strings.Contains(calls, "crew-container:/crew/agents/casey") {
		t.Errorf("destination must be container:path, got:\n%s", calls)
	}
	if !strings.Contains(calls, ".mcp.json") {
		t.Errorf("the archived file must be copied, got:\n%s", calls)
	}
}

// A tar entry naming ../ or an absolute path must not be able to write outside
// the staging directory — the archive comes from config the operator supplies.
func TestCopyToContainer_RefusesPathTraversal(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	err := p.CopyToContainer(context.Background(), "c", "/crew",
		tarOf(t, map[string]string{"../escape.json": "x"}))
	if err == nil {
		t.Fatal("a traversing archive entry must be refused")
	}
	if _, statErr := os.Stat("/tmp/escape.json"); statErr == nil {
		t.Error("the entry escaped the staging directory")
	}
}

func TestCopyToContainer_SurfacesCLIFailure(t *testing.T) {
	installFakeContainer(t, `echo "boom" >&2; exit 1`)
	p := newTestProvider(Config{})

	err := p.CopyToContainer(context.Background(), "c", "/crew",
		tarOf(t, map[string]string{"a.json": "{}"}))
	if err == nil {
		t.Fatal("a failing cp must surface as an error")
	}
}

// `container cp` is a silent no-op into a bind-mounted path: it exits 0, echoes
// the destination, and copies nothing. Verified by hand against 1.2.0 —
// `container cp /tmp/probe.json <crew>:/crew/agents/casey/.mcp.json` reported
// success and the file existed in neither the container nor the host directory
// backing the mount.
//
// /crew, /workspace and /output are bind mounts this provider creates itself,
// so the host path is known and writing there is both simpler and verifiable.
// The CLI is only for destinations that are not mounted.
func TestCopyToContainer_WritesThroughTheBindMount(t *testing.T) {
	installFakeContainer(t, `exit 0`) // any cp call would be a bug here
	hostCrewDir := t.TempDir()

	p := newTestProvider(Config{})
	p.rememberBindMounts("crew-container", map[string]string{"/crew": hostCrewDir})

	err := p.CopyToContainer(context.Background(), "crew-container", "/crew/agents/casey",
		tarOf(t, map[string]string{".mcp.json": `{"mcpServers":{"memory":{}}}`}))
	if err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}

	got, readErr := os.ReadFile(filepath.Join(hostCrewDir, "agents", "casey", ".mcp.json"))
	if readErr != nil {
		t.Fatalf("file did not land on the host side of the mount: %v", readErr)
	}
	if !strings.Contains(string(got), "mcpServers") {
		t.Errorf("content = %q", got)
	}
}

// A destination outside every known mount still has to go somewhere, so the
// CLI remains the fallback rather than an error.
func TestCopyToContainer_FallsBackToTheCLIOffMount(t *testing.T) {
	fake := installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})
	p.rememberBindMounts("c", map[string]string{"/crew": t.TempDir()})

	if err := p.CopyToContainer(context.Background(), "c", "/etc/somewhere",
		tarOf(t, map[string]string{"a.json": "{}"})); err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}
	if !strings.Contains(strings.Join(fake.calls(t), "\n"), "cp ") {
		t.Error("an unmounted destination should still be attempted through the CLI")
	}
}

// TestCopyToContainer_KeepsThePathUsableWhenChownFails covers the failure this
// method exists to prevent, reintroduced one layer down.
//
// The file is written 0600 and the directories 0750, both owned by whoever runs
// the server; the agent in the container is uid 1001. Handing that ownership
// over needs root, which the server does not have on a normal macOS install, so
// the chown fails — and the copy used to return nil anyway. The agent then gets
// EACCES and reports the config as missing, which is exactly the "MCP config
// file not found" symptom this path was written to fix, with no error to act on.
//
// The chown is injected rather than inferred from the euid. Deriving it made the
// coverage a property of the CI image: as root the chown succeeds and the
// fallback is never exercised at all.
func TestCopyToContainer_KeepsThePathUsableWhenChownFails(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	hostCrewDir := t.TempDir()

	p := newTestProvider(Config{})
	p.chownFn = func(string, int, int) error { return errors.New("operation not permitted") }
	p.rememberBindMounts("crew-container", map[string]string{"/crew": hostCrewDir})

	if err := p.CopyToContainer(context.Background(), "crew-container", "/crew/agents/casey",
		tarOf(t, map[string]string{".mcp.json": `{"mcpServers":{"memory":{}}}`})); err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}

	dst := filepath.Join(hostCrewDir, "agents", "casey", ".mcp.json")
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if st.Mode().Perm()&0o044 == 0 {
		t.Errorf("file mode = %#o — only the owner can read it, and the owner is not the agent", st.Mode().Perm())
	}

	// Reading the file is not enough: the agent has to be able to REACH it.
	// Every directory this copy created needs the execute bit, or the open
	// fails with EACCES on the traversal and the copy still reports success.
	for _, dir := range []string{
		filepath.Join(hostCrewDir, "agents"),
		filepath.Join(hostCrewDir, "agents", "casey"),
	} {
		dst, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if dst.Mode().Perm()&0o005 == 0 {
			t.Errorf("directory %s mode = %#o — not traversable by the agent, so the file under it is unreachable",
				dir, dst.Mode().Perm())
		}
	}
}

// A directory that already existed belongs to whoever made it; the copy must
// not quietly relax its mode.
func TestCopyToContainer_LeavesAnExistingDirectoryAlone(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	hostCrewDir := t.TempDir()
	preexisting := filepath.Join(hostCrewDir, "agents")
	if err := os.MkdirAll(preexisting, 0o700); err != nil {
		t.Fatal(err)
	}

	p := newTestProvider(Config{})
	p.chownFn = func(string, int, int) error { return errors.New("operation not permitted") }
	p.rememberBindMounts("crew-container", map[string]string{"/crew": hostCrewDir})

	if err := p.CopyToContainer(context.Background(), "crew-container", "/crew/agents/casey",
		tarOf(t, map[string]string{".mcp.json": "{}"})); err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}

	st, err := os.Stat(preexisting)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("pre-existing directory mode = %#o; want it left at 0700", st.Mode().Perm())
	}
}
