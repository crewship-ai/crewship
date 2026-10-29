package mooring

import "testing"

func TestDeliveryIssuedExpiryCannotBeCascadedByParentDeletion(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	deliveryOpen(t, s, owner, "human-a", "still-live")
	barrier := deliveryRevoke(t, s, db, owner, "human-a", true)
	for _, statement := range []string{`DELETE FROM pages WHERE id='p'`, `DELETE FROM workspaces WHERE id='w'`} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("parent deletion erased live delivery evidence: %s", statement)
		}
		var gates, barriers int
		if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_delivery_gates`).Scan(&gates); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_delivery_barriers`).Scan(&barriers); err != nil || gates != 1 || barriers != 1 {
			t.Fatal("refused deletion changed expiry/barrier evidence", gates, barriers, err)
		}
	}
	status, err := s.Barrier(t.Context(), owner.Key, "human-a", barrier.ID)
	if err != nil || status.Complete || status.RemainingMilliseconds != 1000 {
		t.Fatal("parent deletion shortened revocation barrier", status, err)
	}
}
