package mooring

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestDeliveryReviewAcknowledgementCoversOnlyOneGate(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	a := deliveryOpen(t, s, owner, "human-a", "a")
	b := deliveryOpen(t, s, owner, "human-a", "b")
	barrier := deliveryRevoke(t, s, db, owner, "human-a", true)
	if err := s.AcknowledgeBlocked(t.Context(), owner, a.GateID, barrier.ID); err != nil {
		t.Fatal(err)
	}
	status, err := s.Barrier(t.Context(), owner.Key, "human-a", barrier.ID)
	if err != nil || status.Complete || status.RemainingMilliseconds != 1000 {
		t.Fatal("one ACK covered another live path", status, err)
	}
	if err = s.AcknowledgeBlocked(t.Context(), owner, b.GateID, barrier.ID); err != nil {
		t.Fatal(err)
	}
	status, err = s.Barrier(t.Context(), owner.Key, "human-a", barrier.ID)
	if err != nil || !status.Complete {
		t.Fatal(status, err)
	}
}

func TestDeliveryReviewPortableTargetReplaceCannotForgetUnexpiredGates(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	deliveryOpen(t, s, owner, "human-a", "live-before-target-replacement")
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = backup.ReplaceWorkspaceContents(t.Context(), tx, "w", "w"); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "delivery") {
			return
		}
		t.Fatalf("unexpected unrelated restore refusal: %v", err)
	}
	// Model the same-ID rows restored immediately after --replace's wipe.
	for _, sql := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('w','W','w')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p','w','p','P','u','{}')`,
	} {
		if _, err = tx.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// No synthetic clock advance: the source runtime's issued lease is live.
	proof := deliveryRevoke(t, s, db, owner, "human-a", true)
	if proof.NoIssuedGates {
		t.Fatal("portable target replacement forgot an unexpired source path and prepared a false empty-set revocation proof")
	}
}
