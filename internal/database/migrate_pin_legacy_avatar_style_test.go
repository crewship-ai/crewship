package database

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
)

// #3789 moves the default avatar style from bottts-neutral to voxel-bot. The
// web app draws an agent with no style of its own, in a crew with no style,
// with the DEFAULT — so without this migration every such agent would change
// face on upgrade, on every surface, because many of them have no stored
// render (the stored-avatar rollout is lazy) and most surfaces draw from
// (seed, style) anyway.
//
// The fix pins exactly that population to the old default. Everything else
// keeps what it has: an agent with its own style, and an agent that follows a
// crew which has a style, already resolve to something other than the default.

const pinLegacyAvatarStyleName = "pin_legacy_avatar_style"

func pinLegacyAvatarStyleMigration(t *testing.T) migration {
	t.Helper()
	for _, m := range migrations {
		if m.name == pinLegacyAvatarStyleName {
			return m
		}
	}
	t.Fatalf("no %q migration: every existing agent with no avatar style would be redrawn as Voxel Bot", pinLegacyAvatarStyleName)
	return migration{}
}

func seedAvatarRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws-1', 'W', 'w')`,
		`INSERT INTO crews (id, workspace_id, name, slug, avatar_style) VALUES ('crew-plain', 'ws-1', 'Plain', 'plain', NULL)`,
		`INSERT INTO crews (id, workspace_id, name, slug, avatar_style) VALUES ('crew-empty', 'ws-1', 'Empty', 'empty', '')`,
		`INSERT INTO crews (id, workspace_id, name, slug, avatar_style) VALUES ('crew-styled', 'ws-1', 'Styled', 'styled', 'lorelei')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	agents := []struct {
		id, crew string
		style    any
		deleted  any
	}{
		{"null-in-plain", "crew-plain", nil, nil},
		{"empty-in-plain", "crew-plain", "", nil},
		{"null-in-empty-crew", "crew-empty", nil, nil},
		{"null-no-crew", "", nil, nil},
		{"null-deleted", "crew-plain", nil, "2026-01-01T00:00:00Z"},
		{"own-style", "crew-plain", "thumbs", nil},
		{"follows-styled-crew", "crew-styled", nil, nil},
		{"own-in-styled-crew", "crew-styled", "micah", nil},
	}
	for _, a := range agents {
		var crew any = a.crew
		if a.crew == "" {
			crew = nil
		}
		if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, avatar_style, deleted_at)
			VALUES (?, 'ws-1', ?, ?, ?, 'AGENT', ?, ?)`, a.id, crew, a.id, a.id, a.style, a.deleted); err != nil {
			t.Fatalf("seed agent %s: %v", a.id, err)
		}
	}
}

func agentStyles(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, COALESCE(avatar_style, '<null>') FROM agents`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, style string
		if err := rows.Scan(&id, &style); err != nil {
			t.Fatal(err)
		}
		out[id] = style
	}
	return out
}

var wantPinnedStyles = map[string]string{
	// Resolved to the default before #3789: pinned to what that default was.
	"null-in-plain":      "bottts-neutral",
	"empty-in-plain":     "bottts-neutral",
	"null-in-empty-crew": "bottts-neutral",
	"null-no-crew":       "bottts-neutral",
	// History views still draw deleted agents.
	"null-deleted": "bottts-neutral",
	// Did not resolve to the default: untouched.
	"own-style":           "thumbs",
	"follows-styled-crew": "<null>",
	"own-in-styled-crew":  "micah",
}

func TestPinLegacyAvatarStyle(t *testing.T) {
	m := pinLegacyAvatarStyleMigration(t)
	db := openMigratedTestDB(t).DB
	seedAvatarRows(t, db)

	if _, err := db.Exec(m.sql); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := agentStyles(t, db)
	for id, want := range wantPinnedStyles {
		if got[id] != want {
			t.Errorf("%s avatar_style = %q, want %q", id, got[id], want)
		}
	}

	// Crews are left alone: a crew with no style is how a NEW agent in it
	// gets the new default.
	var plain sql.NullString
	if err := db.QueryRow(`SELECT avatar_style FROM crews WHERE id = 'crew-plain'`).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain.Valid {
		t.Errorf("crew-plain avatar_style = %q, want NULL", plain.String)
	}
}

func TestPinLegacyAvatarStyle_LeavesStoredRendersAlone(t *testing.T) {
	m := pinLegacyAvatarStyleMigration(t)
	db := openMigratedTestDB(t).DB
	seedAvatarRows(t, db)
	if _, err := db.Exec(`UPDATE agents SET avatar_svg = '<svg/>', avatar_svg_hash = 'h' WHERE id = 'null-in-plain'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(m.sql); err != nil {
		t.Fatal(err)
	}
	var hash sql.NullString
	if err := db.QueryRow(`SELECT avatar_svg_hash FROM agents WHERE id = 'null-in-plain'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash.String != "h" {
		t.Errorf("the stored render was dropped (hash %q): it depicts exactly the pinned style", hash.String)
	}
}

// A backup taken before this migration and restored into a build that has it
// brings back un-pinned rows; the restore hook pins them the same way.
func TestPinLegacyAvatarStyle_RestoreBackfill(t *testing.T) {
	m := pinLegacyAvatarStyleMigration(t)
	if m.restoreBackfill == nil {
		t.Fatal("no restore backfill: restoring an older backup would redraw its agents as Voxel Bot")
	}
	db := openMigratedTestDB(t).DB
	seedAvatarRows(t, db)
	for pass := 0; pass < 2; pass++ { // idempotent: the restore path may replay it
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.restoreBackfill(context.Background(), tx, slog.Default()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	got := agentStyles(t, db)
	for id, want := range wantPinnedStyles {
		if got[id] != want {
			t.Errorf("%s avatar_style = %q, want %q", id, got[id], want)
		}
	}
}

// After the migration, a new agent with no style in a crew with no style is
// NOT pinned — it is what gets the new default.
func TestPinLegacyAvatarStyle_RunsOnceOnUpgrade(t *testing.T) {
	db := openMigratedTestDB(t).DB
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-1', 'W', 'w')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, agent_role) VALUES ('new', 'ws-1', 'N', 'n', 'AGENT')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var style sql.NullString
	if err := db.QueryRow(`SELECT avatar_style FROM agents WHERE id = 'new'`).Scan(&style); err != nil {
		t.Fatal(err)
	}
	if style.Valid {
		t.Errorf("a re-run of Migrate pinned an agent created after the upgrade to %q", style.String)
	}
}
