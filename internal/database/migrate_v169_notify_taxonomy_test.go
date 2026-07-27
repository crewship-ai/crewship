package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// v161EraPrefsDDL reproduces the schema v169 has to migrate FROM, verbatim
// (including indentation — widenPrefsCategoryCheck matches the CHECK text as a
// literal substring, so a reflowed copy here would silently exercise the
// "already migrated, nothing to do" path and prove nothing).
const v161EraPrefsDDL = `CREATE TABLE IF NOT EXISTS user_notification_prefs (
		    id            TEXT PRIMARY KEY,
		    workspace_id  TEXT NOT NULL,
		    user_id       TEXT NOT NULL,
		    category      TEXT NOT NULL CHECK (category IN (
		        'approvals','escalations','runs.failed','runs.completed',
		        'chat.replies','security','budget','system','memory','*'
		    )),
		    channel_id    TEXT NOT NULL,
		    state         TEXT NOT NULL DEFAULT 'off' CHECK (state IN ('off','immediate','digest')),
		    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    UNIQUE(user_id, category, channel_id)
		)`

const v161EraChannelsDDL = `CREATE TABLE IF NOT EXISTS notification_channels (
    id              TEXT PRIMARY KEY,
    categories_json TEXT NOT NULL DEFAULT '[]',
    updated_at      TEXT
)`

// openV169FixtureDB builds a database carrying the pre-taxonomy-v2 shape and
// data, ready for migrationNotifyTaxonomy to run against.
func openV169FixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/v169.db?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, ddl := range []string{v161EraPrefsDDL, v161EraChannelsDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("fixture ddl: %v", err)
		}
	}
	return db
}

