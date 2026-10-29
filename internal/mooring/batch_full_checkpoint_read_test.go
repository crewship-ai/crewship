package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Actual FULL SQL; application, reader authority and BOOTTIME are explicit mocks.
func TestReadFullCheckpointUsesOriginalReaderAndFinalOwnerFence(t *testing.T) {
	for _, mode := range []string{"success", "initial-denied", "final-denied", "owner-expires"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			calls := 0
			sentinel := errors.New("current reader denied")
			s.config.Authority = func(_ context.Context, _ *sql.Tx, a BatchAccess) error {
				if a.Intent != "read_checkpoint" || !a.FromStore(s) || a.Scope != lease.Key {
					return ErrInvalid
				}
				calls++
				if (mode == "initial-denied" && calls == 1) || (mode == "final-denied" && calls == 2) {
					return sentinel
				}
				if mode == "owner-expires" && calls == 2 {
					s.config.Owners.clock.(*ownerTestClock).advance(6 * time.Second)
				}
				return nil
			}
			snapshot, err := s.ReadFullCheckpoint(t.Context(), lease)
			if mode != "success" {
				if err == nil || snapshot.Checkpoint.State != "" || snapshot.Targets != nil {
					t.Fatal("denied read returned checkpoint", err)
				}
				if mode == "initial-denied" && calls != 1 {
					t.Fatal("initial denial was retried", calls)
				}
				if mode == "final-denied" && !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
				if mode == "owner-expires" && !errors.Is(err, ErrOwnerStale) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || calls != 2 || string(snapshot.Checkpoint.Digest) != basis.Digest || snapshot.Checkpoint.Generation != "1" || len(snapshot.Targets) != 1 {
				t.Fatal("wrong retained snapshot", calls, err)
			}
			snapshot.Targets[0].Revision = "999"
			snapshot.Checkpoint.Assets = append(snapshot.Checkpoint.Assets, snapshot.Checkpoint.Assets...)
			again, err := s.ReadFullCheckpoint(t.Context(), lease)
			if err != nil || again.Targets[0].Revision != "0" || string(again.Checkpoint.Digest) != basis.Digest {
				t.Fatal("read alias changed durable state", err)
			}
		})
	}
}
func TestReadFullCheckpointDeniesBeforePoisonedMetadata(t *testing.T) {
	s, lease, _ := fullBasisFixture(t)
	if _, err := s.db.Exec(`DROP TRIGGER mooring_checkpoint_metadata_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_metadata_json='{}'`); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("original source denied")
	s.config.Authority = func(context.Context, *sql.Tx, BatchAccess) error { return sentinel }
	if _, err := s.ReadFullCheckpoint(t.Context(), lease); !errors.Is(err, sentinel) {
		t.Fatal("metadata inspected before reader authority", err)
	}
}
