package providerpool

import (
	"errors"
	"testing"
	"time"
)

func TestPoolLifecycle(t *testing.T) {
	s, db, _ := poolFixture(t)
	p := fixturePool()
	if err := s.Create(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p.Name = "Renamed"
	p.Members = []Member{{CredentialID: "b", Priority: 4}}
	if err := s.Update(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(t.Context(), p, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale update: %v", err)
	}
	policy, members, err := s.Snapshot(t.Context(), "ws", "pool")
	if err != nil || policy.Provider != "OPENAI" || len(members) != 1 || members[0].ID != "b" || members[0].Priority != 4 {
		t.Fatalf("snapshot: %+v %v", members, err)
	}
	if err := s.Delete(t.Context(), "foreign", "pool", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if err := s.Delete(t.Context(), "ws", "pool", 1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := s.Delete(t.Context(), "ws", "pool", 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Snapshot(t.Context(), "ws", "pool"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted snapshot: %v", err)
	}
	if _, err := s.Choose(t.Context(), "ws", "pool", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted selection: %v", err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM credentials WHERE id IN ('a','b')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("credentials removed: %d %v", count, err)
	}
}

func TestPoolUpdateRollback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []Member
		want    error
	}{
		{"empty", nil, ErrInvalid},
		{"foreign", []Member{{CredentialID: "foreign-account"}}, ErrInvalid},
		{"duplicate", []Member{{CredentialID: "a"}, {CredentialID: "a"}}, ErrInvalid},
		{"cross owner", []Member{{CredentialID: "a"}, {CredentialID: "c"}}, ErrCrossOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := poolFixture(t)
			execPoolSQL(t, db, `UPDATE credentials SET created_by='other' WHERE id='c'`)
			p := fixturePool()
			if err := s.Create(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			p.Members = tc.members
			if err := s.Update(t.Context(), p, 1); !errors.Is(err, tc.want) {
				t.Fatalf("update: %v", err)
			}
			var revision, count int
			if err := db.QueryRowContext(t.Context(), `SELECT revision,(SELECT COUNT(*) FROM provider_login_pool_members WHERE pool_id='pool') FROM provider_login_pools WHERE id='pool'`).Scan(&revision, &count); err != nil || revision != 1 || count != 2 {
				t.Fatalf("partial update: revision=%d members=%d err=%v", revision, count, err)
			}
		})
	}
}

func TestPoolUpdatePreservesSelectionAndIdentity(t *testing.T) {
	s, db, _ := poolFixture(t)
	p := fixturePool()
	if err := s.Create(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	chosen, err := s.Choose(t.Context(), "ws", "pool", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p.Policy.Provider = "GOOGLE"
	p.Policy.Mode = "api_key"
	if err := s.Update(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	policy, members, err := s.Snapshot(t.Context(), "ws", "pool")
	if err != nil || policy.Provider != "OPENAI" || policy.Mode != "subscription" {
		t.Fatalf("identity changed: %+v %v", policy, err)
	}
	for _, m := range members {
		if m.ID == chosen.ID && m.LastSelected != chosen.LastSelected {
			t.Fatal("retained member lost selection history")
		}
	}
	other := fixturePool()
	other.ID = "other-pool"
	other.Name = "Reserved"
	if err := s.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	p.Name = "Reserved"
	if err := s.Update(t.Context(), p, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("name collision: %v", err)
	}
	var revision int
	if err := db.QueryRowContext(t.Context(), `SELECT revision FROM provider_login_pools WHERE id='pool'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("conflict changed revision: %d %v", revision, err)
	}
}

func TestPoolConcurrentEdit(t *testing.T) {
	s, _, _ := poolFixture(t)
	p := fixturePool()
	if err := s.Create(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- s.Update(t.Context(), p, 1) }()
	}
	close(start)
	success, stale := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrStale):
			stale++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}
