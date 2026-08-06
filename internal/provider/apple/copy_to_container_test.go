package apple

import (
	"archive/tar"
	"bytes"
	"context"
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
