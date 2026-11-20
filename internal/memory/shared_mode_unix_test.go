//go:build unix

package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Regression for #4777 (and the writer half of #1623): the agent (1001),
// the sidecar (1002) and the host write into one `.memory` tree, and the
// only thing that keeps them from locking each other out is group 1002 +
// group write (ownership.go). The writers used to create lockfiles 0600,
// files 0644 and subdirectories 0755, and with the service units' 0022
// umask nothing put group write back until the next container start ran
// MemoryOwnershipCmd. Two uids cannot be simulated without root, so the
// test asserts the modes the second writer depends on: a setgid tree
// (the marker of the shared contract) must get 0660 locks, group-writable
// files and 02775 directories from the writer itself.
//
// Not parallel: umask is process-wide.
func TestSharedTreeWritersAreGroupWritable(t *testing.T) {
	old := syscallUmask(0o022)
	t.Cleanup(func() { syscallUmask(old) })

	type want struct {
		rel  string
		perm os.FileMode // permission + setgid bits only
	}
	cases := []struct {
		name  string
		umask int // 0 means the service default, 0o022
		write func(t *testing.T, mem string)
		want  []want
	}{
		{
			name: "fresh lockfile is 0660",
			write: func(t *testing.T, mem string) {
				lockAndRelease(t, NewFileLock(filepath.Join(mem, "AGENT.md.lock")))
			},
			want: []want{{"AGENT.md.lock", 0o660}},
		},
		{
			name: "own legacy 0600 lockfile is repaired to 0660",
			write: func(t *testing.T, mem string) {
				p := filepath.Join(mem, "AGENT.md.lock")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				lockAndRelease(t, NewFileLock(p))
			},
			want: []want{{"AGENT.md.lock", 0o660}},
		},
		{
			name: "root-anchored lockfile is 0660",
			write: func(t *testing.T, mem string) {
				root, err := os.OpenRoot(mem)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				lk := NewRootFileLock(root, "x.md.lock")
				if err := lk.Lock(); err != nil {
					t.Fatal(err)
				}
				_ = lk.Unlock()
			},
			want: []want{{"x.md.lock", 0o660}},
		},
		{
			name: "durable write 0644 becomes 0664",
			write: func(t *testing.T, mem string) {
				if err := WriteFileDurable(filepath.Join(mem, "AGENT.md"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"AGENT.md", 0o664}},
		},
		{
			name: "private durable write stays 0600",
			write: func(t *testing.T, mem string) {
				if err := WriteFileDurable(filepath.Join(mem, "q"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"q", 0o600}},
		},
		{
			name: "rooted durable write 0644 becomes 0664",
			write: func(t *testing.T, mem string) {
				root, err := os.OpenRoot(mem)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if err := WriteFileDurableRoot(root, "r.md", []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"r.md", 0o664}},
		},
		{
			name: "WriteFile creates group-writable dirs, file and lock",
			write: func(t *testing.T, mem string) {
				if _, err := WriteFile(context.Background(), filepath.Join(mem, "daily", "2026", "d.md"), []byte("x"), WriteConfig{}); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{
				{"daily", os.ModeSetgid | 0o775},
				{"daily/2026", os.ModeSetgid | 0o775},
				{"daily/2026/d.md", 0o664},
				{"daily/2026/d.md.lock", 0o660},
			},
		},
		{
			name: "mutation file rewrite is group-writable",
			write: func(t *testing.T, mem string) {
				f, err := openMutationFile("", filepath.Join(mem, "notes", "n.md"), true)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"notes", os.ModeSetgid | 0o775}, {"notes/n.md", 0o664}},
		},
		{
			name: "rooted mutation file creates 02775 parents",
			write: func(t *testing.T, mem string) {
				f, err := openMutationFile(mem, filepath.Join(mem, "m", "n.md"), true)
				if err != nil {
					t.Fatal(err)
				}
				defer f.close()
				if err := f.write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"m", os.ModeSetgid | 0o775}, {"m/n.md", 0o664}},
		},
		{
			name: "user model creates 02775 users dir",
			write: func(t *testing.T, mem string) {
				if err := WriteUserModel(UserModelPaths{SharedDir: mem}, "u1", "w1", "body"); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"users", os.ModeSetgid | 0o775}},
		},
		{
			name: "peer card creates 02775 peers dir",
			write: func(t *testing.T, mem string) {
				if err := WritePeerCard(PeerPaths{AgentDir: mem}, "u1", "w1", "body"); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"peers", os.ModeSetgid | 0o775}},
		},
		{
			// A hardened umask keeps its say over read access: group write
			// is only added where group read already is (review F1).
			name:  "umask 077 durable write stays 0600",
			umask: 0o077,
			write: func(t *testing.T, mem string) {
				if err := WriteFileDurable(filepath.Join(mem, "a"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"a", 0o600}},
		},
		{
			name:  "umask 077 rooted durable write stays 0600",
			umask: 0o077,
			write: func(t *testing.T, mem string) {
				root, err := os.OpenRoot(mem)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if err := WriteFileDurableRoot(root, "r", []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"r", 0o600}},
		},
		{
			name:  "umask 077 WriteFile keeps dirs and file private",
			umask: 0o077,
			write: func(t *testing.T, mem string) {
				if _, err := WriteFile(context.Background(), filepath.Join(mem, "d", "f.md"), []byte("x"), WriteConfig{}); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"d", os.ModeSetgid | 0o700}, {"d/f.md", 0o600}},
		},
		{
			name:  "umask 077 MkdirAllShared keeps 2700",
			umask: 0o077,
			write: func(t *testing.T, mem string) {
				if err := MkdirAllShared(filepath.Join(mem, "p")); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"p", os.ModeSetgid | 0o700}},
		},
		{
			name:  "umask 027 durable write gains group write, not other read",
			umask: 0o027,
			write: func(t *testing.T, mem string) {
				if err := WriteFileDurable(filepath.Join(mem, "g"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"g", 0o660}},
		},
		{
			// An existing level (for example one another uid just created)
			// is left alone; only levels this call creates are changed
			// (review F2).
			name: "MkdirAllShared leaves an existing level alone",
			write: func(t *testing.T, mem string) {
				if err := os.Mkdir(filepath.Join(mem, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(mem, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := MkdirAllShared(filepath.Join(mem, "x", "y")); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"x", 0o755}},
		},
		{
			name: "MkdirAllShared creates 02775 at every level",
			write: func(t *testing.T, mem string) {
				if err := MkdirAllShared(filepath.Join(mem, "a", "b")); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"a", os.ModeSetgid | 0o775}, {"a/b", os.ModeSetgid | 0o775}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := newSharedTree(t)
			if tc.umask != 0 {
				prev := syscallUmask(tc.umask)
				defer syscallUmask(prev)
			}
			tc.write(t, mem)
			for _, w := range tc.want {
				fi, err := os.Lstat(filepath.Join(mem, w.rel))
				if err != nil {
					t.Fatalf("%s: %v", w.rel, err)
				}
				got := fi.Mode() & (os.ModePerm | os.ModeSetgid)
				if got != w.perm {
					t.Errorf("%s: mode %v, want %v", w.rel, got, w.perm)
				}
			}
		})
	}
}

