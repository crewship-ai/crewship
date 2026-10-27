package mooring

import "testing"

func TestReviewBEditLeaseAcquireRejectsPublicationRevocation(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	if _, err := db.Exec(`CREATE TRIGGER review_b_revoke_acquire AFTER INSERT ON mooring_edit_leases BEGIN UPDATE users SET suspended_at='revoked' WHERE id='u'; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(t.Context(), scope, p); err == nil {
		t.Fatal("Acquire acknowledged a lease after its own publication revoked the principal")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM mooring_edit_leases`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked acquisition committed: %d %v", count, err)
	}
}
func TestReviewBEditLeaseRenewRejectsPublicationRevocation(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	l, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	var original int64
	if err = db.QueryRow(`SELECT expires_ns FROM mooring_edit_leases WHERE page_id=?`, scope.ArtifactID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER review_b_revoke_renew AFTER UPDATE OF expires_ns ON mooring_edit_leases BEGIN UPDATE users SET suspended_at='revoked' WHERE id='u'; END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Renew(t.Context(), l); err == nil {
		t.Fatal("Renew acknowledged a lease after its own publication revoked the principal")
	}
	var suspended any
	if err = db.QueryRow(`SELECT suspended_at FROM users WHERE id='u'`).Scan(&suspended); err != nil || suspended != nil {
		t.Fatalf("denied renewal committed authority mutation: %v %v", suspended, err)
	}
}

func TestReviewBEditLeaseRevokeRejectsWeakerDurability(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	l, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = s.Revoke(t.Context(), tx, scope, p, l.Generation); err == nil {
		t.Fatal("Revoke accepted a NORMAL transaction for durable fencing")
	}
}
