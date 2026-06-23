package localfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/fsnotify/fsnotify"
)

// --- resolve ---

// A symlink inside the base that points outside must be rejected by the
// symlink-aware containment re-check (V-09).
func TestResolve_SymlinkEscapeRejected(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	p := tempProvider(t)

	link := filepath.Join(p.basePath, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := p.Read(context.Background(), "evil"); err == nil {
		t.Fatal("expected symlink-escape to be rejected")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error %q should mention symlink traversal", err)
	}
}

// A symlink that stays inside the base resolves to the real target and reads fine.
func TestResolve_InternalSymlinkAllowed(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "real.txt", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.basePath, "real.txt"), filepath.Join(p.basePath, "alias")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	r, err := p.Read(ctx, "alias")
	if err != nil {
		t.Fatalf("internal symlink should resolve: %v", err)
	}
	r.Close()
}

// When the base path itself can no longer be resolved (deleted out from
// under the provider), resolve must surface the EvalSymlinks error.
func TestResolve_BasePathGoneErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	p, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), "x.txt"); err == nil {
		t.Fatal("expected resolve error after base dir removal")
	} else if !strings.Contains(err.Error(), "resolve base path") {
		t.Errorf("error %q should mention base path resolution", err)
	}
}

// --- Write ---

// Write must refuse a path whose existing target is a symlink (TOCTOU guard).
func TestWrite_RefusesSymlinkTarget(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "victim.txt", bytes.NewReader([]byte("v"))); err != nil {
		t.Fatal(err)
	}
	// Relative symlink target so resolve()'s EvalSymlinks still lands inside
	// the base (the in-base guard is what we want to exercise here).
	if err := os.Symlink("victim.txt", filepath.Join(p.basePath, "link.txt")); err != nil {
		t.Fatal(err)
	}

	// resolve() follows the symlink (still in-base), so to reach the Lstat
	// guard we exercise the path through a freshly-swapped symlink whose
	// target does not exist — EvalSymlinks fails, resolve returns the raw
	// path, and Write's Lstat sees the symlink.
	if err := os.Symlink("missing-target.txt", filepath.Join(p.basePath, "dangling.txt")); err != nil {
		t.Fatal(err)
	}
	err := p.Write(ctx, "dangling.txt", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected Write to refuse symlink target")
	}
	if !strings.Contains(err.Error(), "refuse symlink target") {
		t.Errorf("error %q should mention symlink refusal", err)
	}
}

// Writing to a path occupied by a directory is an unsupported file type.
func TestWrite_RefusesDirectoryTarget(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "adir"); err != nil {
		t.Fatal(err)
	}
	err := p.Write(ctx, "adir", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected Write to a directory path to fail")
	}
	if !strings.Contains(err.Error(), "unsupported file type") {
		t.Errorf("error %q should mention unsupported file type", err)
	}
}

// Overwriting an existing regular file goes through the best-effort chmod
// branch and still succeeds.
func TestWrite_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "f.txt", bytes.NewReader([]byte("one"))); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(ctx, "f.txt", bytes.NewReader([]byte("two"))); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	r, err := p.Read(ctx, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "two" {
		t.Errorf("content = %q, want two", buf.String())
	}
}

// A reader error mid-copy must be wrapped as a write error.
func TestWrite_CopyErrorPropagates(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	boom := errors.New("boom")
	err := p.Write(context.Background(), "broken.txt", iotest.ErrReader(boom))
	if err == nil {
		t.Fatal("expected copy error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v should wrap the reader error", err)
	}
	if !strings.Contains(err.Error(), "write broken.txt") {
		t.Errorf("error %q should carry the logical path", err)
	}
}

// Create failing with EACCES on a read-only parent dir exercises the
// permission-retry path; Remove also fails, so the create error surfaces.
func TestWrite_PermissionDeniedSurfacesCreateError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "ro"); err != nil {
		t.Fatal(err)
	}
	roDir := filepath.Join(p.basePath, "ro")
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	err := p.Write(ctx, "ro/file.txt", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected create failure in read-only dir")
	}
	if !strings.Contains(err.Error(), "create ro/file.txt") {
		t.Errorf("error %q should be a create error for the logical path", err)
	}
}

// Write with a traversal path must fail before touching the filesystem.
func TestWrite_TraversalRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	if err := p.Write(context.Background(), "../../escape.txt", bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected traversal error")
	}
}

// --- List / ListRecursive / Exists ---

func TestList_TraversalRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	if _, err := p.List(context.Background(), "../../escape"); err == nil {
		t.Fatal("expected traversal error")
	}
}

// Listing a path that is a regular file is a read-dir error (ENOTDIR), not
// the silent nil of a missing dir.
func TestList_RegularFileErrors(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "plain.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	_, err := p.List(ctx, "plain.txt")
	if err == nil {
		t.Fatal("expected error listing a regular file")
	}
	if !strings.Contains(err.Error(), "read dir plain.txt") {
		t.Errorf("error %q should be a read-dir error", err)
	}
}

