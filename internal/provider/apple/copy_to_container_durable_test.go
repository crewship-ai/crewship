package apple

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// TestCopyToContainer_BindMountFileIsNeverObservableTorn is the #2124
// regression test for the bind-mount branch of CopyToContainer, in the
// shape #1999 used for progress.jsonl and #1807 for pins.md.
//
// That branch used to be a plain os.WriteFile onto the host side of the
// mount. os.WriteFile opens O_TRUNC and then writes, so a file that
// already existed — the re-provision case, where the orchestrator rewrites
// /crew/agents/<slug>/.mcp.json for a container that is already up — is
// empty between those two syscalls. The reader is not this process: it is
// the agent CLI inside the container, which opens the config through the
// mount and treats what it finds as authoritative. An empty .mcp.json is
// "no MCP servers", not an error, so the agent starts without its tools
// and nothing reports why.
//
// The invariant the durable helper buys and os.WriteFile cannot: the file
// becomes visible only through an atomic rename, so if the path exists it
// holds either the whole previous content or the whole new content, never
// zero bytes and never a prefix.
//
// Verified to FAIL against the pre-fix os.WriteFile implementation.
func TestCopyToContainer_BindMountFileIsNeverObservableTorn(t *testing.T) {
	installFakeContainer(t, `exit 0`) // any cp call would be a bug here
	hostCrewDir := t.TempDir()

	p := newTestProvider(Config{})
	p.rememberBindMounts("crew-container", map[string]string{"/crew": hostCrewDir})

	const (
		iterations = 60
		oldBody    = `{"mcpServers":{"memory":{"url":"http://old"}}}`
		newBody    = `{"mcpServers":{"memory":{"url":"http://new"},"linear":{"url":"http://linear"}}}`
	)

	tornObservations := 0
	for i := range iterations {
		// A fresh agent dir per iteration, seeded with the previous config,
		// so every round exercises the overwrite window rather than a
		// create-then-write on a path nobody is watching yet.
		agentDir := filepath.Join(hostCrewDir, "agents", "casey-"+strconv.Itoa(i))
		if err := os.MkdirAll(agentDir, 0o750); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(agentDir, ".mcp.json")
		if err := os.WriteFile(path, []byte(oldBody), 0o600); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Open-and-read is exactly what the agent inside the
				// container does. Whatever it sees must be one of the two
				// complete documents.
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if s := string(data); s != oldBody && s != newBody {
					tornObservations++
					return
				}
			}
		}()

		err := p.CopyToContainer(context.Background(), "crew-container", "/crew/agents/casey-"+strconv.Itoa(i),
			tarOf(t, map[string]string{".mcp.json": newBody}))
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatalf("CopyToContainer: %v", err)
		}
	}

	if tornObservations != 0 {
		t.Errorf(".mcp.json was observed torn (empty or partial) in %d/%d rewrites — "+
			"the O_TRUNC window is back; the bind-mount branch of CopyToContainer must "+
			"publish via memory.WriteFileDurable's atomic rename",
			tornObservations, iterations)
	}
}

// TestCopyToContainer_BindMountLeavesNoTempFiles guards the durable helper's
// cleanup through this call site: a stray .mcp.json.tmp.* beside the real
// config would be a second copy of a file that can carry credential
// references, and the agent's own directory listing would show it.
func TestCopyToContainer_BindMountLeavesNoTempFiles(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	hostCrewDir := t.TempDir()

	p := newTestProvider(Config{})
	p.rememberBindMounts("crew-container", map[string]string{"/crew": hostCrewDir})

	for range 3 {
		if err := p.CopyToContainer(context.Background(), "crew-container", "/crew/agents/casey",
			tarOf(t, map[string]string{".mcp.json": `{"mcpServers":{}}`})); err != nil {
			t.Fatalf("CopyToContainer: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(hostCrewDir, "agents", "casey"))
	if err != nil {
		t.Fatalf("read agent dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".mcp.json" {
			t.Errorf("unexpected leftover file %q beside .mcp.json", e.Name())
		}
	}
}
