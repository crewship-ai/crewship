package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
)

// PR2 of #5317: read and write enforcement for a chat run's artifact scope.
// These tests describe the approved spec (and its amendments 1, 2, 5 and 7)
// and fail until PR2 lands: today a plain chat run reaches no artifact
// without an exact-run consent bound to one page.

type runScopeRig struct {
	f        *artifactPrincipalFixture
	router   *Router
	runToken string
	crewTok  string
	tokens   map[string]string
}

func newRunScopeRig(t *testing.T, setup ...func(*artifactPrincipalFixture)) *runScopeRig {
	t.Helper()
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactRefsFixture(t, true, setup...)
	const master = "synthetic-artifact-run-scope-master"
	router, err := NewRouter(f.rig.db, "synthetic-artifact-run-scope-secret", newTestLogger(), WithInternalToken(master))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	key := internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID)
	return &runScopeRig{
		f:        f,
		router:   router,
		runToken: internaltoken.DeriveAgentRunToken(key, f.a.WorkspaceID, f.a.AgentID, f.a.RunID),
		crewTok:  internaltoken.DeriveCrewToken(master, f.a.WorkspaceID, f.a.CrewID),
		tokens:   map[string]string{},
	}
}

// create seeds a Project artifact through the production human handler.
func (g *runScopeRig) create(t *testing.T, userID, role, slug string) string {
	t.Helper()
	token, ok := g.tokens[userID]
	if !ok {
		token = mintTokenFor(t, g.f.rig.db, userID, "run-scope-"+userID)
		g.tokens[userID] = token
	}
	body := `{"slug":"` + slug + `","name":"` + slug + `",` + strings.TrimPrefix(artifactProjectBody(0, slug), "{")
	req := pagesRequest(t, http.MethodPost, "/api/v1/pages/artifacts", g.f.a.WorkspaceID, userID, role, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindCLIToken))
	w := httptest.NewRecorder()
	g.router.pages.CreateArtifactProject(w, req)
	if w.Code != 201 {
		t.Fatalf("seed %s: %d %s", slug, w.Code, w.Body)
	}
	var id string
	if err := g.f.rig.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug=?`, g.f.a.WorkspaceID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// start records the message's references and admits the run through the
// trusted human path (PR1).
func (g *runScopeRig) start(t *testing.T, refs ...string) {
	t.Helper()
	if len(refs) > 0 {
		if err := g.f.pages.RecordArtifactRefs(t.Context(), g.f.receipt, g.f.a.MessageID, refs); err != nil {
			t.Fatal("record", err)
		}
	}
	if code := artifactRefsStartRun(t, g.f, g.f.a.RunID, g.f.a.MessageID); code != 201 {
		t.Fatalf("run start: %d", code)
	}
}

func (g *runScopeRig) tool(operation, slug, payload string) *httptest.ResponseRecorder {
	raw := fmt.Sprintf(`{"operation":%q,"slug":%q,"payload":%s}`, operation, slug, payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/artifact-tool", bytes.NewReader([]byte(raw)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+g.runToken)
	req.Header.Set("X-Internal-Token", g.crewTok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	return w
}

func (g *runScopeRig) grant(t *testing.T, page, subjectType, subject, level string) {
	t.Helper()
	// A created artifact starts in its author's Personal; sharing moves it out.
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
	// Readers other than the owner see only the shared version.
	mustExec(t, g.f.rig.db, `UPDATE artifact_version_heads SET shared_version=latest_version WHERE page_id=?`, page)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,?,?,?,?)`, page, subjectType, subject, level, g.f.rig.adminID)
}

