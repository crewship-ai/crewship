//go:build linux

package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSecretsWriteFailurePreservesExistingFileAndRemovesTemporary(t *testing.T) {
	// These tests are deliberately serial: the limit belongs to this test
	// process. Restore it before returning so unrelated tests and coverage
	// output are unaffected. A real file-size limit makes writes fail without
	// filling a shared volume or replacing the production filesystem calls.
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	for _, size := range []int{8, 16 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			path := SecretsFilePath(dir)
			before := []byte("existing-file-must-survive\n")
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			err := func() error {
				limited := original
				limited.Cur = 0
				if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
						t.Errorf("restore file-size limit: %v", err)
					}
				}()
				return writeFile(t.Context(), path, map[string]string{"SYNTHETIC_VALUE": strings.Repeat("x", size)})
			}()
			if err == nil || !errors.Is(err, syscall.EFBIG) {
				t.Fatalf("limited write was acknowledged: %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("failed persistence replaced existing file")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != secretsFileName {
				t.Fatal("failed write left temporary secret material")
			}
		})
	}
}

func TestBootstrapRefusesUnreadableSecretsSymlinkWithoutPublishing(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)
	if err := os.Symlink(path, path); err != nil {
		t.Fatal(err)
	}
	if err := LoadOrGenerate(t.Context(), dir, silentLogger()); err == nil || !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("broken secrets path accepted: %v", err)
	}
	for _, m := range managed {
		if os.Getenv(m.EnvVar) != "" {
			t.Errorf("unreadable secret file published %s", m.EnvVar)
		}
	}
	target, err := os.Readlink(path)
	if err != nil || target != path {
		t.Fatal("refused bootstrap replaced existing symlink")
	}
}
