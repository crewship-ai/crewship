package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// PR4 of #5317: every access a flag-on chat run makes through its scope is
// audited, naming the human the agent acted for. Reads are recorded once per
// run and artifact; writes and creates every time. A failed audit refuses the
// access.

type runAuditRow struct {
	User, Page, Agent, Run, Reach, Operation string
}

func runAuditRows(t *testing.T, g *runScopeRig) []runAuditRow {
	t.Helper()
	rows, err := g.f.rig.db.Query(`SELECT coalesce(user_id,''),coalesce(entity_id,''),coalesce(metadata,'') FROM audit_logs WHERE workspace_id=? AND action=? AND entity_type='page' ORDER BY created_at,id`, g.f.a.WorkspaceID, auditActionAgentRunArtifactAccess)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []runAuditRow
	for rows.Next() {
		var r runAuditRow
		var meta string
		if err := rows.Scan(&r.User, &r.Page, &meta); err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			t.Fatalf("audit metadata %q: %v", meta, err)
		}
		r.Agent, r.Run, r.Reach, r.Operation = m["agent_id"], m["run_id"], m["reach"], m["operation"]
		out = append(out, r)
	}
	return out
}

func (g *runScopeRig) wantAudit(t *testing.T, got runAuditRow, page, reach, operation string) {
	t.Helper()
	want := runAuditRow{User: g.f.a.HumanUserID, Page: page, Agent: g.f.a.AgentID, Run: g.f.a.RunID, Reach: reach, Operation: operation}
	if got != want {
		t.Fatalf("audit row\n got %+v\nwant %+v", got, want)
	}
}

func TestRunAudit_ReadIsRecordedOncePerRunAndArtifact(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-attached")
	g.start(t, id)
	for i := 0; i < 3; i++ {
		if w := g.tool("artifact_versions", "audit-attached", `{}`); w.Code != 200 {
			t.Fatalf("read %d: %d %s", i, w.Code, w.Body)
		}
	}
	rows := runAuditRows(t, g)
	if len(rows) != 1 {
		t.Fatalf("want one read record, got %+v", rows)
	}
	g.wantAudit(t, rows[0], id, "referenced", "read")
}

func TestRunAudit_WritesAndCreatesAreRecordedEachTime(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-write")
	g.start(t, id)
	for v := 1; v <= 2; v++ {
		if w := g.tool("artifact_publish", "audit-write", artifactProjectBody(v, fmt.Sprintf("v%d", v+1))); w.Code != 200 {
			t.Fatalf("publish %d: %d %s", v, w.Code, w.Body)
		}
	}
	body := `{"name":"audit-created",` + strings.TrimPrefix(artifactProjectBody(0, "audit-created"), "{")
	if w := g.tool("artifact_create", "audit-created", body); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created string
	if err := g.f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='audit-created'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	var writes, creates int
	for _, r := range runAuditRows(t, g) {
		switch {
		case r.Page == id && r.Operation == "write":
			g.wantAudit(t, r, id, "referenced", "write")
			writes++
		case r.Page == created && r.Operation == "create":
			g.wantAudit(t, r, created, "created", "create")
			creates++
		}
	}
	if writes != 2 || creates != 1 {
		t.Fatalf("writes=%d creates=%d, want 2 and 1: %+v", writes, creates, runAuditRows(t, g))
	}
}

// The reach kind tells an auditor why the agent could reach the artifact.
func TestRunAudit_NamesTheReach(t *testing.T) {
	g := newRunScopeRig(t)
	standing := g.shared(t, "audit-standing")
	g.grant(t, standing, "agent", g.f.a.AgentID, "read")
	folder := g.shared(t, "audit-folder")
	g.folder(t, "audit", g.f.a.CrewID, "read", folder)
	consented := g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-consented")
	g.start(t)
	mustExec(t, g.f.rig.db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) SELECT run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,expires_at,? FROM artifact_run_principals WHERE run_id=?`, consented, g.f.a.RunID)
	want := map[string]string{standing: "standing", folder: "folder", consented: "consented"}
	for _, slug := range []string{"audit-standing", "audit-folder", "audit-consented"} {
		if w := g.tool("artifact_versions", slug, `{}`); w.Code != 200 {
			t.Fatalf("read %s: %d %s", slug, w.Code, w.Body)
		}
	}
	rows := runAuditRows(t, g)
	if len(rows) != 3 {
		t.Fatalf("want three records, got %+v", rows)
	}
	for _, r := range rows {
		g.wantAudit(t, r, r.Page, want[r.Page], "read")
	}
}

// A refused access leaves no record; the flag off records nothing.
func TestRunAudit_NothingForRefusalsOrWithTheFlagOff(t *testing.T) {
	g := newRunScopeRig(t)
	g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-unreached")
	g.start(t)
	if w := g.tool("artifact_versions", "audit-unreached", `{}`); w.Code == 200 {
		t.Fatal("unreached artifact was read")
	}
	if rows := runAuditRows(t, g); len(rows) != 0 {
		t.Fatalf("a refusal was audited as an access: %+v", rows)
	}

	off := newRunScopeRig(t)
	id := off.create(t, off.f.a.HumanUserID, "MEMBER", "audit-flag-off")
	off.start(t)
	mustExec(t, off.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	mustExec(t, off.f.rig.db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) SELECT run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,expires_at,? FROM artifact_run_principals WHERE run_id=?`, id, off.f.a.RunID)
	if w := off.tool("artifact_versions", "audit-flag-off", `{}`); w.Code != 200 {
		t.Fatalf("flag off consent read: %d %s", w.Code, w.Body)
	}
	if rows := runAuditRows(t, off); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

// Fail closed: when the audit cannot be written, or is silently dropped, the
// access is refused.
func TestRunAudit_FailsClosed(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER run_audit_abort BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
		`CREATE TRIGGER run_audit_ignore BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(strings.Fields(trigger)[2], func(t *testing.T) {
			g := newRunScopeRig(t)
			id := g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-closed")
			g.start(t, id)
			mustExec(t, g.f.rig.db, trigger)
			if w := g.tool("artifact_versions", "audit-closed", `{}`); w.Code == 200 {
				t.Fatal("read succeeded without its audit")
			}
			if w := g.tool("artifact_publish", "audit-closed", artifactProjectBody(1, "Second")); w.Code == 200 {
				t.Fatal("write succeeded without its audit")
			}
			var latest int
			if err := g.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, id).Scan(&latest); err != nil || latest != 1 {
				t.Fatalf("refused write left a version: %d %v", latest, err)
			}
		})
	}
}
