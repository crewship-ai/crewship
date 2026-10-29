package mooring

import (
	"errors"
	"testing"
)

func TestEditLeaseRechecksAuthorityAfterItsSQL(t *testing.T) {
	for _, operation := range []string{"acquire", "renew", "handoff", "release", "request"} {
		t.Run(operation, func(t *testing.T) {
			s, db, _, scope, p := editLeaseFixture(t)
			var lease EditLease
			var err error
			target := EditLeasePrincipal{ActorID: p.ActorID, SessionID: "tab-b"}
			if operation != "acquire" {
				lease, err = s.Acquire(t.Context(), scope, p)
				if err != nil {
					t.Fatal(err)
				}
			}
			if operation == "handoff" {
				if err = s.RequestHandoff(t.Context(), scope, target); err != nil {
					t.Fatal(err)
				}
			}
			table, event := "mooring_edit_leases", "UPDATE"
			if operation == "acquire" {
				event = "INSERT"
			}
			if operation == "request" {
				table, event = "mooring_edit_lease_requests", "INSERT"
			}
			// SQL is trusted here; model a concurrent rights transition at the last
			// publication boundary without relying on a wall-clock race in the test.
			if _, err = db.Exec(`CREATE TRIGGER revoke_during_lease AFTER ` + event + ` ON ` + table + ` BEGIN UPDATE users SET suspended_at='revoked' WHERE id='u'; END`); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "acquire":
				_, err = s.Acquire(t.Context(), scope, p)
			case "renew":
				_, err = s.Renew(t.Context(), lease)
			case "handoff":
				_, err = s.Handoff(t.Context(), lease, target)
			case "release":
				err = s.Release(t.Context(), lease)
			case "request":
				err = s.RequestHandoff(t.Context(), scope, target)
			}
			if !errors.Is(err, ErrEditLeaseDenied) {
				t.Fatalf("%s committed after authority loss: %v", operation, err)
			}
			var suspended bool
			if err = db.QueryRow(`SELECT suspended_at IS NOT NULL FROM users WHERE id='u'`).Scan(&suspended); err != nil || suspended {
				t.Fatalf("failed operation was not rolled back: %v %v", suspended, err)
			}
		})
	}
}
func TestEditLeaseRevokeRejectsCallerTransactionWithoutFullDurability(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	lease, err := s.Acquire(t.Context(), scope, p)
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
	if err = s.Revoke(t.Context(), tx, scope, p, lease.Generation); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revocation admitted weaker durability: %v", err)
	}
}
