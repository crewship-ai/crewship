package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PR4 of #5317: every access a flag-on chat run makes through its scope is
// audited, naming the human the agent acted for. Reads and write
// authorizations are recorded once per run and artifact (each version already
// names the run in the version history); creates every time. A failed audit
// refuses the access.

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

func TestRunAudit_WritesOncePerArtifactCreatesEachTime(t *testing.T) {
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
	if writes != 1 || creates != 1 {
		t.Fatalf("writes=%d creates=%d, want 1 and 1: %+v", writes, creates, runAuditRows(t, g))
	}
	var actors int
	if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=? AND version>1`, id).Scan(&actors); err != nil || actors != 2 {
		t.Fatalf("both writes must be in the version history: %d %v", actors, err)
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
			body := `{"name":"audit-closed-new",` + strings.TrimPrefix(artifactProjectBody(0, "audit-closed-new"), "{")
			if w := g.tool("artifact_create", "audit-closed-new", body); w.Code == 201 {
				t.Fatal("create succeeded without its audit")
			}
			var pages int
			if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='audit-closed-new'`).Scan(&pages); err != nil || pages != 0 {
				t.Fatalf("refused create left a page: %d %v", pages, err)
			}
			var latest int
			if err := g.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, id).Scan(&latest); err != nil || latest != 1 {
				t.Fatalf("refused write left a version: %d %v", latest, err)
			}
		})
	}
}

// human calls a human handler as the run's human, with their CLI token.
func (g *runScopeRig) human(t *testing.T, handler func(http.ResponseWriter, *http.Request), path, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	user := g.f.a.HumanUserID
	token, ok := g.tokens[user]
	if !ok {
		token = mintTokenFor(t, g.f.rig.db, user, "run-scope-"+user)
		g.tokens[user] = token
	}
	req := pagesRequest(t, http.MethodPost, path, g.f.a.WorkspaceID, user, "MEMBER", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindCLIToken))
	req.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

// createAs seeds an artifact with the given body, owned by the run's human.
func (g *runScopeRig) createAs(t *testing.T, slug, body string) string {
	t.Helper()
	body = `{"slug":"` + slug + `","name":"` + slug + `",` + strings.TrimPrefix(body, "{")
	if w := g.human(t, g.router.pages.CreateArtifactProject, "/api/v1/pages/artifacts", "", body); w.Code != 201 {
		t.Fatalf("seed %s: %d %s", slug, w.Code, w.Body)
	}
	var id string
	if err := g.f.rig.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug=?`, g.f.a.WorkspaceID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Each operation family audits its own access: every case runs on a fresh run
// and a fresh artifact, so no other operation can have written the row first.
func TestRunAudit_EachOperationRecordsItsOwnAccess(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	project := artifactProjectBody(0, "audit-op")
	cases := []struct {
		name, body, operation, payload, want string
		code                                 int
		setup                                func(t *testing.T, g *runScopeRig, id string) string
	}{
		{name: "view", body: project, operation: "artifact_versions", payload: `{}`, want: "read", code: 200},
		{name: "comments", body: project, operation: "artifact_comments", payload: `{}`, want: "read", code: 200},
		{name: "data collections", body: dashboardBody(0, "sales"), operation: "artifact_data_collections", payload: `{"version":1}`, want: "read", code: 200},
		{name: "data read", body: dashboardBody(0, "sales"), operation: "artifact_data_read", payload: `{"collection":"sales","version":1}`, want: "read", code: 200},
		{name: "data batch", body: dashboardBody(0, "sales"), operation: "artifact_data_batch", payload: `{"collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":1}}]}`, want: "write", code: 200},
		{name: "slides read", body: slidesArtifactBody(0, "Audit"), operation: "artifact_slides_read", payload: `{"version":1}`, want: "read", code: 200},
		{name: "pdf export", body: slidesArtifactBody(0, "Audit"), operation: "artifact_export_pdf", payload: `{"version":1}`, want: "read", code: 200},
		{name: "publish", body: project, operation: "artifact_publish", payload: artifactProjectBody(1, "Second"), want: "write", code: 200},
		{name: "restore", body: project, operation: "artifact_restore", payload: `{"base_version":2,"version":1}`, want: "write", code: 200,
			setup: func(t *testing.T, g *runScopeRig, id string) string {
				if w := g.human(t, g.router.pages.PublishArtifactProject, "/api/v1/pages/x/artifact", "@"+id, artifactProjectBody(1, "Human second")); w.Code != 200 {
					t.Fatalf("human publish: %d %s", w.Code, w.Body)
				}
				return ""
			}},
		{name: "reply", body: project, operation: "artifact_reply", want: "write", code: 200,
			setup: func(t *testing.T, g *runScopeRig, id string) string {
				w := g.human(t, g.router.pages.CreateArtifactProjectComment, "/api/v1/pages/x/artifact/comments", "@"+id, `{"version":1,"path":"index.html","selector":"#hero","quote":"audit-op","text":"Which one?"}`)
				var c artifactProjectComment
				if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &c) != nil {
					t.Fatalf("human comment: %d %s", w.Code, w.Body)
				}
				mustExec(t, g.f.rig.db, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,comment_id,base_version,run_id,created_at) VALUES(?,?,?,?,'audit-reply',?,?,1,?,datetime('now'))`, g.f.a.MessageID, g.f.a.WorkspaceID, g.f.a.HumanUserID, g.f.a.ChatID, id, c.ID, g.f.a.RunID)
				return fmt.Sprintf(`{"comment_id":%q,"version":1,"text":"Which heading?","needs_clarification":true}`, c.ID)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newRunScopeRig(t)
			id := g.createAs(t, "audit-op", tc.body)
			payload := tc.payload
			if tc.setup != nil {
				if p := tc.setup(t, g, id); p != "" {
					payload = p
				}
			}
			if rows := runAuditRows(t, g); len(rows) != 0 {
				t.Fatalf("setup recorded run access: %+v", rows)
			}
			g.start(t, id)
			if w := g.tool(tc.operation, "@"+id, payload); w.Code != tc.code {
				t.Fatalf("%s: %d %s", tc.operation, w.Code, w.Body)
			}
			rows := runAuditRows(t, g)
			if len(rows) != 1 {
				t.Fatalf("want exactly one %s record, got %+v", tc.want, rows)
			}
			g.wantAudit(t, rows[0], id, "referenced", tc.want)
		})
	}
}

