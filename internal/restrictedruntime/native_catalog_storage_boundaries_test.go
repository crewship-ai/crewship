//go:build linux

package restrictedruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCatalogRefusesUntrustedOwnerStorage(t *testing.T) {
	source := func(context.Context, Plan) ([]NativeInputData, error) { return nil, ErrDenied }
	if _, err := NewFrozenNativeCatalog("relative", Docker{}, source); !errors.Is(err, ErrDenied) {
		t.Fatalf("relative root accepted: %v", err)
	}
	if _, err := NewFrozenNativeCatalog(t.TempDir(), Docker{}, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing authority accepted: %v", err)
	}
	for _, mode := range []string{"public directory", "root file", "owner directory", "owner symlink", "owner hardlink", "public owner", "oversize owner", "invalid owner"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "catalog")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "root file" {
				must(os.WriteFile(root, []byte("keep"), 0600))
			} else {
				must(os.Mkdir(root, 0700))
			}
			owner := filepath.Join(root, "owner")
			switch mode {
			case "public directory":
				must(os.Chmod(root, 0755))
			case "owner directory":
				must(os.Mkdir(owner, 0700))
			case "owner symlink", "owner hardlink":
				original := filepath.Join(parent, "canary")
				must(os.WriteFile(original, []byte(strings.Repeat("a", 32)), 0600))
				if mode == "owner symlink" {
					must(os.Symlink(original, owner))
				} else {
					must(os.Link(original, owner))
				}
			case "public owner":
				must(os.WriteFile(owner, []byte(strings.Repeat("a", 32)), 0644))
			case "oversize owner":
				must(os.WriteFile(owner, []byte(strings.Repeat("a", 8193)), 0600))
			case "invalid owner":
				must(os.WriteFile(owner, []byte("invalid namespace"), 0600))
			}
			if _, err := NewFrozenNativeCatalog(root, Docker{}, source); err == nil {
				t.Fatal("untrusted catalog accepted")
			}
			if mode == "owner symlink" || mode == "owner hardlink" {
				b, err := os.ReadFile(filepath.Join(parent, "canary"))
				if err != nil || string(b) != strings.Repeat("a", 32) {
					t.Fatalf("external identity modified: %q %v", b, err)
				}
			}
		})
	}
}

func TestNativeCatalogRecoveryRefusesCorruptRecordsAndCleansFailedWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "catalog")
	c, err := NewFrozenNativeCatalog(root, Docker{}, func(context.Context, Plan) ([]NativeInputData, error) { return nil, ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.load("missing"); !os.IsNotExist(err) {
		t.Fatalf("missing recovery record: %v", err)
	}
	if _, err := c.load("../escape"); !errors.Is(err, ErrDenied) {
		t.Fatalf("traversal accepted: %v", err)
	}
	if err := c.save(nativeSnapshotRecord{Attempt: "../escape"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("traversal persisted: %v", err)
	}
	for _, raw := range []string{"not-json", strings.Repeat("x", 8193), `{"Attempt":"different"}`} {
		if err := os.WriteFile(filepath.Join(root, "attempt.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.load("attempt"); !errors.Is(err, ErrDenied) {
			t.Fatalf("corrupt record admitted: %v", err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "blocked.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.save(nativeSnapshotRecord{Attempt: "blocked"}); err == nil {
		t.Fatal("directory overwrite accepted")
	}
	files, err := filepath.Glob(filepath.Join(root, ".snapshot-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed replacement leaked private temporary records: %v %v", files, err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := c.save(nativeSnapshotRecord{Attempt: "new"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("changed directory permissions ignored: %v", err)
	}
}
