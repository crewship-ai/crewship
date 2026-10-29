package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// An agent reads the commits feed through its retained mandate and current
// run authority. Its own revisions are those of its mandate; the human's are
// shown by label only. It loses the feed with the mandate, and an agent run
// token can never open a human editor session.
func TestGridCraftAgentCommitsFeedUnderMandate(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "grid-feed-page", "grid-feed", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='grid-feed-page'`)
	mustExec(t, f.rig.db, `UPDATE users SET full_name='Owner Person' WHERE id=?`, f.a.HumanUserID)
	f.pages.artifactMasterToken = "synthetic-workbook-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	engine := &gridcraftBlockingEngine{}
	h := &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true), editor: true}
	var pushed []gridcraftCommitted
	h.broadcast = func(prefix, id, event string, payload any) {
		if prefix != "page" || id != "grid-feed-page" || event != "workbook.committed" {
			t.Errorf("push %s:%s %s", prefix, id, event)
		}
		pushed = append(pushed, payload.(gridcraftCommitted))
	}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "workbook-feed-owner")
	humanRequest := func(method, body string) *http.Request {
		r := httptest.NewRequest(method, "/", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID, TokenID: "clt-" + f.a.HumanUserID})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@grid-feed-page")
		return r
	}
	w := httptest.NewRecorder()
	h.importFile(w, humanRequest("POST", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`))
	wantCode(t, w, 200, "import")
	w = httptest.NewRecorder()
	h.mandate(w, humanRequest("POST", fmt.Sprintf(`{"mandate_id":"feed-mandate","run_id":%q,"base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, f.a.RunID)))
	wantCode(t, w, 200, "mandate")
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	commits := `{"action":"commits","artifact_id":"grid-feed-page","mandate_id":"feed-mandate","since":1}`
	wantError(t, call(commits), 503, "gridcraft_editor_disabled", "workspace flag off")
	mustExec(t, f.rig.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('ffo-agent-feed','ffl_gridcraft_editor',?,1)`, f.a.WorkspaceID)
	wantCode(t, call(`{"action":"apply","artifact_id":"grid-feed-page","mandate_id":"feed-mandate","request":{"mutation_id":"agent-1","mandate_id":"feed-mandate","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`), 200, "agent apply")
	// The import happened while the workspace flag was off: it is not pushed.
	if len(pushed) != 1 || pushed[0].Rev != 2 || pushed[0].Cells != 1 {
		t.Fatalf("pushes %+v", pushed)
	}
	got := call(commits)
	wantCode(t, got, 200, "agent feed")
	var feed struct {
		HeadRev int64 `json:"head_rev"`
		Commits []struct {
			Rev        int64  `json:"rev"`
			Kind       string `json:"kind"`
			ActorLabel string `json:"actor_label"`
			Own        bool   `json:"own"`
			MutationID string `json:"mutation_id"`
		} `json:"commits"`
	}
	// The feed starts at the mandate's base revision (1); the import before it
	// is not part of it (TestGridCraftAgentFeedStartsAtMandateBase).
	if err := json.Unmarshal(got.Body.Bytes(), &feed); err != nil || feed.HeadRev != 2 || len(feed.Commits) != 1 {
		t.Fatalf("feed %s", got.Body.String())
	}
	if c := feed.Commits[0]; !c.Own || c.MutationID != "agent-1" || c.Kind != "cells" {
		t.Fatalf("own agent commit %+v", c)
	}
	if before := call(strings.Replace(commits, `"since":1`, `"since":0`, 1)); before.Code != 410 || !strings.Contains(before.Body.String(), `"min_since":1`) {
		t.Fatalf("since before the mandate %d %s", before.Code, before.Body.String())
	}
	if strings.Contains(got.Body.String(), "user:") || strings.Contains(got.Body.String(), "agent:") {
		t.Fatalf("internal authority leaked: %s", got.Body.String())
	}
	for name, body := range map[string]string{
		"missing since":       `{"action":"commits","artifact_id":"grid-feed-page","mandate_id":"feed-mandate"}`,
		"limit over max":      `{"action":"commits","artifact_id":"grid-feed-page","mandate_id":"feed-mandate","since":0,"limit":201}`,
		"since on a read":     `{"action":"read","artifact_id":"grid-feed-page","mandate_id":"feed-mandate","since":0}`,
		"request on commits":  strings.Replace(commits, `"since":1`, `"since":1,"mutation_id":"agent-1"`, 1),
		"negative since":      strings.Replace(commits, `"since":1`, `"since":-1`, 1),
		"unknown packet item": strings.Replace(commits, `"since":1`, `"since":1,"session_id":"s1"`, 1),
	} {
		wantCode(t, call(body), 400, name)
	}
	gone := call(strings.Replace(commits, `"since":1`, `"since":9`, 1))
	if gone.Code != 410 || !strings.Contains(gone.Body.String(), `"error":"snapshot_required"`) {
		t.Fatalf("since after head %d %s", gone.Code, gone.Body.String())
	}
	// An agent run token on the human session route is refused.
	asAgent := documentDraftRequest(f, token, "POST", "")
	agentCtx := context.WithValue(asAgent.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID, TokenID: "clt-" + f.a.HumanUserID})
	agentCtx = context.WithValue(agentCtx, ctxWorkspaceID, f.a.WorkspaceID)
	agentCtx = context.WithValue(agentCtx, ctxAuthKind, AuthKindCLIToken)
	asAgent = asAgent.WithContext(agentCtx)
	asAgent.SetPathValue("slug", "@grid-feed-page")
	w = httptest.NewRecorder()
	h.openSession(w, asAgent)
	wantCode(t, w, 404, "agent run token opens no editor session")
	w = httptest.NewRecorder()
	h.openSession(w, humanRequest("POST", ""))
	wantCode(t, w, 201, "owner opens a session")
	var sessions int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM gridcraft_sessions`).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("sessions=%d %v", sessions, err)
	}
	// Revoking the mandate ends the agent's feed.
	mustExec(t, f.rig.db, `UPDATE gridcraft_intents SET revoked_at='2026-01-01T00:00:00Z' WHERE mandate_id='feed-mandate'`)
	wantCode(t, call(commits), 404, "revoked mandate")
}
