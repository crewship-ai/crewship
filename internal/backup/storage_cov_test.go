package backup

// Coverage tests for storage.go — Exists, cleanPath, the
// LocalStorageOps method set (success + sanitisation + OS error
// wrapping), and the default-storage plumbing.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExists_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("present file", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "bundle.tar.zst")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ok, err := Exists(ctx, p)
		if !ok || err != nil {
			t.Fatalf("Exists = (%v, %v), want (true, nil)", ok, err)
		}
	})
	t.Run("absent file maps to ErrBundleNotFound", func(t *testing.T) {
		ok, err := Exists(ctx, filepath.Join(t.TempDir(), "nope.tar.zst"))
		if ok || !errors.Is(err, ErrBundleNotFound) {
			t.Fatalf("Exists = (%v, %v), want (false, ErrBundleNotFound)", ok, err)
		}
	})
	t.Run("unsafe path is wrapped, not ErrBundleNotFound", func(t *testing.T) {
		ok, err := Exists(ctx, "bad\x00path")
		if ok || err == nil {
			t.Fatalf("Exists = (%v, %v), want (false, err)", ok, err)
		}
		if errors.Is(err, ErrBundleNotFound) {
			t.Fatalf("NUL path must not look like a missing bundle: %v", err)
		}
		if !errors.Is(err, ErrUnsafeBackupPath) {
			t.Fatalf("expected ErrUnsafeBackupPath in chain, got %v", err)
		}
	})
}

func TestCleanPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", "", true},
		{"nul byte", "a\x00b", "", true},
		{"leading parent ref", "../etc/shadow", "", true},
		{"bare parent ref", "..", "", true},
		{"interior dotdot collapses cleanly", "a/b/../c", "a/c", false},
		{"plain path", "/tmp/x", "/tmp/x", false},
		{"trailing slash cleaned", "/tmp/x/", "/tmp/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("cleanPath(%q) = %q, want error", tc.in, got)
				}
				if !errors.Is(err, ErrUnsafeBackupPath) {
					t.Errorf("error %v must wrap ErrUnsafeBackupPath", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanPath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("cleanPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLocalStorageOps_RejectsUnsafePaths drives every method through
// the cleanPath gate with a NUL-poisoned path so the sanitiser-first
// contract is pinned on each entry point.
func TestLocalStorageOps_RejectsUnsafePaths(t *testing.T) {
	ctx := context.Background()
	st := LocalStorageOps{}
	bad := "x\x00y"

	checks := []struct {
		name string
		call func() error
	}{
		{"MkdirAll", func() error { return st.MkdirAll(ctx, bad, 0o700) }},
		{"ReadDir", func() error { _, err := st.ReadDir(ctx, bad); return err }},
		{"Open", func() error { _, err := st.Open(ctx, bad); return err }},
		{"Create", func() error { _, err := st.Create(ctx, bad, 0o600); return err }},
		{"CreateTemp", func() error { _, err := st.CreateTemp(ctx, bad, "p-*"); return err }},
		{"MkdirTemp", func() error { _, err := st.MkdirTemp(ctx, bad, "p-*"); return err }},
		{"Remove", func() error { return st.Remove(ctx, bad) }},
		{"RemoveAll", func() error { return st.RemoveAll(ctx, bad) }},
		{"Rename old", func() error { return st.Rename(ctx, bad, "/tmp/ok") }},
		{"Rename new", func() error { return st.Rename(ctx, "/tmp/ok", bad) }},
		{"Stat", func() error { _, err := st.Stat(ctx, bad); return err }},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if !errors.Is(err, ErrUnsafeBackupPath) {
				t.Errorf("%s(%q) err = %v, want ErrUnsafeBackupPath", c.name, bad, err)
			}
		})
	}
}

// TestLocalStorageOps_OSErrorsAreWrapped pins the "operation + path"
// wrapping on the underlying os.* failures so logs carry both.
func TestLocalStorageOps_OSErrorsAreWrapped(t *testing.T) {
	ctx := context.Background()
	st := LocalStorageOps{}
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")

	t.Run("Open missing", func(t *testing.T) {
		_, err := st.Open(ctx, missing)
		if err == nil || !strings.Contains(err.Error(), "backup storage: open") || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("Create in missing dir", func(t *testing.T) {
		_, err := st.Create(ctx, filepath.Join(missing, "f"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "backup storage: create") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("CreateTemp in missing dir", func(t *testing.T) {
		_, err := st.CreateTemp(ctx, missing, "p-*")
		if err == nil || !strings.Contains(err.Error(), "backup storage: createtemp") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("MkdirTemp in missing dir", func(t *testing.T) {
		_, err := st.MkdirTemp(ctx, missing, "p-*")
		if err == nil || !strings.Contains(err.Error(), "backup storage: mkdirtemp") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("Remove missing", func(t *testing.T) {
		err := st.Remove(ctx, missing)
		if err == nil || !strings.Contains(err.Error(), "backup storage: remove") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("Rename missing", func(t *testing.T) {
		err := st.Rename(ctx, missing, missing+"2")
		if err == nil || !strings.Contains(err.Error(), "backup storage: rename") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("Stat missing", func(t *testing.T) {
		_, err := st.Stat(ctx, missing)
		if err == nil || !strings.Contains(err.Error(), "backup storage: stat") || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ReadDir missing", func(t *testing.T) {
		_, err := st.ReadDir(ctx, missing)
		if err == nil || !strings.Contains(err.Error(), "backup storage: readdir") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("RemoveAll blocked by read-only parent", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root bypasses permission checks")
		}
		root := t.TempDir()
		locked := filepath.Join(root, "locked")
		if err := os.MkdirAll(filepath.Join(locked, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "child", "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(locked, "child"), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "child"), 0o700) })
		err := st.RemoveAll(ctx, filepath.Join(locked, "child"))
		if err == nil || !strings.Contains(err.Error(), "backup storage: removeall") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("MkdirAll over a regular file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := st.MkdirAll(ctx, filepath.Join(f, "child"), 0o700)
		if err == nil || !strings.Contains(err.Error(), "backup storage: mkdirall") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLocalStorageOps_HappyPaths(t *testing.T) {
	ctx := context.Background()
	st := LocalStorageOps{}
	dir := t.TempDir()

	home, err := st.Home()
	if err != nil || home == "" {
		t.Fatalf("Home = (%q, %v)", home, err)
	}

	sub := filepath.Join(dir, "a", "b")
	if err := st.MkdirAll(ctx, sub, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	p := filepath.Join(sub, "f.txt")
	w, err := st.Create(ctx, p, 0o600)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	r, err := st.Open(ctx, p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	buf := make([]byte, 16)
	n, _ := r.Read(buf)
	_ = r.Close()
	if string(buf[:n]) != "hello" {
		t.Errorf("read back %q", buf[:n])
	}

	info, err := st.Stat(ctx, p)
	if err != nil || info.Size() != 5 {
		t.Fatalf("Stat = (%v, %v)", info, err)
	}

	entries, err := st.ReadDir(ctx, sub)
	if err != nil || len(entries) != 1 || entries[0].Name() != "f.txt" {
		t.Fatalf("ReadDir = (%v, %v)", entries, err)
	}

	tf, err := st.CreateTemp(ctx, dir, "tmp-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(tf.Name()), "tmp-") {
		t.Errorf("temp name %q lacks pattern prefix", tf.Name())
	}
	_ = tf.Close()

	td, err := st.MkdirTemp(ctx, dir, "tdir-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if _, err := os.Stat(td); err != nil {
		t.Errorf("MkdirTemp dir missing: %v", err)
	}

	moved := p + ".moved"
	if err := st.Rename(ctx, p, moved); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("renamed target missing: %v", err)
	}
	if err := st.Remove(ctx, moved); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := st.RemoveAll(ctx, sub); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(sub); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("RemoveAll left %s behind", sub)
	}
}

func TestSetDefaultStorage_NilResetsToLocal(t *testing.T) {
	stub := stubStorageOpsForDefaultDir{homePath: "/stub"}
	restore := SetDefaultStorage(stub)
	if got := getDefaultStorage(); got != StorageOps(stub) {
		t.Fatalf("default not swapped: %T", got)
	}
	// Setting nil must fall back to LocalStorageOps, not store a nil.
	restore2 := SetDefaultStorage(nil)
	if _, ok := getDefaultStorage().(LocalStorageOps); !ok {
		t.Errorf("SetDefaultStorage(nil) left %T, want LocalStorageOps", getDefaultStorage())
	}
	restore2()
	restore()
	if _, ok := getDefaultStorage().(LocalStorageOps); !ok {
		t.Errorf("restore chain left %T, want LocalStorageOps", getDefaultStorage())
	}
}

func TestResolveStorage(t *testing.T) {
	stub := stubStorageOpsForDefaultDir{homePath: "/stub"}
	if got := resolveStorage(stub); got != StorageOps(stub) {
		t.Errorf("resolveStorage(override) = %T, want the override", got)
	}
	if _, ok := resolveStorage(nil).(LocalStorageOps); !ok {
		t.Errorf("resolveStorage(nil) = %T, want package default", resolveStorage(nil))
	}
}
