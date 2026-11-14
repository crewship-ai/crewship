package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

// #5336: the sidecar save_page tool sends the agent run token on
// POST /api/v1/internal/pages/save. With agent access modes on, a valid token
// binds the save to its run (per-run create limit, created-page bookkeeping);
// a save without one is accepted under the crew hourly limit only, unless the
// workspace policy requires the token. The token is accounting, not a
// boundary between agents of one crew.

const runTokenSaveMaster = "synthetic-artifact-run-scope-master"

func runTokenSaveRig(t *testing.T) *runScopeRig {
	t.Helper()
	g := newRunScopeRig(t)
	mustExec(t, g.f.rig.db, `UPDATE crews SET autonomy_level='full' WHERE id=?`, g.f.a.CrewID)
	return g
}

func (g *runScopeRig) save(t *testing.T, name, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"workspace_id":"` + g.f.a.WorkspaceID + `","crew_id":"` + g.f.a.CrewID + `","agent_id":"` + g.f.a.AgentID + `",` +
		`"name":"` + name + `","panels":[{"id":"p1","schema":"status.v1","owner":"crew/principal-crew","producer":"agent/principal-agent","sla_seconds":30,"span":4}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Internal-Token", g.crewTok)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	return w
}

func (g *runScopeRig) runAccess(t *testing.T, run string) []string {
	t.Helper()
	return artifactRefRows(t, g.f, `SELECT page_id FROM artifact_run_access WHERE run_id=? AND source='created' ORDER BY page_id`, run)
}

func (g *runScopeRig) pageCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM pages WHERE workspace_id=? AND owner_crew_id=?`, g.f.a.WorkspaceID, g.f.a.CrewID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Issue test 2: with the token, the 4th save in one run is refused with
// artifact_run_limit, and the three created pages are recorded for the run.
func TestPageSaveRunToken_FourthSaveInOneRunRefused(t *testing.T) {
	g := runTokenSaveRig(t)
	g.start(t)
	for _, name := range []string{"Run save one", "Run save two", "Run save three"} {
		if w := g.save(t, name, g.runToken); w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w := g.save(t, "Run save four", g.runToken)
	if w.Code != http.StatusTooManyRequests || !bytes.Contains(w.Body.Bytes(), []byte(`"artifact_run_limit"`)) {
		t.Fatalf("4th save in one run: %d %s", w.Code, w.Body)
	}
	if n := g.pageCount(t); n != 3 {
		t.Fatalf("refused save left a page: %d pages", n)
	}
	created := g.runAccess(t, g.f.a.RunID)
	pages := artifactRefRows(t, g.f, `SELECT id FROM pages WHERE workspace_id=? AND owner_crew_id=? ORDER BY id`, g.f.a.WorkspaceID, g.f.a.CrewID)
	sort.Strings(created)
	if len(created) != 3 || len(pages) != 3 || created[0] != pages[0] || created[1] != pages[1] || created[2] != pages[2] {
		t.Fatalf("created pages not recorded for the run: access=%v pages=%v", created, pages)
	}
	// The limit is per run, read from the policy: raising it admits the next.
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET chat_run_create_limit=4 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	if w := g.save(t, "Run save four", g.runToken); w.Code != http.StatusCreated {
		t.Fatalf("raised limit: %d %s", w.Code, w.Body)
	}
}

// Issue test 3: without a token (an old sidecar), the save is accepted under
// the crew hourly limit only and counted.
func TestPageSaveRunToken_WithoutTokenCountedUnderTheCrewLimit(t *testing.T) {
	g := runTokenSaveRig(t)
	g.start(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET crew_hourly_page_limit=5 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	before := PageSaveWithoutRunTokenTotal()
	for _, name := range []string{"Legacy one", "Legacy two", "Legacy three", "Legacy four", "Legacy five"} {
		if w := g.save(t, name, ""); w.Code != http.StatusCreated {
			t.Fatalf("%s without a token must be accepted past the run limit: %d %s", name, w.Code, w.Body)
		}
	}
	if got := PageSaveWithoutRunTokenTotal() - before; got != 5 {
		t.Fatalf("saves without a token counted %d, want 5", got)
	}
	if rows := g.runAccess(t, g.f.a.RunID); len(rows) != 0 {
		t.Fatalf("a save without a token was bound to a run: %v", rows)
	}
	w := g.save(t, "Legacy six", "")
	if w.Code != http.StatusTooManyRequests || !bytes.Contains(w.Body.Bytes(), []byte(`"crew_hourly_page_limit"`)) {
		t.Fatalf("crew hourly limit must still apply: %d %s", w.Code, w.Body)
	}
	if got := PageSaveWithoutRunTokenTotal() - before; got != 5 {
		t.Fatalf("a refused tokenless save was counted: %d, want 5", got)
	}
}

// Issue test 4: once the workspace policy requires the token, a save without
// one is refused with 403 run_token_required; with the token it proceeds.
func TestPageSaveRunToken_RequiredRefusesASaveWithoutIt(t *testing.T) {
	g := runTokenSaveRig(t)
	g.start(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET save_requires_run_token=1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	before := PageSaveWithoutRunTokenTotal()
	w := g.save(t, "Required none", "")
	if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte(`"run_token_required"`)) {
		t.Fatalf("save without a token when required: %d %s", w.Code, w.Body)
	}
	if n := g.pageCount(t); n != 0 {
		t.Fatalf("refused save left a page: %d", n)
	}
	if PageSaveWithoutRunTokenTotal() != before {
		t.Fatal("a refused save must not be counted: the counter tracks accepted tokenless saves")
	}
	if w := g.save(t, "Required with token", g.runToken); w.Code != http.StatusCreated {
		t.Fatalf("save with the token when required: %d %s", w.Code, w.Body)
	}
}

// An invalid or foreign token is refused, never treated as no token.
func TestPageSaveRunToken_ForeignTokenRefused(t *testing.T) {
	g := runTokenSaveRig(t)
	g.start(t)
	ws, agent, run := g.f.a.WorkspaceID, g.f.a.AgentID, g.f.a.RunID
	otherCrewKey := internaltoken.DeriveAgentRunKey(runTokenSaveMaster, ws, "other-crew")
	ownKey := internaltoken.DeriveAgentRunKey(runTokenSaveMaster, ws, g.f.a.CrewID)
	for name, token := range map[string]string{
		"garbage":       "agtv2.not-a-token",
		"other crew":    internaltoken.DeriveAgentRunToken(otherCrewKey, ws, agent, run),
		"other agent":   internaltoken.DeriveAgentRunToken(ownKey, ws, "someone-else", run),
		"legacy bearer": internaltoken.DeriveAgentToken(runTokenSaveMaster, ws, agent),
	} {
		w := g.save(t, "Foreign "+name, token)
		if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte(`"invalid_run_token"`)) {
			t.Fatalf("%s token: %d %s", name, w.Code, w.Body)
		}
	}
	if n := g.pageCount(t); n != 0 {
		t.Fatalf("a foreign token created %d pages", n)
	}
}

// A valid token of a run that carries no human (a routine run, or a chat run
// admitted with the flag off) is accounted under the crew limit only: it is
// neither refused nor bound to a run it cannot be recorded for.
func TestPageSaveRunToken_RunWithoutAHumanUsesTheCrewLimit(t *testing.T) {
	g := runTokenSaveRig(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET save_requires_run_token=1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	key := internaltoken.DeriveAgentRunKey(runTokenSaveMaster, g.f.a.WorkspaceID, g.f.a.CrewID)
	routine := internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, g.f.a.AgentID, "routine-run-without-human")
	for _, name := range []string{"Routine one", "Routine two", "Routine three", "Routine four"} {
		if w := g.save(t, name, routine); w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// Flag off: the token, a foreign token and the requirement change nothing.
func TestPageSaveRunToken_FlagOffUnchanged(t *testing.T) {
	g := runTokenSaveRig(t)
	g.start(t)
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE flag_id='ff_artifact_agent_access_modes'`)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET save_requires_run_token=1, chat_run_create_limit=1, crew_hourly_page_limit=1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	before := PageSaveWithoutRunTokenTotal()
	keys := func(w *httptest.ResponseRecorder) []string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode %s: %v", w.Body, err)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	var shape []string
	for i, token := range []string{"", g.runToken, g.runToken, "agtv2.not-a-token", ""} {
		w := g.save(t, "Flag off "+string(rune('a'+i)), token)
		if w.Code != http.StatusCreated {
			t.Fatalf("flag off save %d (token %q): %d %s", i, token, w.Code, w.Body)
		}
		if k := keys(w); shape == nil {
			shape = k
		} else if len(k) != len(shape) {
			t.Fatalf("flag off response shape changed: %v vs %v", k, shape)
		}
	}
	if rows := g.runAccess(t, g.f.a.RunID); len(rows) != 0 {
		t.Fatalf("flag off bound saves to the run: %v", rows)
	}
	if PageSaveWithoutRunTokenTotal() != before {
		t.Fatal("flag off counted saves without a token")
	}
}

