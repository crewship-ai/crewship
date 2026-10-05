//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestProjectManifestRejectsAmbiguousWireData(t *testing.T) {
	for _, body := range []string{"", "not JSON", `{}`, `{"unknown":true}`, `{} {}`, strings.Repeat(" ", 32768) + `{}`} {
		if err := verifyProjectInputs(strings.NewReader(body)); !errors.Is(err, restrictedruntime.ErrDenied) {
			t.Fatalf("untrusted manifest accepted: %q %v", body[:min(len(body), 50)], err)
		}
	}
}

func TestProjectManifestRejectsOwnershipPermissionsMissingAndModifiedInputs(t *testing.T) {
	for _, mutation := range []string{"exact", "hash", "manifest-hash", "missing", "missing-root", "root-file", "root-symlink", "wrong-owner", "directory-mode", "extra-directory"} {
		t.Run(mutation, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "version1")
			file := filepath.Join(dir, "input.txt")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
			content := []byte("owned source")
			sum := sha256.Sum256(content)
			m, err := restrictedruntime.NewNativeInputManifest([]restrictedruntime.NativeInputFile{{VersionID: "version1", Name: "input.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, content, 0400); err != nil {
				t.Fatal(err)
			}
			uid := uint32(os.Getuid())
			switch mutation {
			case "hash":
				if err := os.Chmod(file, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("wrong source"), 0400); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(file, 0400); err != nil {
					t.Fatal(err)
				}
			case "manifest-hash":
				m.Hash = strings.Repeat("0", 64)
			case "missing":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "missing-root":
				base = filepath.Join(base, "missing")
			case "root-file":
				base = file
			case "root-symlink":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(base, link); err != nil {
					t.Fatal(err)
				}
				base = link
			case "wrong-owner":
				uid++
			case "extra-directory":
				if err := os.Mkdir(filepath.Join(base, "extra"), 0500); err != nil {
					t.Fatal(err)
				}
			}
			mode := os.FileMode(0500)
			if mutation == "directory-mode" {
				mode = 0700
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			err = verifyProjectInputsAt(base, *m, uid)
			if mutation == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, restrictedruntime.ErrDenied) {
				t.Fatalf("%s accepted: %v", mutation, err)
			}
		})
	}
}

func TestProjectManifestWireCannotSubstituteAbsentFixedRoot(t *testing.T) {
	// The empty Debian acceptance fixture has no input mount. Never inspect a
	// real deployment's fixed input directory if this test is run there.
	if _, err := os.Stat(restrictedruntime.NativeInputTarget); !os.IsNotExist(err) {
		return
	}
	sum := sha256.Sum256([]byte("owned source"))
	manifest, err := restrictedruntime.NewNativeInputManifest([]restrictedruntime.NativeInputFile{{VersionID: "version1", Name: "input.txt", Size: 12, SHA256: hex.EncodeToString(sum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyProjectInputs(strings.NewReader(string(raw))); !errors.Is(err, restrictedruntime.ErrDenied) {
		t.Fatalf("absent mount accepted: %v", err)
	}
}
