package egresspolicy

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openCrewsTestDB builds the minimal slice of the crews table the egress
// policy reads: network_mode + allowed_domains (migration v18) and the
// deleted_at soft-delete column. Column semantics mirror
// migrate_consts_v16_v25.go.
func openCrewsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE crews (
	id             TEXT PRIMARY KEY,
	network_mode   TEXT NOT NULL DEFAULT 'free',
	allowed_domains TEXT,
	deleted_at     TEXT
);`); err != nil {
		t.Fatalf("create crews: %v", err)
	}
	return db
}

func seedCrew(t *testing.T, db *sql.DB, id, mode, domains string) {
	t.Helper()
	var dom any
	if domains == "" {
		dom = nil
	} else {
		dom = domains
	}
	if _, err := db.Exec(`INSERT INTO crews (id, network_mode, allowed_domains) VALUES (?, ?, ?)`, id, mode, dom); err != nil {
		t.Fatalf("seed crew %s: %v", id, err)
	}
}

// TestCheck_Semantics pins the crew-policy decision table that every egress
// path (http steps, notify, hooks) now resolves through. It is the spec: a
// change here is a change to the shared security boundary.
func TestCheck_Semantics(t *testing.T) {
	db := openCrewsTestDB(t)
	seedCrew(t, db, "crew_free", "free", "")
	seedCrew(t, db, "crew_restricted", "restricted", `["api.partner.com","127.0.0.1"]`)
	seedCrew(t, db, "crew_restricted_bare", "restricted", "")
	seedCrew(t, db, "crew_weird", "yolo", "")
	seedCrew(t, db, "crew_badjson", "restricted", `{not json`)
	// A soft-deleted restricted crew must read as "missing" → allow (matches
	// the deleted-mid-operation convention), so it is NOT a fail-closed row.
	seedCrew(t, db, "crew_deleted", "restricted", `["api.partner.com"]`)
	if _, err := db.Exec(`UPDATE crews SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = 'crew_deleted'`); err != nil {
		t.Fatalf("soft-delete crew: %v", err)
	}

	ctx := context.Background()
	cases := []struct {
		name  string
		crew  string
		host  string
		allow bool
	}{
		{"empty crew allows", "", "evil.example.com", true},
		{"missing crew row allows (v18 default free)", "crew_ghost", "evil.example.com", true},
		{"soft-deleted crew reads as missing → allow", "crew_deleted", "evil.example.com", true},
		{"free mode allows anything", "crew_free", "evil.example.com", true},
		{"restricted allows listed host", "crew_restricted", "api.partner.com", true},
		{"restricted strips port before match", "crew_restricted", "127.0.0.1:8443", true},
		{"restricted is case-insensitive", "crew_restricted", "API.PARTNER.COM", true},
		{"restricted blocks unlisted host", "crew_restricted", "evil.example.com", false},
		{"restricted is exact-match (no subdomains)", "crew_restricted", "sub.api.partner.com", false},
		{"restricted keeps sidecar default LLM domains", "crew_restricted_bare", "api.anthropic.com", true},
		{"restricted with no crew domains blocks the rest", "crew_restricted_bare", "api.partner.com", false},
		{"unknown mode fails closed", "crew_weird", "api.partner.com", false},
		{"malformed allowed_domains fails closed", "crew_badjson", "api.anthropic.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(ctx, db, c.crew, c.host)
			if c.allow && err != nil {
				t.Errorf("Check(%q, %q) = %v, want allow", c.crew, c.host, err)
			}
			if !c.allow && err == nil {
				t.Errorf("Check(%q, %q) allowed, want block", c.crew, c.host)
			}
		})
	}
}

// TestCheck_NilDBAllows documents the bare-unit-path escape hatch: with no
// DB wired the crew-policy layer is absent (the per-path SSRF guard still
// applies). Production callers always pass the DB.
func TestCheck_NilDBAllows(t *testing.T) {
	if err := Check(context.Background(), nil, "crew_restricted", "evil.example.com"); err != nil {
		t.Errorf("Check(nil db) = %v, want allow", err)
	}
}