// The requirement is an explicit opt-in on the workspace policy: false by
// default, kept when omitted, bumps the version when changed.
func TestArtifactCreationPolicySaveRequiresRunToken(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	read := func() map[string]any {
		t.Helper()
		w := artifactPolicyCall(t, h, ws, user, "OWNER", "GET", "")
		artifactPolicyExpect(t, w, 200)
		var p map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read(); p["save_requires_run_token"] != false {
		t.Fatalf("default must be false and reported: %v", p)
	}
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":[],"base_version":0,"save_requires_run_token":"yes"}`), 400)
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":[],"base_version":0,"save_requires_run_token":true}`), 200)
	if p := read(); p["save_requires_run_token"] != true || p["version"] != float64(1) {
		t.Fatalf("set: %v", p)
	}
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":[],"base_version":1}`), 200)
	if p := read(); p["save_requires_run_token"] != true {
		t.Fatalf("omitted must keep the value: %v", p)
	}
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":[],"base_version":2,"save_requires_run_token":false}`), 200)
	if p := read(); p["save_requires_run_token"] != false || p["version"] != float64(3) {
		t.Fatalf("clear: %v", p)
	}
}

// F1: a routine page.create is issued by the host dispatcher with the master
// internal token, which no crew container holds. It is the server's own save,
// so the requirement and the tokenless counter do not apply to it. A crew
// token that forges the dispatcher's routine headers is still refused.
func TestPageSaveRunToken_HostRoutineSaveIsExemptFromTheRequirement(t *testing.T) {
	g := runTokenSaveRig(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET save_requires_run_token=1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	server := httptest.NewServer(g.router)
	t.Cleanup(server.Close)
	before := PageSaveWithoutRunTokenTotal()
	actions := newCrewshipActions(server.URL, runTokenSaveMaster, policy.NewResolver(g.f.rig.db), g.f.rig.db, newTestLogger())
	out, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb:        "page.create",
		Args:        map[string]any{"name": "Routine page", "panels": []any{map[string]any{"id": "p1", "schema": "status.v1", "owner": "crew/principal-crew", "producer": "agent/principal-agent", "sla_seconds": 30, "span": 4}}},
		WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, RunID: "routine-run-1",
	})
	if err != nil {
		t.Fatalf("routine page.create with the requirement on: %v", err)
	}
	if !bytes.Contains([]byte(out), []byte("routine-page")) {
		t.Fatalf("routine page.create output: %s", out)
	}
	if PageSaveWithoutRunTokenTotal() != before {
		t.Fatal("a host save must not count as a tokenless sidecar save")
	}
	// The same request from the crew container, forging the routine headers.
	body := `{"workspace_id":"` + g.f.a.WorkspaceID + `","crew_id":"` + g.f.a.CrewID + `","name":"Forged routine",` +
		`"panels":[{"id":"p1","schema":"status.v1","owner":"crew/principal-crew","producer":"agent/principal-agent","sla_seconds":30,"span":4}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Internal-Token", g.crewTok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Crewship-Pipeline-Run", "routine-run-1")
	req.Header.Set("X-Crewship-Chat-Origin", g.f.a.ChatID)
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	// Refused: the host-origin guards turn the forged headers away before the
	// save runs, and the exemption never looks at them.
	if w.Code != http.StatusForbidden {
		t.Fatalf("crew token forging the routine headers: %d %s", w.Code, w.Body)
	}
	if n := g.pageCount(t); n != 1 {
		t.Fatalf("forged routine save created a page: %d crew pages, want only the host one", n)
	}
	// Without the forged headers the host guards do not apply, and the
	// requirement refuses the crew token's tokenless save.
	req.Header.Del("X-Crewship-Pipeline-Run")
	req.Header.Del("X-Crewship-Chat-Origin")
	w = httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte(`"run_token_required"`)) {
		t.Fatalf("crew token save without a run token: %d %s", w.Code, w.Body)
	}
}

// F2: a token whose run principal is no longer live (here: expired) is
// refused, not downgraded to a tokenless save.
func TestPageSaveRunToken_ExpiredPrincipalRefused(t *testing.T) {
	g := runTokenSaveRig(t)
	g.start(t)
	mustExec(t, g.f.rig.db, `UPDATE artifact_run_principals SET expires_at='2020-01-01T00:00:00Z' WHERE run_id=?`, g.f.a.RunID)
	before := PageSaveWithoutRunTokenTotal()
	w := g.save(t, "Expired principal", g.runToken)
	if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte(`"invalid_run_token"`)) {
		t.Fatalf("expired principal: %d %s", w.Code, w.Body)
	}
	if n := g.pageCount(t); n != 0 || PageSaveWithoutRunTokenTotal() != before {
		t.Fatalf("expired principal saved (%d pages) or counted as tokenless", n)
	}
}

// F3: a read error while resolving the run or the requirement fails closed
// (500), never as "no run" or "not required".
func TestPageSaveRunToken_ReadErrorsFailClosed(t *testing.T) {
	t.Run("principal lookup", func(t *testing.T) {
		g := runTokenSaveRig(t)
		g.start(t)
		mustExec(t, g.f.rig.db, `ALTER TABLE artifact_run_principals RENAME TO artifact_run_principals_gone`)
		if w := g.save(t, "Lookup error", g.runToken); w.Code != http.StatusInternalServerError {
			t.Fatalf("principal lookup error: %d %s", w.Code, w.Body)
		}
		if n := g.pageCount(t); n != 0 {
			t.Fatalf("lookup error saved %d pages", n)
		}
	})
	t.Run("requirement read", func(t *testing.T) {
		g := runTokenSaveRig(t)
		mustExec(t, g.f.rig.db, `ALTER TABLE workspace_artifact_creation_policy DROP COLUMN save_requires_run_token`)
		if w := g.save(t, "Requirement error", ""); w.Code != http.StatusInternalServerError {
			t.Fatalf("requirement read error: %d %s", w.Code, w.Body)
		}
		if n := g.pageCount(t); n != 0 {
			t.Fatalf("requirement read error saved %d pages", n)
		}
	})
}

// F1, below the host-origin guard: even if a forged routine header reached the
// handler, the exemption is the master-token authentication alone.
func TestPageSaveRunToken_ExemptionIgnoresRoutineHeaders(t *testing.T) {
	g := runTokenSaveRig(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET save_requires_run_token=1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	h := NewPageHandler(g.f.rig.db, nil, newTestLogger())
	h.SetPolicyResolver(policy.NewResolver(g.f.rig.db))
	h.artifactMasterToken = runTokenSaveMaster
	body := `{"workspace_id":"` + g.f.a.WorkspaceID + `","crew_id":"` + g.f.a.CrewID + `","name":"Forged below guard",` +
		`"panels":[{"id":"p1","schema":"status.v1","owner":"crew/principal-crew","producer":"agent/principal-agent","sla_seconds":30,"span":4}]}`
	ctx := context.WithValue(context.WithValue(t.Context(), ctxInternalTokenWS, g.f.a.WorkspaceID), ctxInternalTokenCrew, g.f.a.CrewID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/save", bytes.NewBufferString(body)).WithContext(ctx)
	req.Header.Set("X-Crewship-Pipeline-Run", "routine-run-1")
	req.Header.Set("X-Crewship-Chat-Origin", g.f.a.ChatID)
	w := httptest.NewRecorder()
	h.InternalSave(w, req)
	if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte(`"run_token_required"`)) {
		t.Fatalf("forged routine headers below the guard: %d %s", w.Code, w.Body)
	}
}