func TestListRecursive_TraversalRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	if _, err := p.ListRecursive(context.Background(), "../../escape"); err == nil {
		t.Fatal("expected traversal error")
	}
}

// Exists with a file in the parent position returns a non-NotExist stat error.
func TestExists_StatErrorPropagates(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "leaf.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	ok, err := p.Exists(ctx, "leaf.txt/child")
	if err == nil {
		t.Fatal("expected ENOTDIR-style stat error")
	}
	if ok {
		t.Error("Exists must be false when stat errors")
	}
}

// --- Watch / toFileEvent / addRecursive ---

// toFileEvent classification, including ops not exercised by the
// integration watch tests (rename mapping, unknown op → nil).
func TestToFileEvent_OpMapping(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	base := p.basePath

	cases := []struct {
		name   string
		op     fsnotify.Op
		wantOp string
		isNil  bool
	}{
		{"create", fsnotify.Create, "create", false},
		{"write", fsnotify.Write, "write", false},
		{"remove", fsnotify.Remove, "remove", false},
		{"rename", fsnotify.Rename, "rename", false},
		{"chmod is ignored", fsnotify.Chmod, "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ev := fsnotify.Event{Name: filepath.Join(base, "agent-a", "out.txt"), Op: tc.op}
			fe := p.toFileEvent(ev, base)
			if tc.isNil {
				if fe != nil {
					t.Fatalf("expected nil event, got %+v", fe)
				}
				return
			}
			if fe == nil {
				t.Fatal("expected event, got nil")
			}
			if fe.Op != tc.wantOp {
				t.Errorf("op = %q, want %q", fe.Op, tc.wantOp)
			}
			if fe.Agent != "agent-a" {
				t.Errorf("agent = %q, want agent-a", fe.Agent)
			}
			if fe.Path != filepath.Join("agent-a", "out.txt") {
				t.Errorf("path = %q", fe.Path)
			}
		})
	}
}

// Size is populated from a stat of the (existing) target file.
func TestToFileEvent_SizeFromStat(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	full := filepath.Join(p.basePath, "sized.txt")
	if err := os.WriteFile(full, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	fe := p.toFileEvent(fsnotify.Event{Name: full, Op: fsnotify.Write}, p.basePath)
	if fe == nil {
		t.Fatal("expected event")
	}
	if fe.Size != 5 {
		t.Errorf("size = %d, want 5", fe.Size)
	}
}

// addRecursive registers every directory in the tree and skips files.
func TestAddRecursive_RegistersDirsOnly(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(ctx, "a/file.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := addRecursive(w, p.basePath); err != nil {
		t.Fatalf("addRecursive: %v", err)
	}
	got := w.WatchList()
	if len(got) != 3 { // base, a, a/b
		t.Errorf("watch list = %v, want 3 directories", got)
	}
	for _, watched := range got {
		if strings.HasSuffix(watched, "file.txt") {
			t.Errorf("file %q must not be watched directly", watched)
		}
	}
}

// Write must fail when a path component of the parent dir is a regular file.
func TestWrite_ParentDirCreationFails(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "blocker.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	err := p.Write(ctx, "blocker.txt/sub/file.txt", bytes.NewReader([]byte("y")))
	if err == nil {
		t.Fatal("expected parent-dir creation to fail under a regular file")
	}
	if !strings.Contains(err.Error(), "create parent dir") {
		t.Errorf("error %q should be a parent-dir error", err)
	}
}

// Watch surfaces addRecursive failures (unreadable subdir) instead of
// silently watching a partial tree.
func TestWatch_AddRecursiveErrorPropagates(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "w/locked"); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(p.basePath, "w", "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	events := make(chan provider.FileEvent, 1)
	err := p.Watch(ctx, "w", events)
	if err == nil {
		t.Fatal("expected watch error for unreadable subdir")
	}
	if !strings.Contains(err.Error(), "watch w") {
		t.Errorf("error %q should wrap the watch dir", err)
	}
}

// A non-relativizable event name yields no event.
func TestToFileEvent_RelErrorReturnsNil(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	fe := p.toFileEvent(fsnotify.Event{Name: "not-absolute.txt", Op: fsnotify.Create}, p.basePath)
	if fe != nil {
		t.Fatalf("expected nil event for non-relativizable name, got %+v", fe)
	}
}

// addRecursive on a missing root is a no-op (walk error swallowed by design).
func TestAddRecursive_MissingRootIsNoOp(t *testing.T) {
	t.Parallel()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := addRecursive(w, filepath.Join(t.TempDir(), "ghost")); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := w.WatchList(); len(got) != 0 {
		t.Errorf("watch list should be empty, got %v", got)
	}
}
