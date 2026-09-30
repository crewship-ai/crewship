package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/retention"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Admin › Data retention. The instance admin ("boss") belongs to ws-old only;
// ws-new is carol's and must be reachable anyway.

type retentionListBody struct {
	Workspaces []struct {
		WorkspaceID   string          `json:"workspace_id"`
		WorkspaceSlug string          `json:"workspace_slug"`
		Windows       map[string]*int `json:"windows"`
	} `json:"workspaces"`
	Defaults           map[string]*int `json:"defaults"`
	DefaultsConfigured []string        `json:"defaults_configured"`
	Keys               []struct {
		Key            string `json:"key"`
		ForeverAllowed bool   `json:"forever_allowed"`
	} `json:"keys"`
	Housekeeping []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"housekeeping"`
}

type retentionPutBody struct {
	Applied               bool   `json:"applied"`
	DryRun                bool   `json:"dry_run"`
	Changed               int    `json:"changed"`
	RowsAffectedNextSweep int64  `json:"rows_affected_next_sweep"`
	PreviewID             string `json:"preview_id"`
	Workspaces            []struct {
		WorkspaceID string `json:"workspace_id"`
		Changes     []struct {
			Key                   string `json:"key"`
			From                  *int   `json:"from"`
			To                    *int   `json:"to"`
			RowsAffectedNextSweep int64  `json:"rows_affected_next_sweep"`
		} `json:"changes"`
	} `json:"workspaces"`
}

func (f *instanceFixture) windows(ws string) retention.Windows {
	f.t.Helper()
	w, err := retention.Load(context.Background(), f.db, ws)
	if err != nil {
		f.t.Fatalf("retention.Load: %v", err)
	}
	return w
}

func TestInstanceRetentionIsForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/retention", ""), http.StatusForbidden, "workspace ADMIN reads")
	wantCode(t, f.do(f.wsAdmin, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":null,"windows":{"inbox_days":30}}`), http.StatusForbidden, "workspace ADMIN writes")
	if f.windows("ws-old")[retention.Inbox] != nil {
		t.Fatal("a refused save still wrote")
	}
}

func TestInstanceRetentionListsEveryWorkspaceWithDefaultsAndHousekeeping(t *testing.T) {
	f := newInstanceFixture(t)
	mustExec(t, f.db, `UPDATE workspaces SET run_retention_days = 30 WHERE id = 'ws-new'`)
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/retention", "")
	wantCode(t, rr, http.StatusOK, "list")
	body := decodeAs[retentionListBody](t, rr.Body.Bytes())
	if len(body.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want both", len(body.Workspaces))
	}
	for _, w := range body.Workspaces {
		if len(w.Windows) != len(retention.Keys) {
			t.Fatalf("%s has %d windows, want %d", w.WorkspaceID, len(w.Windows), len(retention.Keys))
		}
		for _, k := range []string{"inbox_days", "chats_days", "keeper_decisions_days", "audit_days"} {
			if v, ok := w.Windows[k]; !ok || v != nil {
				t.Fatalf("%s %s = %v, want null (forever) after an upgrade", w.WorkspaceID, k, v)
			}
		}
		want := 90
		if w.WorkspaceID == "ws-new" {
			want = 30
		}
		if got := w.Windows["routine_runs_days"]; got == nil || *got != want {
			t.Fatalf("%s routine_runs_days = %v, want %d", w.WorkspaceID, got, want)
		}
	}
	if v := body.Defaults["approvals_days"]; v == nil || *v != 90 || len(body.DefaultsConfigured) != 0 {
		t.Fatalf("defaults = %v configured %v", body.Defaults, body.DefaultsConfigured)
	}
	if len(body.Keys) != len(retention.Keys) || len(body.Housekeeping) == 0 {
		t.Fatalf("keys %d housekeeping %d", len(body.Keys), len(body.Housekeeping))
	}

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/retention?ws=ws-new", "")
	wantCode(t, rr, http.StatusOK, "filtered")
	if got := decodeAs[retentionListBody](t, rr.Body.Bytes()); len(got.Workspaces) != 1 || got.Workspaces[0].WorkspaceID != "ws-new" {
		t.Fatalf("?ws=ws-new returned %+v", got.Workspaces)
	}
	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/retention?ws=ws-new&ws=ws-old", "")
	if got := decodeAs[retentionListBody](t, rr.Body.Bytes()); len(got.Workspaces) != 2 {
		t.Fatalf("repeated ws returned %d", len(got.Workspaces))
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/retention?ws=nope", ""), http.StatusNotFound, "unknown workspace")
}

func TestInstanceRetentionRefusesInvalidSaves(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"not json", `nope`, http.StatusBadRequest},
		{"no workspace_ids", `{"windows":{"inbox_days":30}}`, http.StatusBadRequest},
		{"empty workspace_ids", `{"workspace_ids":[],"windows":{"inbox_days":30}}`, http.StatusBadRequest},
		{"nothing to change", `{"workspace_ids":null,"windows":{}}`, http.StatusBadRequest},
		{"unknown window", `{"workspace_ids":null,"windows":{"forever_days":3}}`, http.StatusBadRequest},
		{"zero days", `{"workspace_ids":null,"windows":{"inbox_days":0}}`, http.StatusBadRequest},
		{"too many days", `{"workspace_ids":null,"windows":{"inbox_days":3651}}`, http.StatusBadRequest},
		{"fraction", `{"workspace_ids":null,"windows":{"inbox_days":2.5}}`, http.StatusBadRequest},
		{"runs cannot be forever", `{"workspace_ids":null,"windows":{"routine_runs_days":null}}`, http.StatusBadRequest},
		{"unknown workspace", `{"workspace_ids":["nope"],"windows":{"inbox_days":30}}`, http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", c.body), c.want, c.name)
		})
	}
	if n := retentionRows(t, f, `SELECT COUNT(*) FROM retention_settings`); n != 0 {
		t.Fatalf("refused saves wrote %d rows", n)
	}
}