func runV169(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrationNotifyTaxonomy(context.Background(), tx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		_ = tx.Rollback()
		t.Fatalf("migrationNotifyTaxonomy: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestV169_RemapsPreferencesWithoutLosingIntent is the core data-preservation
// contract. A user who switched a category on must still have it on after the
// rename, and one who muted must stay muted — the migration rewrites, it never
// drops. 'system' had no producer under the old vocabulary, so it splits into
// both new system categories rather than silently discarding half the intent.
func TestV169_RemapsPreferencesWithoutLosingIntent(t *testing.T) {
	db := openV169FixtureDB(t)

	seed := []struct{ id, category, state string }{
		{"p1", "approvals", "immediate"},
		{"p2", "escalations", "immediate"},
		{"p3", "runs.failed", "immediate"},
		{"p4", "runs.completed", "off"},
		{"p5", "budget", "immediate"},
		{"p6", "system", "immediate"},
		{"p7", "chat.replies", "immediate"},
		{"p8", "security", "immediate"},
		{"p9", "memory", "off"},
		{"p10", "*", "immediate"}, // the mute-all sentinel must survive untouched
	}
	for _, s := range seed {
		if _, err := db.Exec(
			`INSERT INTO user_notification_prefs (id, workspace_id, user_id, category, channel_id, state)
			 VALUES (?, 'ws1', 'u1', ?, 'nch1', ?)`, s.id, s.category, s.state); err != nil {
			t.Fatalf("seed %s: %v", s.id, err)
		}
	}

	runV169(t, db)

	got := map[string]string{} // category -> state
	rows, err := db.Query(`SELECT category, state FROM user_notification_prefs`)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c, s string
		if err := rows.Scan(&c, &s); err != nil {
			t.Fatal(err)
		}
		if _, dup := got[c]; dup {
			t.Errorf("category %q appears twice for the same (user, channel)", c)
		}
		got[c] = s
	}

	want := map[string]string{
		notify.CategoryAgentsApproval:    "immediate",
		notify.CategoryAgentsEscalation:  "immediate",
		notify.CategoryRoutinesFailed:    "immediate",
		notify.CategoryRoutinesCompleted: "off",
		notify.CategoryAgentsBudget:      "immediate",
		// 'system' → both, each inheriting the original state.
		notify.CategorySystemHealth:    "immediate",
		notify.CategorySystemMigration: "immediate",
		notify.CategoryChatReplies:     "immediate",
		notify.CategorySecurity:        "immediate",
		notify.CategoryMemory:          "off",
		notify.CategoryMuteAll:         "immediate",
	}
	for cat, wantState := range want {
		gotState, ok := got[cat]
		if !ok {
			t.Errorf("category %q is missing after the migration — a user's preference was lost", cat)
			continue
		}
		if gotState != wantState {
			t.Errorf("category %q state = %q, want %q — the migration changed what the user chose", cat, gotState, wantState)
		}
	}
	for cat := range got {
		if _, expected := want[cat]; !expected {
			t.Errorf("unexpected category %q survived the migration", cat)
		}
	}
}

// TestV169_WidenedCheckAcceptsNewVocabulary proves the CHECK was actually
// rewritten, not merely that the UPDATEs ran: a fresh insert on a new-only
// category must be accepted, and a retired name must now be rejected.
func TestV169_WidenedCheckAcceptsNewVocabulary(t *testing.T) {
	db := openV169FixtureDB(t)
	runV169(t, db)

	for _, c := range notify.AllCategories {
		if _, err := db.Exec(
			`INSERT INTO user_notification_prefs (id, workspace_id, user_id, category, channel_id, state)
			 VALUES (?, 'ws1', 'u2', ?, 'nch1', 'immediate')`, "new_"+c, c); err != nil {
			t.Errorf("category %q rejected by the widened CHECK: %v", c, err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO user_notification_prefs (id, workspace_id, user_id, category, channel_id, state)
		 VALUES ('retired', 'ws1', 'u3', 'runs.failed', 'nch1', 'immediate')`); err == nil {
		t.Error("the retired category name 'runs.failed' is still accepted — the CHECK was not tightened")
	}
}

// TestV169_RemapsChannelAllowlists covers the admin per-channel allowlist.
// An empty list means "every category" and must stay empty; a list mentioning
// a split category must gain both halves; duplicates must collapse.
func TestV169_RemapsChannelAllowlists(t *testing.T) {
	db := openV169FixtureDB(t)
	seed := []struct{ id, cats string }{
		{"ch_empty", `[]`},
		{"ch_simple", `["approvals","escalations"]`},
		{"ch_split", `["system"]`},
		{"ch_collapse", `["security","security"]`},
		{"ch_mixed", `["runs.failed","memory"]`},
	}
	for _, s := range seed {
		if _, err := db.Exec(`INSERT INTO notification_channels (id, categories_json) VALUES (?, ?)`, s.id, s.cats); err != nil {
			t.Fatalf("seed %s: %v", s.id, err)
		}
	}

	runV169(t, db)

	want := map[string][]string{
		"ch_empty":    {},
		"ch_simple":   {notify.CategoryAgentsApproval, notify.CategoryAgentsEscalation},
		"ch_split":    {notify.CategorySystemHealth, notify.CategorySystemMigration},
		"ch_collapse": {notify.CategorySecurity},
		"ch_mixed":    {notify.CategoryRoutinesFailed, notify.CategoryMemory},
	}
	for id, wantCats := range want {
		var raw string
		if err := db.QueryRow(`SELECT categories_json FROM notification_channels WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		var got []string
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("%s: allowlist is not JSON: %v", id, err)
		}
		sort.Strings(got)
		sorted := append([]string(nil), wantCats...)
		sort.Strings(sorted)
		if len(got) != len(sorted) {
			t.Errorf("%s allowlist = %v, want %v", id, got, sorted)
			continue
		}
		for i := range got {
			if got[i] != sorted[i] {
				t.Errorf("%s allowlist = %v, want %v", id, got, sorted)
				break
			}
		}
	}
}

// TestV169_IsIdempotent pins that a re-apply (a restore, a re-run after a
// partial failure) neither double-splits 'system' nor errors on the UNIQUE
// constraint.
func TestV169_IsIdempotent(t *testing.T) {
	db := openV169FixtureDB(t)
	if _, err := db.Exec(
		`INSERT INTO user_notification_prefs (id, workspace_id, user_id, category, channel_id, state)
		 VALUES ('p1', 'ws1', 'u1', 'system', 'nch1', 'immediate')`); err != nil {
		t.Fatal(err)
	}
	runV169(t, db)
	runV169(t, db)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_notification_prefs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("after two applies there are %d pref rows, want 2 (system → health + migration, once)", n)
	}
}

// TestV169_LeavesUnknownCategoriesAlone guards the escape hatch: a value from
// neither vocabulary is inert, and rewriting or dropping it on a guess is
// worse than leaving a dead entry.
//
// Exercised through the channel allowlist rather than the prefs table:
// categories_json is free-form TEXT, so an unknown value is actually
// reachable there, whereas user_notification_prefs.category has a CHECK that
// makes one unreachable by construction.
func TestV169_LeavesUnknownCategoriesAlone(t *testing.T) {
	db := openV169FixtureDB(t)
	if _, err := db.Exec(
		`INSERT INTO notification_channels (id, categories_json) VALUES ('ch_odd', '["approvals","weird.thing"]')`); err != nil {
		t.Fatal(err)
	}
	// Corrupt JSON must not fail the whole migration — the reader already
	// treats it as "all categories" rather than erroring.
	if _, err := db.Exec(
		`INSERT INTO notification_channels (id, categories_json) VALUES ('ch_broken', 'not json at all')`); err != nil {
		t.Fatal(err)
	}

	runV169(t, db)

	var raw string
	if err := db.QueryRow(`SELECT categories_json FROM notification_channels WHERE id = 'ch_odd'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("allowlist is no longer JSON: %v", err)
	}
	want := []string{notify.CategoryAgentsApproval, "weird.thing"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("allowlist = %v, want %v — the known name should remap and the unknown one survive", got, want)
	}

	if err := db.QueryRow(`SELECT categories_json FROM notification_channels WHERE id = 'ch_broken'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "not json at all" {
		t.Errorf("unparseable allowlist was rewritten to %q; it should have been left untouched", raw)
	}
}
