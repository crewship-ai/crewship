//go:build !windows

package managedlaunch

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type spoolReadObserver struct {
	source io.Reader
	dir    string
	t      *testing.T
	seen   bool
}

func (r *spoolReadObserver) Read(p []byte) (int, error) {
	files, e := filepath.Glob(filepath.Join(r.dir, "crewship-capture-*"))
	if e != nil {
		r.t.Fatal(e)
	}
	if len(files) != 0 {
		r.t.Fatal("archive bytes written while named spool remained")
	}
	if runtime.GOOS == "linux" {
		fds, e := os.ReadDir("/proc/self/fd")
		if e != nil {
			r.t.Fatal(e)
		}
		for _, fd := range fds {
			path := filepath.Join("/proc/self/fd", fd.Name())
			target, e := os.Readlink(path)
			if e != nil || !strings.HasPrefix(target, filepath.Join(r.dir, "crewship-capture-")) || !strings.HasSuffix(target, " (deleted)") {
				continue
			}
			info, e := os.Stat(path)
			if e != nil {
				r.t.Fatal(e)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				r.t.Fatalf("unsafe spool mode%v", info.Mode())
			}
			r.seen = true
		}
	}
	return r.source.Read(p)
}

type spoolInjectedError struct{}

func (spoolInjectedError) Read([]byte) (int, error) {
	return 0, errors.New("owned injected read failure")
}
func TestManagedArchiveSpoolModeAndCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	good := artifactArchive(t, "codex", 0555, 0, nativeFixture(), false)
	for _, c := range []struct {
		name   string
		reader io.Reader
		valid  bool
	}{{"valid", bytes.NewReader(good), true}, {"truncated", bytes.NewReader(good[:529]), false}, {"read-error", io.MultiReader(bytes.NewReader(good[:529]), spoolInjectedError{}), false}, {"format", bytes.NewReader(artifactArchive(t, "codex", 0555, 0, []byte("script"), false)), false}, {"extra", bytes.NewReader(artifactArchive(t, "codex", 0555, 0, nativeFixture(), true)), false}} {
		t.Run(c.name, func(t *testing.T) {
			reader := &spoolReadObserver{source: c.reader, dir: dir, t: t}
			_, e := CaptureArchive("/opt/native/codex", reader)
			if (e == nil) != c.valid {
				t.Fatalf("unexpected qualification error%v", e)
			}
			if runtime.GOOS == "linux" && !reader.seen {
				t.Fatal("spool mode was never observed")
			}
			files, e := os.ReadDir(dir)
			if e != nil || len(files) != 0 {
				t.Fatalf("spool leaked files%v error%v", files, e)
			}
		})
	}
}
func TestManagedArchiveSpoolCreationFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if e := os.WriteFile(file, []byte("owned"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("TMPDIR", file)
	if _, e := CaptureArchive("/opt/native/codex", bytes.NewReader(artifactArchive(t, "codex", 0555, 0, nativeFixture(), false))); e == nil {
		t.Fatal("unavailable staging accepted")
	}
}
func TestManagedVerifyArchiveUsesOpenFileAfterPathReplacement(t *testing.T) {
	raw := nativeFixture()
	file := streamOwnedFile(t, raw)
	if e := os.Rename(file.Name(), file.Name()+"-old"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(file.Name(), bytes.Repeat([]byte("x"), len(raw)), 0600); e != nil {
		t.Fatal(e)
	}
	archive := artifactArchive(t, "codex", 0555, 0, raw, false)
	if e := VerifyArchiveFile(bytes.NewReader(archive), "codex", file); e != nil {
		t.Fatal("verified file was reopened by path", e)
	}
}