func retentionRows(t *testing.T, f *instanceFixture, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%v: %s", err, q)
	}
	return n
}

func TestInstanceRetentionDryRunCountsWhatTheNextSweepDeletes(t *testing.T) {
	f := newInstanceFixture(t)
	old := tsformat.Format(time.Now().Add(-40 * 24 * time.Hour).UTC())
	mustExec(t, f.db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, blocking, resolved_at)
		VALUES ('i1','ws-new','escalation','s1','t','resolved',1,?), ('i2','ws-new','escalation','s2','t','unread',1,NULL), ('i3','ws-old','escalation','s3','t','resolved',0,?)`, old, old)
	mustExec(t, f.db, `INSERT INTO audit_logs (id, workspace_id, action, entity_type, created_at) VALUES ('a1','ws-new','x','y',?)`, old)

	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":["ws-new"],"dry_run":true,"windows":{"inbox_days":30,"audit_days":30,"chats_days":null}}`)
	wantCode(t, rr, http.StatusOK, "dry run")
	got := decodeAs[retentionPutBody](t, rr.Body.Bytes())
	if got.Applied || !got.DryRun || got.Changed != 1 || len(got.Workspaces) != 1 || got.PreviewID == "" {
		t.Fatalf("dry run = %+v", got)
	}
	changes := got.Workspaces[0].Changes
	// chats_days is already forever: no change for it.
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want inbox and audit", changes)
	}
	for _, c := range changes {
		if c.From != nil || c.To == nil || *c.To != 30 || c.RowsAffectedNextSweep != 1 {
			t.Fatalf("change %+v, want forever → 30 with 1 row", c)
		}
	}
	if got.RowsAffectedNextSweep != 2 {
		t.Fatalf("total rows = %d, want 2", got.RowsAffectedNextSweep)
	}
	if f.windows("ws-new")[retention.Inbox] != nil || retentionRows(t, f, `SELECT COUNT(*) FROM instance_audit_logs WHERE action LIKE 'instance.retention%'`) != 0 {
		t.Fatal("a dry run wrote")
	}
	if retentionRows(t, f, `SELECT COUNT(*) FROM inbox_items`) != 3 {
		t.Fatal("a dry run deleted")
	}
}

func TestInstanceRetentionSavesOneWorkspaceTheAdminIsNotIn(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":["ws-new"],"windows":{"keeper_decisions_days":180,"approvals_days":null,"memory_versions_days":14}}`)
	wantCode(t, rr, http.StatusOK, "save")
	got := decodeAs[retentionPutBody](t, rr.Body.Bytes())
	if !got.Applied || got.Changed != 1 {
		t.Fatalf("save = %+v", got)
	}
	w := f.windows("ws-new")
	if w[retention.KeeperDecisions] == nil || *w[retention.KeeperDecisions] != 180 || w[retention.Approvals] != nil || *w[retention.MemoryVersions] != 14 {
		t.Fatalf("ws-new windows after save: keeper %v approvals %v memory %v", w[retention.KeeperDecisions], w[retention.Approvals], w[retention.MemoryVersions])
	}
	if f.windows("ws-old")[retention.KeeperDecisions] != nil {
		t.Fatal("a one-workspace save touched ws-old")
	}
	if retentionRows(t, f, `SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.retention_updated' AND target_workspace_id = 'ws-new'`) != 1 {
		t.Fatal("no audit entry for ws-new")
	}
	if retentionRows(t, f, `SELECT COUNT(*) FROM retention_settings WHERE workspace_id = 'ws-new' AND updated_by = 'boss'`) != 1 {
		t.Fatal("retention_settings does not name who set it")
	}
	// Saving the same again changes nothing and audits nothing.
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":["ws-new"],"windows":{"keeper_decisions_days":180}}`)
	if got := decodeAs[retentionPutBody](t, rr.Body.Bytes()); got.Changed != 0 {
		t.Fatalf("repeat save changed %d", got.Changed)
	}
	if retentionRows(t, f, `SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.retention_updated'`) != 1 {
		t.Fatal("a no-op save was audited")
	}
}

