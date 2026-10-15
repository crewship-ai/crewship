package documents

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadServiceSecretFileBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits and service transport")
	}
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