// Spec §2 read, test 1: an attached artifact is readable by the run; a sibling
// the human could also read is not, and its refusal is the same as for a
// slug that does not exist (amendment 5).
func TestRunScope_ReadsTheAttachedArtifactOnly(t *testing.T) {
	g := newRunScopeRig(t)
	attached := g.create(t, g.f.a.HumanUserID, "MEMBER", "scope-attached")
	g.create(t, g.f.a.HumanUserID, "MEMBER", "scope-sibling")
	g.start(t, attached)

	if w := g.tool("artifact_versions", "scope-attached", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "scope-attached") {
		t.Fatalf("attached artifact not readable by its run: %d %s", w.Code, w.Body)
	}
	sibling := g.tool("artifact_versions", "scope-sibling", `{}`)
	missing := g.tool("artifact_versions", "scope-nowhere", `{}`)
	if sibling.Code == 200 {
		t.Fatalf("an unattached sibling was readable: %s", sibling.Body)
	}
	if sibling.Code != missing.Code || sibling.Body.String() != missing.Body.String() {
		t.Fatalf("refusal tells unattached from missing: %d %q vs %d %q", sibling.Code, sibling.Body, missing.Code, missing.Body)
	}
}

// Test 2: a name or slug in the text is not a reference.
func TestRunScope_TextIsNotAReference(t *testing.T) {
	g := newRunScopeRig(t)
	g.create(t, g.f.a.HumanUserID, "MEMBER", "scope-named-in-text")
	mustExec(t, g.f.rig.db, `UPDATE conversation_messages SET content='please read scope-named-in-text' WHERE id=?`, g.f.a.MessageID)
	g.start(t)
	if w := g.tool("artifact_versions", "scope-named-in-text", `{}`); w.Code == 200 {
		t.Fatal("a slug in the message text gave the run access")
	}
}

// Amendment 1 and 6: the agent's standing grant reaches a page only where the
// human may also read it.
func TestRunScope_StandingAccessIsIntersectedWithTheHuman(t *testing.T) {
	g := newRunScopeRig(t)
	both := g.create(t, g.f.rig.adminID, "OWNER", "scope-shared-both")
	agentOnly := g.create(t, g.f.rig.adminID, "OWNER", "scope-agent-only")
	g.grant(t, both, "user", g.f.a.HumanUserID, "read")
	g.grant(t, both, "agent", g.f.a.AgentID, "read")
	g.grant(t, agentOnly, "agent", g.f.a.AgentID, "read")
	g.start(t)
	if w := g.tool("artifact_versions", "scope-shared-both", `{}`); w.Code != 200 {
		t.Fatalf("standing grant the human shares was refused: %d %s", w.Code, w.Body)
	}
	if w := g.tool("artifact_versions", "scope-agent-only", `{}`); w.Code == 200 {
		t.Fatal("the agent's own grant reached a page its human cannot read")
	}
}

// Amendment 2 on the standing path: a human who reaches a page only through
// the workspace ADMIN or OWNER role does not lend that reach to the run, even
// where the agent holds its own grant. The reference layer is covered by
// TestArtifactRefs_DelegableViewerByStanding.
func TestRunScope_RoleOnlyReachIsNotDelegatedThroughStandingAccess(t *testing.T) {
	for _, role := range []string{"ADMIN", "OWNER"} {
		t.Run(role, func(t *testing.T) {
			g := newRunScopeRig(t, func(f *artifactPrincipalFixture) {
				mustExec(t, f.rig.db, `UPDATE workspace_members SET role=? WHERE user_id=? AND workspace_id=?`, role, f.a.HumanUserID, f.a.WorkspaceID)
			})
			shared := g.create(t, g.f.rig.adminID, "OWNER", "scope-role-shared")
			roleOnly := g.create(t, g.f.rig.adminID, "OWNER", "scope-role-only")
			g.grant(t, shared, "user", g.f.a.HumanUserID, "read")
			g.grant(t, shared, "agent", g.f.a.AgentID, "read")
			g.grant(t, roleOnly, "agent", g.f.a.AgentID, "read")
			g.start(t)
			if w := g.tool("artifact_versions", "scope-role-shared", `{}`); w.Code != 200 {
				t.Fatalf("baseline: a page the %s reads as a member was refused: %d %s", role, w.Code, w.Body)
			}
			refused := g.tool("artifact_versions", "scope-role-only", `{}`)
			missing := g.tool("artifact_versions", "scope-role-nowhere", `{}`)
			if refused.Code == 200 {
				t.Fatalf("the %s role's reach was delegated to the run: %s", role, refused.Body)
			}
			if refused.Code != missing.Code || refused.Body.String() != missing.Body.String() {
				t.Fatalf("refusal tells role-only from missing: %d %q vs %d %q", refused.Code, refused.Body, missing.Code, missing.Body)
			}
		})
	}
}

