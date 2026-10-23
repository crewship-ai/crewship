package mooring

import "testing"

// The callback remains synthetic like deliveryFixture. This proves unavoidable
// same-writer invalidation from a real canonical SQL mutation, not live RBAC.
func TestAuthorityEpochCanonicalGrantDeletionClosesGate(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('p','user','u','write','u')`); err != nil {
		t.Fatal(err)
	}
	g := deliveryOpen(t, s, owner, "human-a", "before-canonical-revoke")
	if _, err := db.Exec(`DELETE FROM page_grants WHERE page_id='p' AND subject_type='user' AND subject_id='u'`); err != nil {
		t.Fatal(err)
	}
	var closed bool
	if err := db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil || !closed {
		t.Fatal("canonical grant deletion left issued gate open", closed, err)
	}
}
