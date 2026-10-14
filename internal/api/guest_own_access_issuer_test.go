package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

// Integration of member self-explanation with the live issuer ceiling.
func TestGuestOwnAccessExplainsIssuerLoss(t *testing.T) {
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('own-guest','own-guest@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('own-member',?,'own-guest','VIEWER')`, workspace)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('own-crew',?,'Crew','own-crew')`, workspace)
	seedAgentRow(t, db, "own-agent", workspace, "own-crew", "Issuer-scoped worker", "own-agent", "AGENT")
	store := access.Store{DB: db}
	m, err := store.Membership(t.Context(), "own-guest", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "own-guest", workspace, "restricted", m, []access.Right{{Kind: "agent", ID: "own-agent", Operation: "discover"}}); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, "own-issuer-integration-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, db, "own-guest", "own-issuer-"+strings.Repeat("0", 40))
	read := func() memberEffectiveAccess {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/workspaces/"+workspace+"/access/me?user_id="+owner, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		var result memberEffectiveAccess
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &result) != nil || result.ID != "own-member" || len(result.Decisions) != 1 {
			t.Fatalf("own explanation: %d %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), `"issuer_id"`) || strings.Contains(rr.Body.String(), `"issuer_name"`) {
			t.Fatalf("own explanation exposed issuer metadata: %s", rr.Body.String())
		}
		own, err := store.OwnPolicy(t.Context(), "own-guest", workspace)
		if err != nil || len(own.GrantDetails) != 0 {
			t.Fatalf("self policy retained administrative issuer metadata: %+v %v", own, err)
		}
		return result
	}
	before := read()
	if before.Decisions[0].Name != "Issuer-scoped worker" || before.Decisions[0].State != "allowed" {
		t.Fatalf("live share: %+v", before)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, workspace)
	after := read()
	if after.Revision <= before.Revision || after.Decisions[0].State != "denied" || !strings.Contains(after.Decisions[0].Reason, "grant issuer") || after.Decisions[0].Name != "" {
		t.Fatalf("inert share explanation: %+v", after)
	}
}