// Outside a setgid tree nothing changes: a host-only directory keeps the
// private 0600 lock and the umask-shaped file mode it always had.
func TestNonSharedTreeWritersKeepPrivateModes(t *testing.T) {
	old := syscallUmask(0o002)
	t.Cleanup(func() { syscallUmask(old) })

	dir := t.TempDir()
	lockAndRelease(t, NewFileLock(filepath.Join(dir, "f.lock")))
	if err := WriteFileDurable(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAllShared(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]os.FileMode{
		"f.lock": 0o600,
		"f":      0o644,
		"sub":    0o755,
	} {
		fi, err := os.Lstat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode() & (os.ModePerm | os.ModeSetgid); got != want {
			t.Errorf("%s: mode %v, want %v", rel, got, want)
		}
	}
}

func TestSharedLockDoesNotWidenModeOfNonemptyFile(t *testing.T) {
	path := filepath.Join(newSharedTree(t), "data.lock")
	if err := os.WriteFile(path, []byte("sample data"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockAndRelease(t, NewFileLock(path))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("nonempty file mode = %o, want 600", got)
	}
}

func newSharedTree(t *testing.T) string {
	t.Helper()
	mem := filepath.Join(t.TempDir(), ".memory")
	if err := os.Mkdir(mem, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mem, os.ModeSetgid|0o775); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(mem)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("setgid did not stick on %s (mode %v); the shared-tree tests need it", mem, fi.Mode())
	}
	return mem
}

func lockAndRelease(t *testing.T, lk *FileLock) {
	t.Helper()
	if err := lk.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := lk.Unlock(); err != nil {
		t.Fatal(err)
	}
}
