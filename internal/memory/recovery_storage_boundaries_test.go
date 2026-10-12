package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryStorageErrorsKeepIntentRetryable(t *testing.T) {
	for _, point := range []string{"after_intent", "after_rename"} {
		for _, stage := range []string{"renamed", "confirmed", "anchor"} {
			t.Run(point+"/"+stage, func(t *testing.T) {
				f := newMutateFixture(t)
				crash := errors.New("fixture crash")
				req := f.req("recover", OpAppend, "recoverable fact\n")
				req.testHook = func(step string) error {
					if step == point {
						return crash
					}
					return nil
				}
				if _, err := f.mutate(t, req); !errors.Is(err, crash) {
					t.Fatalf("crash point missed: %v", err)
				}
				trigger := `CREATE TRIGGER fail_recovery BEFORE UPDATE OF state ON memory_mutations WHEN NEW.state='` + stage + `' BEGIN SELECT RAISE(ABORT,'fixture recovery unavailable'); END`
				if stage == "anchor" {
					trigger = `CREATE TRIGGER fail_recovery BEFORE INSERT ON memory_revisions BEGIN SELECT RAISE(ABORT,'fixture recovery unavailable'); END`
				}
				if _, err := f.db.ExecContext(t.Context(), trigger); err != nil {
					t.Fatal(err)
				}
				if n, c, err := RecoverPending(t.Context(), f.db, f.blobRoot); err == nil || !strings.Contains(err.Error(), "fixture recovery unavailable") || n != 0 || c != 0 {
					t.Fatalf("failed recovery acknowledged: %d %d %v", n, c, err)
				}
				var confirmed int
				if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_mutations WHERE state='confirmed'`).Scan(&confirmed); err != nil || confirmed != 0 {
					t.Fatalf("partly committed confirmation: %d %v", confirmed, err)
				}
				if _, err := f.db.ExecContext(t.Context(), `DROP TRIGGER fail_recovery`); err != nil {
					t.Fatal(err)
				}
				if n, c, err := RecoverPending(t.Context(), f.db, f.blobRoot); err != nil || n != 1 || c != 0 {
					t.Fatalf("retry failed: %d %d %v", n, c, err)
				}
				if f.onDisk(t) != "recoverable fact\n" {
					t.Fatal("recovery duplicated or lost bytes")
				}
				if n, c, err := RecoverPending(t.Context(), f.db, f.blobRoot); err != nil || n != 0 || c != 0 {
					t.Fatalf("repeated recovery not idle: %d %d %v", n, c, err)
				}
			})
		}
	}
}

func TestRecoveryCannotReplaceDriftWhenConflictRecordingFails(t *testing.T) {
	f := newMutateFixture(t)
	crash := errors.New("fixture crash")
	req := f.req("recover", OpAppend, "pending fact\n")
	req.testHook = func(step string) error {
		if step == "after_intent" {
			return crash
		}
		return nil
	}
	if _, err := f.mutate(t, req); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, []byte("operator correction\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(t.Context(), `CREATE TRIGGER fail_conflict BEFORE UPDATE OF state ON memory_mutations WHEN NEW.state='conflicted' BEGIN SELECT RAISE(ABORT,'fixture conflict unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if n, c, err := RecoverPending(t.Context(), f.db, f.blobRoot); err == nil || n != 0 || c != 0 {
		t.Fatalf("failed conflict recording hidden: %d %d %v", n, c, err)
	}
	if f.onDisk(t) != "operator correction\n" {
		t.Fatal("operator content overwritten")
	}
	if _, err := f.db.ExecContext(t.Context(), `DROP TRIGGER fail_conflict`); err != nil {
		t.Fatal(err)
	}
	if n, c, err := RecoverPending(t.Context(), f.db, f.blobRoot); err != nil || n != 0 || c != 1 {
		t.Fatalf("conflict retry: %d %d %v", n, c, err)
	}
	if f.onDisk(t) != "operator correction\n" {
		t.Fatal("conflict retry overwrote operator content")
	}
}

func TestIntentBlobRecoveryFallbackAndRootValidation(t *testing.T) {
	root := t.TempDir()
	body := []byte("recoverable data")
	sha := sha256Hex(body)
	name := blobPathFor(root, sha)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", filepath.Join(root, "old-moved-location")} {
		got, err := readIntentBlob(ref, root, sha)
		if err != nil || string(got) != string(body) {
			t.Fatalf("moved blob fallback failed: %q %v", got, err)
		}
	}
	if _, err := readIntentBlob(root, root, sha); err == nil {
		t.Fatal("directory blob ignored")
	}
	if _, err := readIntentBlob("", "", sha); err == nil {
		t.Fatal("missing blob location accepted")
	}
	for _, tc := range [][2]string{{"", sha}, {root, "short"}, {root, strings.Repeat("z", 64)}, {filepath.Join(root, "absent"), sha}} {
		if _, err := readRootedIntentBlob(tc[0], tc[1]); err == nil {
			t.Fatalf("invalid rooted location accepted: %v", tc)
		}
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readRootedIntentBlob(root, sha); err == nil {
		t.Fatal("directory accepted as durable blob")
	}
}