// Test 3, read side: access the human loses mid-run is lost by the run.
func TestRunScope_ReadStopsWhenTheHumanLosesAccess(t *testing.T) {
	g := newRunScopeRig(t)
	shared := g.create(t, g.f.rig.adminID, "OWNER", "scope-revoked")
	g.grant(t, shared, "user", g.f.a.HumanUserID, "read")
	g.start(t, shared)
	if w := g.tool("artifact_versions", "scope-revoked", `{}`); w.Code != 200 {
		t.Fatalf("baseline: %d %s", w.Code, w.Body)
	}
	mustExec(t, g.f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user'`, shared)
	if w := g.tool("artifact_versions", "scope-revoked", `{}`); w.Code == 200 {
		t.Fatal("the run kept reading after its human lost access")
	}
}

// Spec §2 write: a write needs the human's edit right now, re-checked.
func TestRunScope_WriteNeedsTheHumansEditRight(t *testing.T) {
	g := newRunScopeRig(t)
	own := g.create(t, g.f.a.HumanUserID, "MEMBER", "scope-own-write")
	viewOnly := g.create(t, g.f.rig.adminID, "OWNER", "scope-view-only")
	g.grant(t, viewOnly, "user", g.f.a.HumanUserID, "read")
	g.start(t, own, viewOnly)
	if w := g.tool("artifact_publish", "scope-own-write", artifactProjectBody(1, "Second")); w.Code != 200 {
		t.Fatalf("write to the human's own attached artifact: %d %s", w.Code, w.Body)
	}
	if w := g.tool("artifact_publish", "scope-view-only", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("wrote where the human may only view")
	}
}

// Spec §2 create: up to the cap (3 by default) per chat run, each created
// artifact joins the run's reach, and the next create is refused.
func TestRunScope_CreateCapAndCreatedJoinTheScope(t *testing.T) {
	g := newRunScopeRig(t)
	g.start(t)
	for i := 1; i <= 3; i++ {
		slug := fmt.Sprintf("scope-created-%d", i)
		body := `{"name":"` + slug + `",` + strings.TrimPrefix(artifactProjectBody(0, slug), "{")
		if w := g.tool("artifact_create", slug, body); w.Code != 201 {
			t.Fatalf("create %d: %d %s", i, w.Code, w.Body)
		}
		if w := g.tool("artifact_versions", slug, `{}`); w.Code != 200 {
			t.Fatalf("created artifact %d not in reach: %d %s", i, w.Code, w.Body)
		}
	}
	body := `{"name":"scope-created-4",` + strings.TrimPrefix(artifactProjectBody(0, "scope-created-4"), "{")
	if w := g.tool("artifact_create", "scope-created-4", body); w.Code == 201 {
		t.Fatal("a fourth artifact was created in one run")
	}
}

// Amendment 1: the read cap (50 distinct artifacts by default) applies to the
// standing-access path too.
func TestRunScope_ReadCapCountsDistinctArtifacts(t *testing.T) {
	g := newRunScopeRig(t)
	for i := 1; i <= 51; i++ {
		id := g.create(t, g.f.rig.adminID, "OWNER", fmt.Sprintf("scope-cap-%02d", i))
		g.grant(t, id, "user", g.f.a.HumanUserID, "read")
		g.grant(t, id, "agent", g.f.a.AgentID, "read")
	}
	g.start(t)
	for i := 1; i <= 50; i++ {
		if w := g.tool("artifact_versions", fmt.Sprintf("scope-cap-%02d", i), `{}`); w.Code != 200 {
			t.Fatalf("read %d under the cap: %d %s", i, w.Code, w.Body)
		}
	}
	if w := g.tool("artifact_versions", "scope-cap-01", `{}`); w.Code != 200 {
		t.Fatalf("re-reading an artifact already read must not count again: %d", w.Code)
	}
	if w := g.tool("artifact_versions", "scope-cap-51", `{}`); w.Code == 200 {
		t.Fatal("the 51st distinct artifact was read")
	}
}

// Test 11: with the flag off a plain chat run reaches nothing, as today.
func TestRunScope_FlagOffKeepsTodaysRefusal(t *testing.T) {
	g := newRunScopeRig(t)
	attached := g.create(t, g.f.a.HumanUserID, "MEMBER", "scope-flag-off")
	g.start(t, attached)
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if w := g.tool("artifact_versions", "scope-flag-off", `{}`); w.Code != 403 {
		t.Fatalf("flag off: %d %s", w.Code, w.Body)
	}
	_ = json.Valid
}

func runScopeStartWith(t *testing.T, g *runScopeRig, run, trigger string, ctx context.Context) int {
	t.Helper()
	writer := journal.NewWriter(g.f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-refs-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(g.f.rig.db, "synthetic-refs-master", newTestLogger())
	h.SetJournal(writer)
	raw, _ := json.Marshal(map[string]any{"id": run, "workspace_id": g.f.a.WorkspaceID, "agent_id": g.f.a.AgentID, "chat_id": g.f.a.ChatID, "trigger_type": trigger, "human_authority": g.f.receipt, "trigger_message_id": g.f.a.MessageID})
	w := httptest.NewRecorder()
	h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)).WithContext(ctx))
	return w.Code
}

