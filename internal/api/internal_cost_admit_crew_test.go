package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// #2761: a routine script step runs against the crew-level sidecar, which has
// no agent. Its opaque HTTPS tunnels are admitted under the crew's own
// identity — taken from the crew-bound internal token, never from the body —
// with the same hard-budget rule as an agent and no credential at all.
func TestBudgetAdmissionCrewScopedScriptIdentity(t *testing.T) {
	r, _ := costRouter(t)
	db := r.db
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "script-crew", ws, "Script", "script")
	other := seedCrewRow(t, db, "script-other", ws, "Other", "other")
	h := NewInternalHandler(db, bindTestMaster, testLogger())
	call := func(token, body string) int {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", token)
		rr := httptest.NewRecorder()
		h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(rr, req)
		return rr.Code
	}
	token := internaltoken.DeriveCrewToken(bindTestMaster, ws, crew)
	crewTunnel := `{"crew_id":"` + crew + `","credential_id":"","provider":"OPAQUE_TUNNEL"}`

	for _, tc := range []struct {
		name  string
		token string
		body  string
		want  int
	}{
		{"own crew, uncapped", token, crewTunnel, 200},
		{"crew identity must match the token's crew", token, `{"crew_id":"` + other + `","credential_id":"","provider":"OPAQUE_TUNNEL"}`, 403},
		// A workspace-bound token passes the first check and reaches the
		// crew-binding branch; the master token stops earlier, so it is not
		// the case that proves the crew identity comes from the token.
		{"a workspace-bound token cannot claim a crew", internaltoken.DeriveWorkspaceToken(bindTestMaster, ws), crewTunnel, 403},
		{"the master token is refused before identity is considered", bindTestMaster, crewTunnel, 403},
		{"a crew-scoped caller holds no credential", token, `{"crew_id":"` + crew + `","credential_id":"budget-key","provider":"OPENAI"}`, 403},
		{"agent and crew together is ambiguous", token, `{"agent_id":"x","crew_id":"` + crew + `","credential_id":"","provider":"OPAQUE_TUNNEL"}`, 400},
		{"neither agent nor crew", token, `{"credential_id":"","provider":"OPAQUE_TUNNEL"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(tc.token, tc.body); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}

	// The budget rule is not bypassed: a hard cap that would refuse an
	// unattributed agent tunnel refuses the crew's too, and a soft one does not.
	for _, scope := range []string{`'workspace',?`, `'crew',?`, `'mission','m'`} {
		_, err := db.Exec(`DELETE FROM budget_limits`)
		if err != nil {
			t.Fatal(err)
		}
		args := []any{ws}
		switch scope {
		case `'workspace',?`:
			args = append(args, ws)
		case `'crew',?`:
			args = append(args, crew)
		}
		if _, err := db.Exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('cap',?,`+scope+`,'day',100,'hard')`, args...); err != nil {
			t.Fatal(err)
		}
		if got := call(token, crewTunnel); got != 403 {
			t.Fatalf("hard %s cap: got %d, want 403", scope, got)
		}
		if _, err := db.Exec(`UPDATE budget_limits SET mode='soft'`); err != nil {
			t.Fatal(err)
		}
		if got := call(token, crewTunnel); got != 200 {
			t.Fatalf("soft %s cap: got %d, want 200", scope, got)
		}
	}
	if _, err := db.Exec(`UPDATE crews SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM budget_limits`); err != nil {
		t.Fatal(err)
	}
	if got := call(token, crewTunnel); got != 403 {
		t.Fatalf("deleted crew: got %d, want 403", got)
	}
}
