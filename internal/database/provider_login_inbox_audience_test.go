package database

import (
	"database/sql"
	"testing"
)

func TestProviderLoginInboxAudience_Backfill(t *testing.T) {
	t.Parallel()
	var migrationSQL string
	for _, m := range migrations {
		if m.name == "provider_login_inbox_audience" {
			migrationSQL = m.sql
		}
	}
	if migrationSQL == "" {
		t.Fatal("provider login inbox audience migration is not registered")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE inbox_items (id TEXT PRIMARY KEY, kind TEXT, sender_type TEXT, source_id TEXT, target_user_id TEXT, target_role TEXT, state TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, sender, source, user, role, state, wantUser, wantRole string
	}{
		{"unread", "system", "provider-login-relogin:a", "creator", "", "unread", "", "ADMIN"},
		{"read", "system", "provider-login-relogin:b", "creator", "", "read", "", "ADMIN"},
		{"resolved", "system", "provider-login-relogin:c", "creator", "", "resolved", "", "ADMIN"},
		{"manager", "system", "provider-login-relogin:d", "", "MANAGER", "unread", "", "ADMIN"},
		{"owner", "system", "provider-login-relogin:e", "creator", "OWNER", "unread", "", "OWNER"},
		{"other", "system", "routine-failure:a", "creator", "MANAGER", "unread", "creator", "MANAGER"},
		{"human", "user", "provider-login-relogin:f", "creator", "", "unread", "creator", ""},
	}
	for _, tc := range cases {
		if _, err := db.Exec(`INSERT INTO inbox_items VALUES (?, 'message', ?, ?, ?, ?, ?, 'before')`, tc.id, tc.sender, tc.source, tc.user, tc.role, tc.state); err != nil {
			t.Fatal(err)
		}
	}
	// Reapplying the data backfill is harmless; no rows or their state are lost.
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(migrationSQL); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range cases {
		var user, role, state string
		if err := db.QueryRow(`SELECT COALESCE(target_user_id, ''), target_role, state FROM inbox_items WHERE id = ?`, tc.id).Scan(&user, &role, &state); err != nil {
			t.Fatal(err)
		}
		if user != tc.wantUser || role != tc.wantRole || state != tc.state {
			t.Errorf("%s: audience=(%q,%q), state=%q; want (%q,%q), %q", tc.id, user, role, state, tc.wantUser, tc.wantRole, tc.state)
		}
	}
}
