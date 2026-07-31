package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The admin surface for "which key does this evaluator spend" (#1554).
//
// The five evaluator rows are the paid half of the Keeper stack. They could name
// a provider and a model and not a credential, so on an instance holding several
// keys the console could say which model a sweep runs on and not whose
// subscription it bills.

// auxCredHandler is newKeeperAuxHandler with a real credentials table and the
// write-time check wired, i.e. the production shape.
func auxCredHandler(t *testing.T) (*AdminKeeperAuxHandler, *keepercfg.AuxStore, *sql.DB, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (id, email, full_name)
		VALUES ('admin-1', 'admin@example.com', 'Admin')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	aux := keepercfg.NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := aux.Load(context.Background()); err != nil {
		t.Fatalf("load aux store: %v", err)
	}
	judge := keepercfg.New(db, keeperEnvDefaults)
	if err := judge.Load(context.Background()); err != nil {
		t.Fatalf("load judge store: %v", err)
	}
	h := NewAdminKeeperAuxHandler(aux, judge, nil, newTestLogger()).WithCredentials(newAuxCredentialCheck(db))
	return h, aux, db, wsID
}

func seedAuxCredential(t *testing.T, db *sql.DB, wsID, id, name, credType, plain string) {
	t.Helper()
	e, err := encryption.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, status, created_by)
		VALUES (?, ?, ?, ?, ?, 'ACTIVE', ?)`,
		id, wsID, name, e, credType, "admin-1")
}

// wsReq is manageReq with a workspace, which the credential check needs — the
// route is behind RequireWorkspace in production.
func wsSlotReq(method, slot, wsID, body string) *http.Request {
	req := slotReq(method, slot, body)
	return req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, wsID))
}

func TestAdminKeeperAux_PutStoresACredential(t *testing.T) {
	h, store, db, wsID := auxCredHandler(t)
	seedAuxCredential(t, db, wsID, "cred_prod", "prod-anthropic", string(CredTypeAPIKey), "sk-ant-live")

	rr := httptest.NewRecorder()
	h.Put(rr, wsSlotReq("PUT", "behavior", wsID, `{"credential_id":"cred_prod"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	row := auxRow(t, decodeAux(t, rr), "behavior")
	if row.CredentialID.Value != "cred_prod" {
		t.Errorf("credential_id = %q, want cred_prod", row.CredentialID.Value)
	}
	if row.CredentialID.Source != "instance" {
		t.Errorf("credential source = %q, want instance", row.CredentialID.Source)
	}
	if !row.Overridden {
		t.Error("a slot with a pinned key is not reported as overridden")
	}
	// And it actually reached the store the evaluators resolve from — a response
	// that echoes the write without persisting it is the failure mode here.
	if got := store.CredentialFor("behavior"); got != "cred_prod" {
		t.Errorf("store.CredentialFor = %q, want cred_prod", got)
	}
}

// The write-time workspace gate. keeper_aux_settings is instance-global and the
// vault is workspace-scoped; this is where the two meet, and an admin binding a
// key their workspace does not hold has to be refused rather than accepted and
// then silently degraded at build time.
func TestAdminKeeperAux_PutRefusesAnUnusableCredential(t *testing.T) {
	h, store, db, wsID := auxCredHandler(t)
	seedAuxCredential(t, db, wsID, "cred_endpoint", "an-endpoint", string(CredTypeEndpointURL), "https://llm.example/v1")

	for _, tc := range []struct{ name, body string }{
		{"an id this workspace does not hold", `{"credential_id":"cred_from_elsewhere"}`},
		{"an ENDPOINT_URL, which is not a key", `{"credential_id":"cred_endpoint"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.Put(rr, wsSlotReq("PUT", "behavior", wsID, tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", rr.Code, rr.Body.String())
			}
			if got := store.CredentialFor("behavior"); got != "" {
				t.Errorf("a refused write still landed: %q", got)
			}
		})
	}
}

// Clearing is not a lookup: "" means "go back to the process env", and it must
// work even when the credential it named has since been deleted outright.
func TestAdminKeeperAux_PutClearsTheCredentialWithoutALookup(t *testing.T) {
	h, store, db, wsID := auxCredHandler(t)
	seedAuxCredential(t, db, wsID, "cred_prod", "prod-anthropic", string(CredTypeAPIKey), "sk-ant-live")

	rr := httptest.NewRecorder()
	h.Put(rr, wsSlotReq("PUT", "behavior", wsID, `{"credential_id":"cred_prod"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("seed write got %d: %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, db, `DELETE FROM credentials WHERE id = 'cred_prod'`)

	rr = httptest.NewRecorder()
	h.Put(rr, wsSlotReq("PUT", "behavior", wsID, `{"credential_id":""}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("clear got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := store.CredentialFor("behavior"); got != "" {
		t.Errorf("credential still pinned after a clear: %q", got)
	}
}

// The backward-compatibility guarantee at the API boundary: a body that says
// nothing about the credential leaves it alone, and an untouched instance
// reports no credential anywhere.
func TestAdminKeeperAux_CredentialIsAbsentUntilSomebodySetsOne(t *testing.T) {
	h, _, db, wsID := auxCredHandler(t)
	seedAuxCredential(t, db, wsID, "cred_prod", "prod-anthropic", string(CredTypeAPIKey), "sk-ant-live")

	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/aux", ""))
	for _, s := range decodeAux(t, rr).Slots {
		if s.CredentialID.Value != "" {
			t.Errorf("slot %s reports credential %q on an untouched instance", s.Slot, s.CredentialID.Value)
		}
	}

	// A model-only patch must not disturb the credential field either way.
	rr = httptest.NewRecorder()
	h.Put(rr, wsSlotReq("PUT", "behavior", wsID, `{"credential_id":"cred_prod"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("seed write got %d: %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.Put(rr, wsSlotReq("PUT", "behavior", wsID, `{"model":"claude-opus-5"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("model write got %d: %s", rr.Code, rr.Body.String())
	}
	row := auxRow(t, decodeAux(t, rr), "behavior")
	if row.CredentialID.Value != "cred_prod" {
		t.Errorf("a model-only patch dropped the credential: %q", row.CredentialID.Value)
	}
}

// With no credential seam wired (an older/embedded router) the field must not
// become silently writable — a key that is stored but never validated is how a
// cross-workspace binding would slip in.
func TestAdminKeeperAux_UnwiredCredentialSeamRefusesTheField(t *testing.T) {
	h, _ := newKeeperAuxHandler(t)
	rr := httptest.NewRecorder()
	h.Put(rr, wsSlotReq("PUT", "behavior", "ws-1", `{"credential_id":"cred_prod"}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503: %s", rr.Code, rr.Body.String())
	}
}
