package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListCredentials_CrewScope covers #1031: the internal credential metadata
// listing must be filterable to the calling crew so a compromised agent
// container (which reaches this endpoint through its sidecar) can't enumerate
// every peer credential's existence/provider in the workspace.
func TestListCredentials_CrewScope(t *testing.T) {
	h, db, userID, wsID := covICRig(t)

	// Two crews, one agent each.
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'crew-a')`, wsID)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-b', ?, 'B', 'crew-b')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-a', ?, 'crew-a', 'AgA', 'ag-a')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-b', ?, 'crew-b', 'AgB', 'ag-b')`, wsID)

	// credA assigned to crew-a's agent; credB assigned to crew-b's agent;
	// credC directly crew-scoped to crew-a via credential_crews (no agent).
	// The metadata path never decrypts, so a placeholder encrypted value is fine.
	const enc = "enc-placeholder"
	covICSeedAICred(t, db, wsID, userID, "credA", enc, nil)
	covICSeedAICred(t, db, wsID, userID, "credB", enc, nil)
	covICSeedAICred(t, db, wsID, userID, "credC", enc, nil)
	mustExec(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-a', 'ag-a', 'credA', 'A', 0, datetime('now'))`)
	mustExec(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-b', 'ag-b', 'credB', 'B', 0, datetime('now'))`)
	mustExec(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('credC', 'crew-a')`)

	ids := func(q string) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?"+q, nil)
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var out []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]bool{}
		for _, c := range out {
			got[c.ID] = true
		}
		return got
	}

	// crew-a scoped: sees its agent's cred (credA) and its crew-scoped cred
	// (credC), but NOT crew-b's credB.
	t.Run("crew-scoped hides peer credentials", func(t *testing.T) {
		got := ids("workspace_id=" + wsID + "&crew_id=crew-a")
		if !got["credA"] || !got["credC"] {
			t.Errorf("crew-a should see credA+credC, got %v", got)
		}
		if got["credB"] {
			t.Errorf("crew-a must NOT see crew-b's credB, got %v", got)
		}
	})

	// No crew_id → unchanged workspace-wide behaviour (TokenSyncer / legacy).
	t.Run("no crew_id returns all (backward compatible)", func(t *testing.T) {
		got := ids("workspace_id=" + wsID)
		if !got["credA"] || !got["credB"] || !got["credC"] {
			t.Errorf("workspace-wide should see all three, got %v", got)
		}
	})
}
