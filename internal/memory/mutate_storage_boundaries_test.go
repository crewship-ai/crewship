package memory

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMutationStorageFailuresRetainRecoverableIntentWithoutFalseConfirmation(t *testing.T) {
	for _, stage := range []string{"intent", "confirmation", "anchor"} {
		t.Run(stage, func(t *testing.T) {
			f := newMutateFixture(t)
			trigger := `CREATE TRIGGER injected_memory_failure BEFORE INSERT ON memory_mutations BEGIN SELECT RAISE(ABORT,'fixture memory storage unavailable'); END`
			if stage == "confirmation" {
				trigger = `CREATE TRIGGER injected_memory_failure BEFORE UPDATE OF state ON memory_mutations WHEN NEW.state='confirmed' BEGIN SELECT RAISE(ABORT,'fixture memory storage unavailable'); END`
			}
			if stage == "anchor" {
				trigger = `CREATE TRIGGER injected_memory_failure BEFORE INSERT ON memory_revisions BEGIN SELECT RAISE(ABORT,'fixture memory storage unavailable'); END`
			}
			if _, err := f.db.ExecContext(t.Context(), trigger); err != nil {
				t.Fatal(err)
			}
			req := f.req("storage-failure", OpAppend, "durable fact\n")
			_, err := f.mutate(t, req)
			if err == nil || !strings.Contains(err.Error(), "fixture memory storage unavailable") {
				t.Fatalf("durable failure hidden: %v", err)
			}
			var confirmed, anchors int
			if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_mutations WHERE state='confirmed'`).Scan(&confirmed); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_revisions`).Scan(&anchors); err != nil {
				t.Fatal(err)
			}
			if confirmed != 0 || anchors != 0 {
				t.Fatalf("partial confirmation: %d mutations, %d anchors", confirmed, anchors)
			}
			if stage == "intent" {
				if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unrecorded target written: %v", err)
				}
			} else if got := f.onDisk(t); got != "durable fact\n" {
				t.Fatalf("recoverable content lost: %q", got)
			}
			if _, err := f.db.ExecContext(t.Context(), `DROP TRIGGER injected_memory_failure`); err != nil {
				t.Fatal(err)
			}
			recovered := f.mustMutate(t, req)
			if recovered.Revision != 1 || !recovered.LedgerRecorded || f.onDisk(t) != "durable fact\n" {
				t.Fatalf("retry did not recover exactly once: %+v", recovered)
			}
			again := f.mustMutate(t, req)
			if !again.Idempotent || again.Revision != 1 || f.onDisk(t) != "durable fact\n" {
				t.Fatalf("retry duplicated write: %+v", again)
			}
		})
	}
}

func TestMemoryRecoveryRefusesUnavailableLedger(t *testing.T) {
	f := newMutateFixture(t)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAnchor(t.Context(), f.db, "ws_test", f.auditPath); err == nil {
		t.Fatal("closed anchor treated as missing")
	}
	if _, err := loadOperation(t.Context(), f.db, "ws_test", f.auditPath, "op"); err == nil {
		t.Fatal("closed operation treated as missing")
	}
	if err := confirmMutation(t.Context(), f.db, f.req("op", OpAppend, "x"), "id", 1, "hash", 1, MutateResult{}); err == nil {
		t.Fatal("closed ledger confirmed mutation")
	}
	if n, c, err := RecoverPending(t.Context(), f.db, f.blobRoot); err == nil || n != 0 || c != 0 {
		t.Fatalf("recovery silently succeeded: %d %d %v", n, c, err)
	}
	if _, err := f.mutate(t, f.req("op", OpAppend, "x")); err == nil {
		t.Fatal("write succeeded without ledger")
	}
	if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrecorded target written: %v", err)
	}
}