// One row per run, artifact and operation: a read and a write of the same
// artifact are two rows, and every create is its own row.
func TestRunAudit_ReadThenWriteAndCreatesAreSeparateRows(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-rw")
	g.start(t, id)
	if w := g.tool("artifact_versions", "audit-rw", `{}`); w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	if w := g.tool("artifact_publish", "audit-rw", artifactProjectBody(1, "Second")); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}
	for _, slug := range []string{"audit-new-1", "audit-new-2"} {
		body := `{"name":"` + slug + `",` + strings.TrimPrefix(artifactProjectBody(0, slug), "{")
		if w := g.tool("artifact_create", slug, body); w.Code != 201 {
			t.Fatalf("create %s: %d %s", slug, w.Code, w.Body)
		}
	}
	got := map[string]int{}
	for _, r := range runAuditRows(t, g) {
		got[r.Operation]++
		if r.Operation != "create" && r.Page != id {
			t.Fatalf("unexpected record %+v", r)
		}
	}
	if got["read"] != 1 || got["write"] != 1 || got["create"] != 2 || len(got) != 3 {
		t.Fatalf("records by operation %v, want read 1, write 1, create 2", got)
	}
}

// The dedupe key names the run: a second run on the same artifact is its own
// row, and a repeated access by the first run is not.
func TestRunAudit_EachRunHasItsOwnRow(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "audit-two-runs")
	first := g.f.a
	runA := &artifactRunAuthority{WorkspaceID: first.WorkspaceID, HumanUserID: first.HumanUserID, AgentID: first.AgentID, CrewID: first.CrewID, RunID: "audit-run-a", ChatID: first.ChatID}
	runB := *runA
	runB.RunID = "audit-run-b"
	tx, err := g.f.rig.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, step := range []struct {
		run *artifactRunAuthority
		op  string
	}{{runA, "read"}, {&runB, "read"}, {runA, "read"}, {runA, "write"}, {&runB, "write"}, {runA, "write"}} {
		if err := recordAgentRunArtifactAccess(t.Context(), tx, step.run, id, runReachReferenced, step.op); err != nil {
			t.Fatal(step.run.RunID, step.op, err)
		}
	}
	var rows int
	if err := tx.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND entity_id=?`, auditActionAgentRunArtifactAccess, id).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("records = %d (%v), want one per run and operation (4)", rows, err)
	}
}

// A write refused after its pre-check (a stale base version, an unknown
// comment, a stale data version) records nothing: pre-checks are probes and
// only the write's own transaction audits.
func TestRunAudit_WriteRefusedAfterItsPreCheckRecordsNothing(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	for _, tc := range []struct{ name, body, operation, payload string }{
		{"stale publish", artifactProjectBody(0, "audit-pre"), "artifact_publish", artifactProjectBody(7, "Stale")},
		{"unknown comment reply", artifactProjectBody(0, "audit-pre"), "artifact_reply", `{"comment_id":"no-such-comment","version":1,"text":"Hello"}`},
		{"stale restore", artifactProjectBody(0, "audit-pre"), "artifact_restore", `{"base_version":7,"version":1}`},
		{"stale data batch", dashboardBody(0, "sales"), "artifact_data_batch", `{"collection":"sales","base_version":7,"documents":[{"id":"row","ifVersion":0,"value":{"amount":1}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRunScopeRig(t)
			id := g.createAs(t, "audit-pre", tc.body)
			g.start(t, id)
			if w := g.tool(tc.operation, "@"+id, tc.payload); w.Code == 200 || w.Code == 201 {
				t.Fatalf("%s succeeded: %d %s", tc.operation, w.Code, w.Body)
			}
			if rows := runAuditRows(t, g); len(rows) != 0 {
				t.Fatalf("a refused write was audited: %+v", rows)
			}
		})
	}
}
