package database

import (
	"database/sql"
	"testing"
)

// columnInfo reports whether table has a column with the given name and, if so,
// its declared default (nil when the column has none).
func columnInfo(t *testing.T, db *sql.DB, table, column string) (bool, *string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid   int
			name  string
			ctype string
			nn    int
			d     *string
			pk    int
		)
		if err := rows.Scan(&cid, &name, &ctype, &nn, &d, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true, d
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows table_info(%s): %v", table, err)
	}
	return false, nil
}

// TestMigrate_V165_LeaseProvenanceColumns asserts agent_credentials gained the
// three provenance columns that explain WHY a grant is a lease and which
// approval minted it (#1373). All nullable — a pre-migration lease keeps
// working and reports an unknown source.
func TestMigrate_V165_LeaseProvenanceColumns(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	for _, col := range []string{"lease_source", "lease_issued_at", "lease_request_id"} {
		found, def := columnInfo(t, db.DB, "agent_credentials", col)
		if !found {
			t.Errorf("agent_credentials missing %s column after v165", col)
			continue
		}
		if def != nil {
			t.Errorf("%s default = %q, want NULL (a standing grant has no lease provenance)", col, *def)
		}
	}
}

// TestMigrate_V165_AutoLeaseSecondsDefaultsOff asserts the per-workspace
// auto-issuance knob exists and defaults to 0 — auto-lease is opt-in, so every
// pre-migration workspace keeps today's standing-grant behaviour.
func TestMigrate_V165_AutoLeaseSecondsDefaultsOff(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	found, def := columnInfo(t, db.DB, "keeper_governance_settings", "auto_lease_seconds")
	if !found {
		t.Fatal("keeper_governance_settings missing auto_lease_seconds column after v165")
	}
	if def == nil || *def != "0" {
		t.Errorf("auto_lease_seconds default = %v, want 0 (opt-in, default off)", def)
	}
}

// TestMigrate_V165_LeaseIndexExists asserts the partial lease index landed. The
// gate "(expires_at IS NULL OR expires_at > now)" now runs on every boot
// credential resolve and every /keeper/execute injection, so leased rows need
// to be reachable without a full grant-table scan.
func TestMigrate_V165_LeaseIndexExists(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_agent_credentials_lease'`,
	).Scan(&n); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if n != 1 {
		t.Fatalf("idx_agent_credentials_lease present = %d, want 1", n)
	}
}

// TestMigrate_V165_ExistingGrantStaysStanding is the compatibility guard: a
// grant that existed before v165 must still read back as a standing grant
// (expires_at NULL, no provenance), not as an already-lapsed lease. Getting
// this wrong would refuse every legacy credential at injection time.
func TestMigrate_V165_ExistingGrantStaysStanding(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w1')`)
	mustExec(t, db.DB, `INSERT INTO users (id, email) VALUES ('u1','a@example.com')`)
	mustExec(t, db.DB, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag1','ws1','A','a')`)
	mustExec(t, db.DB, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
		VALUES ('cr1','ws1','TOK','enc','API_KEY','CUSTOM_CLI','u1')`)
	mustExec(t, db.DB, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('acg1','ag1','cr1','TOK',0)`)

	var expiresAt, source sql.NullString
	if err := db.QueryRow(
		`SELECT expires_at, lease_source FROM agent_credentials WHERE id='acg1'`,
	).Scan(&expiresAt, &source); err != nil {
		t.Fatalf("read grant: %v", err)
	}
	if expiresAt.Valid {
		t.Errorf("expires_at = %q, want NULL (standing grant)", expiresAt.String)
	}
	if source.Valid {
		t.Errorf("lease_source = %q, want NULL (standing grant has no issuing event)", source.String)
	}
}
