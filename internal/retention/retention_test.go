package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	exec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'One', 'one'), ('ws2', 'Two', 'two')`)
	return db
}

func exec(t *testing.T, db DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return n
}

func ago(d int) string {
	return tsformat.Format(time.Now().Add(-time.Duration(d) * 24 * time.Hour).UTC())
}

func val(p *int) any {
	if p == nil {
		return "forever"
	}
	return *p
}

func TestLoadTranslatesEachColumnConvention(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want map[Key]any
	}{
		{name: "nothing configured is the product default",
			set: `UPDATE workspaces SET memory_config = NULL WHERE id = 'ws1'`,
			want: map[Key]any{RoutineRuns: 90, Approvals: 90, Audit: "forever", CredentialAudit: 90,
				MemoryVersions: 30, PagePanelData: 7, Inbox: "forever", Chats: "forever", KeeperDecisions: "forever"}},
		{name: "explicit zero is forever where the column allows it, the default where it does not",
			set: `UPDATE workspaces SET run_retention_days = 0, approvals_retention_days = 0, audit_log_retention_days = 0,
				credential_audit_retention_days = 0, page_retention_days = 0 WHERE id = 'ws1'`,
			want: map[Key]any{RoutineRuns: 90, Approvals: "forever", Audit: "forever", CredentialAudit: "forever", PagePanelData: 7}},
		{name: "positive values read back",
			set: `UPDATE workspaces SET run_retention_days = 30, approvals_retention_days = 5000, audit_log_retention_days = 400,
				credential_audit_retention_days = 14, page_retention_days = 3, memory_config = '{"versions_retention_days": 2.5, "x": 1}' WHERE id = 'ws1'`,
			want: map[Key]any{RoutineRuns: 30, Approvals: 5000, Audit: 400, CredentialAudit: 14, PagePanelData: 3, MemoryVersions: 3}},
		{name: "corrupt memory config falls back to the default",
			set:  `UPDATE workspaces SET memory_config = '{nope' WHERE id = 'ws1'`,
			want: map[Key]any{MemoryVersions: 30}},
		{name: "retention_settings rows",
			set:  `INSERT INTO retention_settings (workspace_id, key, days) VALUES ('ws1','inbox_days',60), ('ws1','chats_days',NULL), ('ws1','unknown_days',5)`,
			want: map[Key]any{Inbox: 60, Chats: "forever", KeeperDecisions: "forever"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newDB(t)
			exec(t, db, c.set)
			w, err := Load(context.Background(), db, "ws1")
			if err != nil {
				t.Fatal(err)
			}
			if len(w) != len(Keys) {
				t.Fatalf("Load returned %d keys, want all %d", len(w), len(Keys))
			}
			for k, want := range c.want {
				if got := val(w[k]); got != want {
					t.Errorf("%s = %v, want %v", k, got, want)
				}
			}
		})
	}
	if _, err := Load(context.Background(), newDB(t), "nope"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
}

func TestStoreRoundTripsEveryWindow(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	now := time.Now()
	exec(t, db, `UPDATE workspaces SET memory_config = '{"keep": "me"}' WHERE id = 'ws1'`)
	for _, info := range Keys {
		if err := Store(ctx, db, "ws1", info.Key, days(45), "u1", now); err != nil {
			t.Fatalf("store %s: %v", info.Key, err)
		}
		if info.ForeverAllowed {
			if err := Store(ctx, db, "ws2", info.Key, nil, "u1", now); err != nil {
				t.Fatalf("store forever %s: %v", info.Key, err)
			}
		} else if err := Store(ctx, db, "ws2", info.Key, nil, "u1", now); err == nil {
			t.Fatalf("%s accepted forever", info.Key)
		}
	}
	w1, _ := Load(ctx, db, "ws1")
	w2, _ := Load(ctx, db, "ws2")
	for _, info := range Keys {
		if val(w1[info.Key]) != 45 {
			t.Errorf("ws1 %s = %v, want 45", info.Key, val(w1[info.Key]))
		}
		if info.ForeverAllowed && w2[info.Key] != nil {
			t.Errorf("ws2 %s = %v, want forever", info.Key, val(w2[info.Key]))
		}
	}
	var memCfg string
	_ = db.QueryRow(`SELECT memory_config FROM workspaces WHERE id = 'ws1'`).Scan(&memCfg)
	var doc map[string]any
	if err := json.Unmarshal([]byte(memCfg), &doc); err != nil || doc["keep"] != "me" {
		t.Fatalf("memory_config lost its other keys: %s", memCfg)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM retention_settings WHERE workspace_id = 'ws1' AND updated_by = 'u1'`); n != 3 {
		t.Fatalf("retention_settings rows for ws1 = %d, want 3", n)
	}
	// The audit sweep reads 0 as the operator's explicit forever.
	if n := count(t, db, `SELECT audit_log_retention_days FROM workspaces WHERE id = 'ws2'`); n != 0 {
		t.Fatalf("audit forever stored as %d, want 0", n)
	}
}

