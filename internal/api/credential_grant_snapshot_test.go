package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCredentialGrantSnapshot_EffectivePrecedenceAndRevocation(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewRow(t, db, "grant-crew", ws, "Grants", "grant-crew")
	for _, id := range []string{"a", "b"} {
		execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES (?,?,'grant-crew',?,?)`, id, ws, id, id)
	}
	seedCredentialEnc(t, db, ws, user, "grant-key", "API_TOKEN", "synthetic-never-return")
	execOrFatal(t, db, `INSERT INTO credential_crews(credential_id,crew_id) VALUES ('grant-key','grant-crew')`)
	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name,expires_at) VALUES ('grant-a','a','grant-key','API_TOKEN',?)`, expired)
	h := &InternalHandler{db: db, logger: newTestLogger()}
	read := func(crew string) (int, map[string]map[string]string) {
		r := httptest.NewRequest("GET", "/api/v1/internal/credential-grants?crew_id="+crew, nil)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "grant-crew")
		w := httptest.NewRecorder()
		h.CredentialGrants(w, r.WithContext(ctx))
		if strings.Contains(w.Body.String(), "synthetic-never-return") {
			t.Fatal("snapshot leaked value")
		}
		var body struct {
			Grants map[string]map[string]string `json:"grants"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Grants
	}
	code, grants := read("grant-crew")
	if code != 200 || !reflect.DeepEqual(grants["grant-key"], map[string]string{"b": ""}) {
		t.Fatalf("expired explicit grant fell back to crew: %d %#v", code, grants)
	}
	delivered, _, err := loadDeliveredCredentials(context.Background(), db, "b")
	if err != nil || len(delivered) != 1 || !reflect.DeepEqual(delivered[0].AgentGrants, grants["grant-key"]) {
		t.Fatalf("boot/live disagreement: %v %#v", err, delivered)
	}
	execOrFatal(t, db, `DELETE FROM credential_crews WHERE credential_id='grant-key'`)
	_, grants = read("grant-crew")
	if len(grants) != 0 {
		t.Fatal("revoked grant survived")
	}
	code, _ = read("sibling")
	if code != 403 {
		t.Fatal("sibling scope accepted")
	}
}
