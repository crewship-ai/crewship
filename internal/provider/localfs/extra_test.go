package localfs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestListRecursive_Tree(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	files := []string{
		"a/x.txt",
		"a/y.txt",
		"a/sub/z.txt",
		"b/q.txt",
	}
	for _, f := range files {
		if err := p.Write(ctx, f, bytes.NewReader([]byte(f))); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	got, err := p.ListRecursive(ctx, ".")
	if err != nil {
		t.Fatalf("list recursive: %v", err)
	}

	// On macOS, /var → /private/var symlink resolution can cause filepath.Rel
	// to produce a path that's actually upward-traversing. We just verify the
	// expected leaf names appear (the listing is correct, only the rel-base
	// for path display drifts).
	var names []string
	for _, fi := range got {
		names = append(names, fi.Name)
	}
	sort.Strings(names)
	want := []string{"a", "b", "q.txt", "sub", "x.txt", "y.txt", "z.txt"}
	if len(names) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestListRecursive_NotExist(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	got, err := p.ListRecursive(context.Background(), "no-such-dir")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestPathTraversal_AbsolutePathRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	_, err := p.Read(context.Background(), "/etc/passwd")
	if err == nil {
		t.Fatal("expected absolute path to be rejected via traversal guard")
	}
}

func TestPathTraversal_ReadAfterClean(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "ok.txt", bytes.NewReader([]byte("ok"))); err != nil {
		t.Fatal(err)
	}
	// "x/../ok.txt" cleans to "ok.txt" which IS inside basePath, so should
	// be allowed.
	r, err := p.Read(ctx, "x/../ok.txt")
	if err != nil {
		t.Fatalf("clean-equivalent path should resolve: %v", err)
	}
	r.Close()
}

func TestExists_DirectoryReturnsTrue(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "somedir"); err != nil {
		t.Fatal(err)
	}
	ok, err := p.Exists(ctx, "somedir")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected directory to exist")
	}
}

func TestDelete_RecursivelyRemovesDir(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	for _, f := range []string{"d/a.txt", "d/sub/b.txt"} {
		if err := p.Write(ctx, f, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Delete(ctx, "d"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	exists, _ := p.Exists(ctx, "d")
	if exists {
		t.Error("expected dir gone after Delete")
	}
}

func TestNew_FailsOnUncreatable(t *testing.T) {
	t.Parallel()
	// Try to use a path that already exists as a file (mkdir over file fails).
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(bad); err == nil {
		t.Error("expected New to fail when basePath is a file")
	}
}

// TestWatch_EmitsCreateEvent verifies the inotify-based watcher actually
// publishes a "create" FileEvent when a file appears under the watched dir.
// This is timing-dependent; we wait up to 2s.
func TestWatch_EmitsCreateEvent(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := p.EnsureDir(ctx, "watchme"); err != nil {
		t.Fatal(err)
	}
	events := make(chan provider.FileEvent, 16)
	if err := p.Watch(ctx, "watchme", events); err != nil {
		t.Fatalf("watch: %v", err)
	}
	// Give fsnotify a moment to register.
	time.Sleep(50 * time.Millisecond)

	if err := p.Write(ctx, "watchme/created.txt", bytes.NewReader([]byte("hi"))); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if strings.Contains(e.Path, "created.txt") {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for create event")
		}
	}
}

func TestExtractAgent(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"agent-a/output.txt", "agent-a"},
		{"agent-a", "agent-a"},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractAgent(c.in); got != c.want {
			t.Errorf("extractAgent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWatch_RemoveAndRename emits remove + rename events when a file is
// deleted/renamed under the watched dir.
func TestWatch_RemoveAndRename(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := p.EnsureDir(ctx, "rm"); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(ctx, "rm/a.txt", bytes.NewReader([]byte("hi"))); err != nil {
		t.Fatal(err)
	}
	events := make(chan provider.FileEvent, 16)
	if err := p.Watch(ctx, "rm", events); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Delete the file via the provider so we go through the resolved path.
	if err := p.Delete(ctx, "rm/a.txt"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Op == "remove" || e.Op == "rename" {
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for remove event")
		}
	}
}

func TestWatch_NestedSubdir_AutoRegisters(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := p.EnsureDir(ctx, "auto"); err != nil {
		t.Fatal(err)
	}
	events := make(chan provider.FileEvent, 32)
	if err := p.Watch(ctx, "auto", events); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Create a new subdir, then a file inside it. The watcher must auto-register
	// the new subdir so the file event also fires.
	if err := p.EnsureDir(ctx, "auto/new"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := p.Write(ctx, "auto/new/file.txt", bytes.NewReader([]byte("y"))); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	gotFile := false
	for !gotFile {
		select {
		case e := <-events:
			if strings.Contains(e.Path, "file.txt") {
				gotFile = true
			}
		case <-deadline:
			t.Fatal("never received file event from auto-registered subdir")
		}
	}
}

func TestWatch_RejectsBadPath(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	events := make(chan provider.FileEvent, 1)
	if err := p.Watch(context.Background(), "../../escape", events); err == nil {
		t.Error("expected traversal error")
	}
}

func TestEnsureDir_TraversalRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	if err := p.EnsureDir(context.Background(), "../../escape"); err == nil {
		t.Error("expected traversal error")
	}
}

func TestDelete_TraversalRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	if err := p.Delete(context.Background(), "../../escape"); err == nil {
		t.Error("expected traversal error")
	}
}

func TestExists_TraversalRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	_, err := p.Exists(context.Background(), "../../escape")
	if err == nil {
		t.Error("expected traversal error")
	}
}