func TestParsePatch(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		want    map[Key]any
	}{
		{name: "empty", body: `{}`, wantErr: true},
		{name: "unknown key", body: `{"forever_days": 3}`, wantErr: true},
		{name: "fraction", body: `{"inbox_days": 1.5}`, wantErr: true},
		{name: "zero", body: `{"inbox_days": 0}`, wantErr: true},
		{name: "too long", body: `{"inbox_days": 3651}`, wantErr: true},
		{name: "string", body: `{"inbox_days": "30"}`, wantErr: true},
		{name: "forever where it cannot be", body: `{"routine_runs_days": null}`, wantErr: true},
		{name: "forever and days", body: `{"inbox_days": null, "audit_days": 365}`, want: map[Key]any{Inbox: "forever", Audit: 365}},
		{name: "bounds", body: `{"chats_days": 1, "keeper_decisions_days": 3650}`, want: map[Key]any{Chats: 1, KeeperDecisions: 3650}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(c.body), &raw); err != nil {
				t.Fatal(err)
			}
			got, err := ParsePatch(raw)
			if c.wantErr {
				var ve ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want a ValidationError", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d keys, want %d", len(got), len(c.want))
			}
			for k, w := range c.want {
				if val(got[k]) != w {
					t.Errorf("%s = %v, want %v", k, val(got[k]), w)
				}
			}
		})
	}
}

func TestDefaultsReachOnlyNewWorkspacesAndOnlySavedKeys(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	w, configured, err := Defaults(ctx, db)
	if err != nil || len(configured) != 0 || val(w[RoutineRuns]) != 90 || w[Inbox] != nil {
		t.Fatalf("unset defaults = %v %v %v", w, configured, err)
	}
	// Nothing saved: a new workspace gets nothing written.
	exec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws3', 'Three', 'three')`)
	if err := ApplyDefaults(ctx, db, "ws3"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM retention_settings WHERE workspace_id = 'ws3'`); n != 0 {
		t.Fatalf("defaults with nothing saved wrote %d rows", n)
	}
	if err := MergeDefaults(ctx, db, Windows{Inbox: days(30), Audit: nil}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := MergeDefaults(ctx, db, Windows{RoutineRuns: days(14)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	w, configured, _ = Defaults(ctx, db)
	if len(configured) != 3 || val(w[Inbox]) != 30 || w[Audit] != nil || val(w[RoutineRuns]) != 14 || w[Chats] != nil {
		t.Fatalf("defaults = %v configured %v", w, configured)
	}
	exec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws4', 'Four', 'four')`)
	if err := ApplyDefaults(ctx, db, "ws4"); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(ctx, db, "ws4")
	if val(got[Inbox]) != 30 || val(got[RoutineRuns]) != 14 || got[Audit] != nil || got[Chats] != nil || val(got[Approvals]) != 90 {
		t.Fatalf("new workspace windows = inbox %v runs %v audit %v chats %v approvals %v",
			val(got[Inbox]), val(got[RoutineRuns]), val(got[Audit]), val(got[Chats]), val(got[Approvals]))
	}
	// Existing workspaces are untouched by a defaults save.
	if old, _ := Load(ctx, db, "ws1"); old[Inbox] != nil || val(old[RoutineRuns]) != 90 {
		t.Fatal("saving defaults changed an existing workspace")
	}
}

func TestHousekeepingIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range Housekeeping() {
		if h.Key == "" || h.Label == "" || h.Value == "" || h.Detail == "" || seen[h.Key] {
			t.Fatalf("bad housekeeping item %+v", h)
		}
		seen[h.Key] = true
	}
	for _, k := range []string{"journal_compaction", "routine_webhook_receipts", "work_raw_bodies", "page_project_history", "orphaned_attachments", "pre_migration_snapshots"} {
		if !seen[k] {
			t.Errorf("housekeeping misses %s", k)
		}
	}
}