// A plain chat run (no attachment) records its human with the flag on; a run
// not started by the human's own message (routine, webhook, crew IPC) does not.
func TestRunScope_OnlyAHumanMessageRecordsAPrincipal(t *testing.T) {
	g := newRunScopeRig(t)
	principals := func(run string) int {
		var n int
		if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`, run).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, trigger := range []string{"ROUTINE", "WEBHOOK", "CRON", "AGENT"} {
		run := "principal-" + strings.ToLower(trigger)
		runScopeStartWith(t, g, run, trigger, t.Context())
		if principals(run) != 0 {
			t.Fatalf("%s run recorded a human principal", trigger)
		}
	}
	crew := context.WithValue(t.Context(), ctxInternalTokenCrew, g.f.a.CrewID)
	runScopeStartWith(t, g, "principal-crew-ipc", "USER", crew)
	if principals("principal-crew-ipc") != 0 {
		t.Fatal("a crew-token run recorded a human principal")
	}
	g.start(t)
	if principals(g.f.a.RunID) != 1 {
		t.Fatal("a plain chat run with the flag on recorded no principal")
	}
}

// The limits are read from the policy on every use: lowering the create
// limit refuses the next create with its own code, and a limit change bumps
// the policy version (which moves its authority epoch).
func TestRunScope_LimitsComeFromThePolicyOnEachUse(t *testing.T) {
	g := newRunScopeRig(t)
	g.start(t)
	create := func(slug string) *httptest.ResponseRecorder {
		body := `{"name":"` + slug + `",` + strings.TrimPrefix(artifactProjectBody(0, slug), "{")
		return g.tool("artifact_create", slug, body)
	}
	if w := create("scope-limit-1"); w.Code != 201 {
		t.Fatalf("first create: %d %s", w.Code, w.Body)
	}
	var before, after int64
	_ = g.f.rig.db.QueryRow(`SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=?`, g.f.a.WorkspaceID).Scan(&before)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET chat_run_create_limit=1, version=version+1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	if err := g.f.rig.db.QueryRow(`SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=?`, g.f.a.WorkspaceID).Scan(&after); err != nil || after != before+1 {
		t.Fatalf("the fixture has no stored policy to lower: %d->%d %v", before, after, err)
	}
	w := create("scope-limit-2")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "artifact_run_limit") {
		t.Fatalf("over the lowered limit: %d %s", w.Code, w.Body)
	}
	var n int
	if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='scope-limit-2'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused create left a page: %d %v", n, err)
	}
}
