package providerpool

import (
	"errors"
	"testing"
	"time"
)

func TestStoreRejectsIncompletePoolIdentity(t *testing.T) {
	s, _, _ := poolFixture(t)
	for _, missing := range []string{"id", "workspace", "creator"} {
		pool := fixturePool()
		switch missing {
		case "id":
			pool.ID = ""
		case "workspace":
			pool.WorkspaceID = ""
		case "creator":
			pool.CreatedBy = ""
		}
		if err := s.Create(t.Context(), pool); !errors.Is(err, ErrInvalid) {
			t.Fatalf("missing %s accepted: %v", missing, err)
		}
	}
	for _, ids := range [][2]string{{"", "pool"}, {"ws", ""}} {
		if _, err := s.Choose(t.Context(), ids[0], ids[1], time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("incomplete selection identity: %v", err)
		}
		if _, _, err := s.Snapshot(t.Context(), ids[0], ids[1]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("incomplete snapshot identity: %v", err)
		}
	}
}

func TestStoreClosedDatabasePreservesOutage(t *testing.T) {
	s, db, _ := poolFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	checks := []struct {
		name string
		run  func() error
	}{
		{"create", func() error { return s.Create(t.Context(), fixturePool()) }},
		{"update", func() error { return s.Update(t.Context(), fixturePool(), 1) }},
		{"delete", func() error { return s.Delete(t.Context(), "ws", "pool", 1) }},
		{"choose", func() error { _, err := s.Choose(t.Context(), "ws", "pool", now); return err }},
		{"snapshot", func() error { _, _, err := s.Snapshot(t.Context(), "ws", "pool"); return err }},
		{"record", func() error {
			return s.RecordObservation(t.Context(), "ws", "a", Observation{Generation: Generation("value"), At: now, BlockedReason: "billing", Source: "provider_http"})
		}},
		{"clear", func() error { _, err := s.ClearObservation(t.Context(), "ws", "a", 1); return err }},
		{"read", func() error { _, err := s.ReadObservation(t.Context(), "ws", "a"); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
				t.Fatalf("outage disguised: %v", err)
			}
		})
	}
}

func TestPoolWritesRollbackWhenStorageRejectsMutation(t *testing.T) {
	for _, scenario := range []string{"create pool", "create member", "update member", "remove member", "update pool", "delete pool", "reserve turn", "record selection"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, _ := poolFixture(t)
			pool := fixturePool()
			creating := scenario == "create pool" || scenario == "create member"
			if !creating {
				if err := s.Create(t.Context(), pool); err != nil {
					t.Fatal(err)
				}
			}
			table, event := "provider_login_pools", "UPDATE"
			switch scenario {
			case "create pool":
				event = "INSERT"
			case "create member", "update member":
				table = "provider_login_pool_members"
				event = "INSERT"
			case "remove member":
				table = "provider_login_pool_members"
				event = "DELETE"
				pool.Members = pool.Members[:1]
			case "record selection":
				table = "provider_login_pool_members"
			}
			execPoolSQL(t, db, "CREATE TRIGGER fail_write BEFORE "+event+" ON "+table+" BEGIN SELECT RAISE(ABORT,'storage unavailable'); END")
			var err error
			switch scenario {
			case "create pool", "create member":
				err = s.Create(t.Context(), pool)
			case "delete pool":
				err = s.Delete(t.Context(), "ws", "pool", 1)
			case "reserve turn", "record selection":
				_, err = s.Choose(t.Context(), "ws", "pool", time.Now())
			default:
				pool.Name = "changed"
				err = s.Update(t.Context(), pool, 1)
			}
			if err == nil {
				t.Fatal("rejected write reported success")
			}
			if creating {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM provider_login_pools`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial create persisted: %d %v", count, err)
				}
				return
			}
			var name string
			var revision, sequence, members int
			if err := db.QueryRow(`SELECT name,revision,selection_seq,(SELECT COUNT(*) FROM provider_login_pool_members) FROM provider_login_pools WHERE id='pool' AND deleted_at IS NULL`).Scan(&name, &revision, &sequence, &members); err != nil {
				t.Fatal(err)
			}
			if name != "OpenAI team" || revision != 1 || sequence != 0 || members != 2 {
				t.Fatalf("partial mutation persisted: %s %d %d %d", name, revision, sequence, members)
			}
		})
	}
}

func TestObservationRejectsMalformedStoredDatesAndMissingCredential(t *testing.T) {
	s, db, _ := poolFixture(t)
	now := time.Now()
	observation := Observation{Generation: Generation("test-ciphertext-never-decrypted"), At: now, BlockedReason: "billing", Source: "provider_http"}
	if err := s.RecordObservation(t.Context(), "ws", "missing", observation); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing credential: %v", err)
	}
	if err := s.RecordObservation(t.Context(), "ws", "a", observation); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"observed_at", "cooldown_until"} {
		execPoolSQL(t, db, `UPDATE provider_login_availability SET observed_at=?,cooldown_until=NULL`, now.UTC().Format(time.RFC3339Nano))
		execPoolSQL(t, db, "UPDATE provider_login_availability SET "+column+"='malformed'")
		if _, err := s.ReadObservation(t.Context(), "ws", "a"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed %s accepted: %v", column, err)
		}
	}
	if _, err := s.ReadObservation(t.Context(), "", "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing workspace: %v", err)
	}
	if _, err := s.ClearObservation(t.Context(), "ws", "a", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing revision: %v", err)
	}
}

func TestMalformedAccountExpiryCannotBeConfiguredOrSelected(t *testing.T) {
	for _, operation := range []string{"create", "update", "choose", "snapshot"} {
		t.Run(operation, func(t *testing.T) {
			s, db, _ := poolFixture(t)
			pool := fixturePool()
			if operation != "create" {
				if err := s.Create(t.Context(), pool); err != nil {
					t.Fatal(err)
				}
			}
			execPoolSQL(t, db, `UPDATE credentials SET token_expires_at='malformed' WHERE id='a'`)
			var err error
			switch operation {
			case "create":
				err = s.Create(t.Context(), pool)
			case "update":
				err = s.Update(t.Context(), pool, 1)
			case "choose":
				_, err = s.Choose(t.Context(), "ws", "pool", time.Now())
			case "snapshot":
				_, _, err = s.Snapshot(t.Context(), "ws", "pool")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("malformed expiry bypassed validation: %v", err)
			}
			var seq int
			if err := db.QueryRow(`SELECT COALESCE(SUM(selection_seq),0) FROM provider_login_pools`).Scan(&seq); err != nil || seq != 0 {
				t.Fatalf("failure consumed selection: %d %v", seq, err)
			}
		})
	}
}
