package documents

import (
	"errors"
	"strings"
	"testing"
)

func TestEngineBootRecoveryPreservesRetryAndFencesReplacement(t *testing.T) {
	store, db, _ := fixture(t, Options{})
	lease := acquire(t, store)
	for _, tc := range []struct {
		name, boot      string
		invalid, fenced bool
	}{
		{"invalid short", "short", true, false},
		{"first engine", "synthetic-first-engine", false, true},
		{"same boot retry", "synthetic-first-engine", false, false},
		{"replacement", "synthetic-next-engine", false, true},
		{"invalid long", strings.Repeat("x", 129), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store.RecoverEngine(ctx, tc.boot)
			if (err != nil) != tc.invalid {
				t.Fatalf("boot result: %v", err)
			}
			_, err = store.Renew(ctx, key, lease, allow)
			if tc.fenced {
				if !errors.Is(err, ErrFence) {
					t.Fatalf("replacement retained previous writer: %v", err)
				}
				lease = acquire(t, store)
			} else if err != nil {
				t.Fatalf("retry or invalid boot fenced current writer: %v", err)
			}
		})
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_engine_boot BEFORE UPDATE OF boot_id ON document_writer_fence BEGIN SELECT RAISE(ABORT,'injected boot failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverEngine(ctx, "synthetic-rejected-engine"); err == nil {
		t.Fatal("failed durable boot update was accepted")
	}
	if _, err := store.Renew(ctx, key, lease, allow); err != nil {
		t.Fatalf("failed boot update did not roll back writer fencing: %v", err)
	}
}
