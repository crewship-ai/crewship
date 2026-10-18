//go:build unix

package documents

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every case depends on ReadServiceSecret's POSIX mode-0600 gate, including
// content bounds; Windows does not implement this service-file contract.
func TestReadServiceSecretFileBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		mode          os.FileMode
		valid         bool
	}{
		{"valid", strings.Repeat("s", 32), 0600, true},
		{"trim newline", strings.Repeat("s", 64) + "\n", 0600, true},
		{"world readable", strings.Repeat("s", 32), 0644, false},
		{"group readable", strings.Repeat("s", 32), 0640, false},
		{"executable", strings.Repeat("s", 32), 0700, false},
		{"short", strings.Repeat("s", 31), 0600, false},
		{"long token", strings.Repeat("s", 257), 0600, false},
		{"oversized file", strings.Repeat(" ", 513) + strings.Repeat("s", 32), 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(p, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
			got, err := ReadServiceSecret(p)
			if tc.valid {
				if err != nil || got != strings.TrimSpace(tc.content) {
					t.Fatalf("valid secret: len=%d error=%v", len(got), err)
				}
			} else if err == nil {
				t.Fatal("invalid secret accepted")
			}
		})
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("s", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadServiceSecret(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := ReadServiceSecret(dir); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := ReadServiceSecret(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing secret accepted")
	}
}

func TestServiceSecretOpenedDescriptorSurvivesPathReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openServiceSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 32)), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("descriptor lost original permission evidence")
	}
	got, err := readServiceSecretContents(file)
	if err != nil || got != strings.Repeat("a", 32) {
		t.Fatal("read did not retain the originally opened inode")
	}
}

func TestServiceSecretGrowthAfterDescriptorStatIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openServiceSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != 32 {
		t.Fatal("unexpected initial descriptor state")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 4096)), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &secretCountingReader{Reader: file}
	if _, err := readServiceSecretContents(reader); err == nil {
		t.Fatal("grown secret accepted")
	}
	if reader.bytes != 513 {
		t.Fatalf("read %d bytes beyond bounded evidence", reader.bytes)
	}
}

func TestReadServiceSecretFIFORejectsWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadServiceSecret(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a nonregular token blocked")
	}
}
