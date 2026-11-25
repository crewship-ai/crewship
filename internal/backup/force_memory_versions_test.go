//go:build linux

package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

func TestForceRecoveryValidatesMemoryObjects(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"short digest", "invalid memory version digest"},
		{"nonhex digest", "invalid memory version digest"},
		{"different content", "differs from its digest"},
		{"wrong length", "differs from its digest"},
		{"target-only object", "no verified memory object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			rel := filepath.Join("versions", f.memSHA[:2], f.memSHA)
			src := filepath.Join(f.paths.Memory, rel)
			dir := t.TempDir()
			var err error
			switch tc.name {
			case "short digest", "nonhex digest":
				digest := "draft"
				if tc.name == "nonhex digest" {
					digest = strings.Repeat("z", 64)
				}
				_, err = f.db.Exec("UPDATE memory_versions SET sha256=? WHERE id='mv1'", digest)
			case "different content":
				b, readErr := os.ReadFile(src)
				if readErr != nil {
					t.Fatal(readErr)
				}
				b[0] ^= 1
				err = os.WriteFile(src, b, 0600)
			case "wrong length":
				_, err = f.db.Exec("UPDATE memory_versions SET bytes=bytes+1 WHERE id='mv1'")
			case "target-only object":
				dst := filepath.Join(dir, "memory", rel)
				if err = os.MkdirAll(filepath.Dir(dst), 0700); err == nil {
					err = os.Rename(src, dst)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "operator.txt"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			before := forceGenerationSnapshot(t, dir)
			bundle := f.create(t, true)
			rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{DataDir: dir, Force: true, BundlePath: bundle.Path, Identities: []age.Identity{f.identity}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery error = %v, want %q", err, tc.want)
			}
			if rep == nil || rep.PreviousDataDir != "" || rep.Result != backup.RestoreResultFailed {
				t.Fatalf("unexpected failure report: %+v", rep)
			}
			assertOriginalGeneration(t, dir, before)
		})
	}
}

func TestForceRecoveryRebasesLegacyPersonaMemory(t *testing.T) {
	f := newInstanceFixture(t)
	blob := filepath.Join(f.paths.Memory, "versions", f.memSHA[:2], f.memSHA)
	content, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	persona := filepath.Join(f.paths.Output, "crews", "c_lab", "shared", ".memory", "PERSONA.md")
	if err = os.MkdirAll(filepath.Dir(persona), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(blob, persona); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE memory_versions SET tier='persona',path=?,payload_ref=? WHERE id='mv1'", persona, persona); err != nil {
		t.Fatal(err)
	}
	bundle := f.create(t, true)
	// Only restored bytes can establish this historical version.
	if err = os.Remove(persona); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{DataDir: dir, Force: true, BundlePath: bundle.Path, Identities: []age.Identity{f.identity}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + rep.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ref string
	if err = db.QueryRow("SELECT payload_ref FROM memory_versions WHERE id='mv1'").Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "memory", "versions", f.memSHA[:2], f.memSHA); ref != want {
		t.Fatalf("payload reference = %q, want %q", ref, want)
	}
	got, err := memory.ReadVersion(t.Context(), db.DB, "ws_lab", persona, f.memSHA)
	if err != nil || string(got) != string(content) {
		t.Fatalf("read recovered version: %q, %v", got, err)
	}
}
