package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// An authenticated endpoint remains an ENDPOINT_URL in the Vault. Its proxy
// projection must retain the real identity used by admission, audit and cost.
func TestEndpointProxySourceAdmission(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	r, _ := costRouter(t)
	db := r.db
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "endpoint-crew", ws, "Endpoint", "endpoint")
	agent := seedAgentRow(t, db, "endpoint-agent", ws, crew, "Endpoint", "endpoint", "AGENT")
	value := `{"baseURL":"https://fixture.example:38423/v1","apiKey":"synthetic-endpoint-token"}`
	enc, err := encryption.Encrypt(value)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES('endpoint-source',?,'endpoint-source',?,'ENDPOINT_URL','OLLAMA','ACTIVE','WORKSPACE',?)`, ws, enc, user)
	mustExec(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('endpoint-grant',?,'endpoint-source','OWN_ENDPOINT')`, agent)
	h := NewInternalHandler(db, bindTestMaster, testLogger())
	ep := resolveLocalModelEndpoint(context.Background(), db, testLogger(), agent, ws, []mcpCredEntry{{ID: "endpoint-source", Type: CredTypeEndpointURL, Value: value, SourceRevision: endpointSourceRevision(enc), AgentIDs: []string{agent}, AgentGrants: map[string]string{agent: ""}}})
	projected, added := h.appendProxiedEndpointCredential(nil, ep)
	if !added || len(projected) != 1 {
		t.Fatal("authenticated endpoint not projected")
	}
	t.Run("real source identity", func(t *testing.T) {
		if projected[0].ID != "endpoint-source" {
			t.Errorf("proxy identity = %q; want real Vault ID", projected[0].ID)
		}
		if projected[0].AgentGrants == nil {
			t.Error("effective grant snapshot lost")
		}
	})
	t.Run("native host admission", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(`{"agent_id":"`+agent+`","credential_id":"endpoint-source","provider":"OPENAI_COMPAT","source_revision":"`+endpointSourceRevision(enc)+`"}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(bindTestMaster, ws, crew))
		rr := httptest.NewRecorder()
		h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("endpoint admission %d: %s", rr.Code, rr.Body.String())
		}
	})
}
