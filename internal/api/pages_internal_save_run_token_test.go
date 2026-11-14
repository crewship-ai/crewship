package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
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
	if PageSaveWithoutRunTokenTotal() != before+1 {
		t.Fatal("a refused save without a token must still be counted")
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