// "All workspaces" is a selection, never a defaults change: every existing
// workspace changes, and a workspace created afterwards does not inherit it.
func TestInstanceRetentionSaveForAllLeavesTheDefaultsAlone(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":null,"windows":{"inbox_days":90,"routine_runs_days":45}}`)
	wantCode(t, rr, http.StatusOK, "save for all")
	got := decodeAs[retentionPutBody](t, rr.Body.Bytes())
	if !got.Applied || got.Changed != 2 {
		t.Fatalf("save for all = %+v", got)
	}
	if strings.Contains(rr.Body.String(), "defaults_updated") {
		t.Fatalf("the workspace save still speaks of defaults: %s", rr.Body.String())
	}
	if f.audited("instance.retention_defaults_updated") || retentionRows(t, f, `SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.retention_updated'`) != 2 {
		t.Fatal("want one workspace audit entry per workspace and no defaults entry")
	}
	if retentionRows(t, f, `SELECT COUNT(*) FROM app_settings WHERE key = ?`, retention.DefaultsSettingKey) != 0 {
		t.Fatal("a save for all workspaces wrote the defaults")
	}
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/workspaces", `{"name":"Ops","slug":"ops","owner_user_id":"carol"}`)
	wantCode(t, rr, http.StatusCreated, "create workspace")
	var id string
	if err := f.db.QueryRow(`SELECT id FROM workspaces WHERE slug = 'ops'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := f.windows(id); w[retention.Inbox] != nil || *w[retention.RoutineRuns] != 90 {
		t.Fatalf("new workspace inherited a workspace save: inbox %v runs %v", w[retention.Inbox], *w[retention.RoutineRuns])
	}
}

type retentionDefaultsBody struct {
	Applied         bool            `json:"applied"`
	DryRun          bool            `json:"dry_run"`
	AffectsExisting bool            `json:"affects_existing"`
	Defaults        map[string]*int `json:"defaults"`
	Configured      []string        `json:"configured"`
	Changes         []struct {
		Key  string `json:"key"`
		From *int   `json:"from"`
		To   *int   `json:"to"`
	} `json:"changes"`
}

func TestInstanceRetentionDefaultsAreTheirOwnOperation(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/retention/defaults", ""), http.StatusForbidden, "workspace ADMIN reads defaults")
	wantCode(t, f.do(f.wsAdmin, "PUT", "/api/v1/admin/instance/retention/defaults", `{"windows":{"inbox_days":30}}`), http.StatusForbidden, "workspace ADMIN writes defaults")

	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/retention/defaults", "")
	wantCode(t, rr, http.StatusOK, "get defaults")
	got := decodeAs[retentionDefaultsBody](t, rr.Body.Bytes())
	if len(got.Defaults) != len(retention.Keys) || len(got.Configured) != 0 || got.Defaults["inbox_days"] != nil || *got.Defaults["routine_runs_days"] != 90 {
		t.Fatalf("defaults before any save = %s", rr.Body.String())
	}

	for _, c := range []struct{ name, body string }{
		{"not json", `nope`},
		{"nothing to change", `{"windows":{}}`},
		{"unknown window", `{"windows":{"forever_days":3}}`},
		{"runs cannot be forever", `{"windows":{"routine_runs_days":null}}`},
		{"too many days", `{"windows":{"inbox_days":9999}}`},
	} {
		wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/retention/defaults", c.body), http.StatusBadRequest, c.name)
	}

	body := `{"windows":{"inbox_days":60,"chats_days":null,"routine_runs_days":45}}`
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/retention/defaults", `{"dry_run":true,`+body[1:])
	wantCode(t, rr, http.StatusOK, "dry run")
	got = decodeAs[retentionDefaultsBody](t, rr.Body.Bytes())
	// chats_days is already forever: not a change.
	if got.Applied || !got.DryRun || got.AffectsExisting || len(got.Changes) != 2 || *got.Defaults["inbox_days"] != 60 {
		t.Fatalf("dry run = %s", rr.Body.String())
	}
	if retentionRows(t, f, `SELECT COUNT(*) FROM app_settings WHERE key = ?`, retention.DefaultsSettingKey) != 0 || f.audited("instance.retention_defaults_updated") {
		t.Fatal("a dry run wrote")
	}

	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/retention/defaults", body)
	wantCode(t, rr, http.StatusOK, "save defaults")
	got = decodeAs[retentionDefaultsBody](t, rr.Body.Bytes())
	if !got.Applied || got.AffectsExisting || len(got.Changes) != 2 {
		t.Fatalf("save = %s", rr.Body.String())
	}
	if !f.audited("instance.retention_defaults_updated") || f.audited("instance.retention_updated") {
		t.Fatal("want the defaults audit entry and no workspace entry")
	}
	for _, ws := range []string{"ws-old", "ws-new"} {
		if w := f.windows(ws); w[retention.Inbox] != nil || *w[retention.RoutineRuns] != 90 {
			t.Fatalf("saving defaults changed existing workspace %s", ws)
		}
	}
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/workspaces", `{"name":"Ops","slug":"ops","owner_user_id":"carol"}`)
	wantCode(t, rr, http.StatusCreated, "create workspace")
	var id string
	if err := f.db.QueryRow(`SELECT id FROM workspaces WHERE slug = 'ops'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := f.windows(id); w[retention.Inbox] == nil || *w[retention.Inbox] != 60 || *w[retention.RoutineRuns] != 45 || w[retention.Chats] != nil {
		t.Fatalf("new workspace: inbox %v runs %v chats %v", w[retention.Inbox], *w[retention.RoutineRuns], w[retention.Chats])
	}
	got = decodeAs[retentionDefaultsBody](t, f.do(f.boss, "GET", "/api/v1/admin/instance/retention/defaults", "").Body.Bytes())
	if len(got.Configured) != 2 || *got.Defaults["inbox_days"] != 60 {
		t.Fatalf("defaults after save = %v configured %v", got.Defaults, got.Configured)
	}
	list := decodeAs[retentionListBody](t, f.do(f.boss, "GET", "/api/v1/admin/instance/retention", "").Body.Bytes())
	if len(list.DefaultsConfigured) != 2 || *list.Defaults["inbox_days"] != 60 {
		t.Fatalf("GET /retention defaults = %v configured %v", list.Defaults, list.DefaultsConfigured)
	}
	// The same save again is no change and no audit entry.
	got = decodeAs[retentionDefaultsBody](t, f.do(f.boss, "PUT", "/api/v1/admin/instance/retention/defaults", body).Body.Bytes())
	if len(got.Changes) != 0 || got.Applied || retentionRows(t, f, `SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.retention_defaults_updated'`) != 1 {
		t.Fatalf("repeat save = %+v", got)
	}
}

// A save without a preview is allowed from the API; the console always
// previews, and expect_preview is how it holds the save to what it showed.
func TestInstanceRetentionSavesWithoutAPreview(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":["ws-old","ws-new"],"windows":{"chats_days":120,"inbox_days":30}}`)
	wantCode(t, rr, http.StatusOK, "save without preview")
	got := decodeAs[retentionPutBody](t, rr.Body.Bytes())
	if !got.Applied || len(got.Workspaces) != 2 {
		t.Fatalf("save = %+v", got)
	}
	for _, w := range got.Workspaces {
		if len(w.Changes) != 2 || w.Changes[0].Key != "inbox_days" || w.Changes[1].Key != "chats_days" {
			t.Fatalf("%s changes = %+v, want one per window in display order", w.WorkspaceID, w.Changes)
		}
	}
}

func TestInstanceRetentionConfirmsTheSavePreviewed(t *testing.T) {
	f := newInstanceFixture(t)
	body := `{"workspace_ids":null,"dry_run":true,"windows":{"inbox_days":30}}`
	preview := decodeAs[retentionPutBody](t, f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", body).Body.Bytes())
	// Someone else changes ws-new meanwhile.
	mustExec(t, f.db, `INSERT INTO retention_settings (workspace_id, key, days) VALUES ('ws-new', 'inbox_days', 7)`)
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":null,"expect_preview":"`+preview.PreviewID+`","windows":{"inbox_days":30}}`)
	wantCode(t, rr, http.StatusConflict, "stale preview")
	if w := f.windows("ws-old"); w[retention.Inbox] != nil {
		t.Fatal("a refused save wrote ws-old")
	}
	fresh := decodeAs[retentionPutBody](t, f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", body).Body.Bytes())
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/retention", `{"workspace_ids":null,"expect_preview":"`+fresh.PreviewID+`","windows":{"inbox_days":30}}`)
	wantCode(t, rr, http.StatusOK, "fresh preview")
}

// retention cannot import internal/api, so it carries its own copy of the
// credential audit default; this keeps the two from drifting apart.
func TestCredentialAuditDefaultMatchesTheSweep(t *testing.T) {
	if retention.DefaultCredentialAuditDays != DefaultCredentialAuditRetentionDays {
		t.Fatalf("retention says %d, the sweep uses %d", retention.DefaultCredentialAuditDays, DefaultCredentialAuditRetentionDays)
	}
}
