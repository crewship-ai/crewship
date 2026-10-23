package api

import (
	"context"
	"database/sql"
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

// On a shared (published) workbook an agent applies directly only under a
// mandate the workbook owner admitted after publication, bounded to an
// explicit cell. Mandates from before publication, mandates from anybody but
// the owner, wildcard targets and agent imports keep proposal_required. The
// agent's commits reach the named editors under the agent's label.
func TestGridCraftSharedWorkbookAgentNeedsPostPublicationOwnerMandate(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "grid-shared-page", "grid-shared", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='grid-shared-page'`)
	mustExec(t, f.rig.db, `INSERT INTO users(id,email,full_name) VALUES('grid-peer','grid-peer@example.test','Peer')`)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('grid-peer-member',?,'grid-peer','MEMBER')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('ffo-shared-agent','ffl_gridcraft_editor',?,1)`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `UPDATE agents SET name='Pilot Agent' WHERE id=?`, f.a.AgentID)
	f.pages.artifactMasterToken = "synthetic-workbook-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	engine := &gridcraftBlockingEngine{}
	h := &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true), editor: true}
	ownerToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "workbook-share-owner")
	peerToken := mintTokenFor(t, f.rig.db, "grid-peer", "workbook-share-peer0")
	humanAs := func(userID, token, method, target, body string) *http.Request {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: userID, Email: userID + "@example.test"})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@grid-shared-page")
		return r
	}
	owner := func(method, body string) *http.Request {
		return humanAs(f.a.HumanUserID, ownerToken, method, "/", body)
	}
	serve := func(handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	agent := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	mandate := func(id string, base int, target string) string {
		return fmt.Sprintf(`{"mandate_id":%q,"run_id":%q,"base_rev":%d,"generation":1,"targets":[%q]}`, id, f.a.RunID, base, target)
	}
	read := func(id string) string {
		return fmt.Sprintf(`{"action":"read","artifact_id":"grid-shared-page","mandate_id":%q}`, id)
	}
	apply := func(id string, base int, mutation string) string {
		return fmt.Sprintf(`{"action":"apply","artifact_id":"grid-shared-page","mandate_id":%q,"request":{"mutation_id":%q,"mandate_id":%q,"base_rev":%d,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`, id, mutation, id, base)
	}
	proposalRequired := func(w *httptest.ResponseRecorder, what string) {
		t.Helper()
		wantCode(t, w, 409, what)
		if !strings.Contains(w.Body.String(), "proposal_required") {
			t.Fatalf("%s: %s", what, w.Body.String())
		}
	}
	head := func() int {
		t.Helper()
		var rev int
		if err := f.rig.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='grid-shared-page'`).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		return rev
	}

	// Private period: the owner admits a mandate and the agent applies (rev 2).
	wantCode(t, serve(h.importFile, owner("POST", gridcraftImportBody("import", 0, 0))), 200, "import")
	wantCode(t, serve(h.mandate, owner("POST", mandate("private-edit", 1, "Sheet1!B2"))), 200, "private mandate")
	wantCode(t, agent(read("private-edit")), 200, "private direct read")
	wantCode(t, agent(apply("private-edit", 1, "agent-private")), 200, "private direct apply")

	// Publication at head 2 revokes it; a mandate from before publication
	// never applies directly on the shared workbook.
	shared := serve(h.share, owner("POST", `{"editor":"grid-peer@example.test","publish":true}`))
	wantCode(t, shared, 200, "publish")
	if !strings.Contains(shared.Body.String(), `"revoked_mandates":1`) || !strings.Contains(shared.Body.String(), `"published_rev":2`) {
		t.Fatalf("share response %s", shared.Body.String())
	}
	proposalRequired(agent(read("private-edit")), "pre-publication mandate read")
	proposalRequired(agent(apply("private-edit", 1, "agent-pre")), "pre-publication mandate apply")
	// Even a pre-publication intent that somehow stayed live is below the
	// publication floor and is refused.
	mustExec(t, f.rig.db, `INSERT INTO gridcraft_intents(workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash) SELECT workspace_id,page_id,'stale-live',human_user_id,run_id,1,generation,page_version,policy_version,targets_json,'stale' FROM gridcraft_intents WHERE mandate_id='private-edit'`)
	proposalRequired(agent(read("stale-live")), "live mandate below the publication floor")

	// A named editor cannot admit a mandate; nor can anybody name a wildcard.
	proposalRequired(serve(h.mandate, humanAs("grid-peer", peerToken, "POST", "/", mandate("peer-edit", 2, "Sheet1!B2"))), "editor admits")
	proposalRequired(serve(h.mandate, owner("POST", mandate("wildcard", 2, "*"))), "wildcard on shared")
	if n := gridcraftCountDB(t, f.rig.db, `SELECT count(*) FROM gridcraft_intents WHERE mandate_id IN ('peer-edit','wildcard')`); n != 0 {
		t.Fatalf("refused mandates recorded: %d", n)
	}

	// The owner admits an explicit-cell mandate after publication.
	wantCode(t, serve(h.mandate, owner("POST", mandate("shared-edit", 2, "Sheet1!B2"))), 200, "post-publication owner mandate")
	wantCode(t, agent(read("shared-edit")), 200, "shared agent read")
	applied := agent(apply("shared-edit", 2, "agent-shared"))
	wantCode(t, applied, 200, "shared agent apply")
	if head() != 3 {
		t.Fatalf("head after shared agent apply = %d", head())
	}
	// Agents never import, also not on a shared workbook.
	proposalRequired(agent(`{"action":"apply","artifact_id":"grid-shared-page","mandate_id":"shared-edit","request":{"mutation_id":"agent-import","mandate_id":"shared-edit","base_rev":3,"generation":1,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}}`), "agent import on shared")

	// The editor sees the agent's commit under the agent's label.
	feed := serve(h.commits, humanAs("grid-peer", peerToken, "GET", "/?since=2", ""))
	wantCode(t, feed, 200, "editor feed")
	var out struct {
		Commits []struct {
			Rev        int64  `json:"rev"`
			ActorLabel string `json:"actor_label"`
			Own        bool   `json:"own"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(feed.Body.Bytes(), &out); err != nil || len(out.Commits) != 1 || out.Commits[0].Rev != 3 || out.Commits[0].ActorLabel != "Pilot Agent" || out.Commits[0].Own {
		t.Fatalf("editor feed %s", feed.Body.String())
	}
	if strings.Contains(feed.Body.String(), "agent:") {
		t.Fatalf("internal authority leaked: %s", feed.Body.String())
	}

	// Revocation ends the agent's direct access as on a private workbook.
	revoke := owner("DELETE", "")
	revoke.SetPathValue("mandate", "shared-edit")
	wantCode(t, serve(h.revoke, revoke), 200, "revoke")
	wantCode(t, agent(read("shared-edit")), 404, "revoked shared mandate")
	if head() != 3 {
		t.Fatalf("refused agent calls changed the head: %d", head())
	}
}

func gridcraftCountDB(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
