package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestRoutinePublicationValidationDoesNotCreateExecutionAuthority(t *testing.T) {
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	seedPipelineRow(t, db, workspace, "publication-server", "publication-server")
	const definition = `{"dsl_version":"1.0","name":"publication-server","steps":[{"id":"answer","type":"agent_run","agent_slug":"agent","prompt":"Answer briefly"}]}`
	execOrFatal(t, db, `UPDATE pipelines SET definition_json=? WHERE id='publication-server'`, definition)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('publication-editor','publication-editor@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('publication-editor','publication-editor',?,'VIEWER')`, workspace)
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "publication-editor", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "publication-editor", workspace, "restricted", member, []access.Right{
		{Kind: "routine", ID: "publication-server", Operation: "view"},
		{Kind: "routine", ID: "publication-server", Operation: "edit"},
	}); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	t.Cleanup(func() { writer.Close() })
	router, err := NewRouter(db, "publication-server-synthetic-router-secret", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, db, "publication-editor", "publication-server-editor")
	path := "/api/v1/workspaces/" + workspace + "/restricted-routine-library/publication-server/draft"
	call := func(method, target string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var encoded []byte
		if body != nil {
			var marshalErr error
			encoded, marshalErr = json.Marshal(body)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s: HTTP%d want%d: %s", method, target, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	read := func() guestRoutineDraft {
		t.Helper()
		var draft guestRoutineDraft
		if err := json.Unmarshal(call("GET", path, nil, 200).Body.Bytes(), &draft); err != nil {
			t.Fatal(err)
		}
		return draft
	}
	// Calibrate the existing authenticated shared-draft route before testing
	// the missing server validation route. No agent or provider is installed.
	draft := read()
	draft.Name = "Reviewed publication"
	call("PUT", path, draft.guestRoutineDraftContent, 200)
	draft = read()
	if draft.DraftID == "" || draft.Revision != 1 {
		t.Fatal("shared draft calibration did not persist")
	}
	reference := map[string]any{"draft_id": draft.DraftID, "revision": draft.Revision, "base_revision": draft.BaseRevision}
	validated := call("POST", path+"/validate", reference, 200)
	var result struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(validated.Body.Bytes(), &result); err != nil || !result.Valid {
		t.Fatal("missing structural validation result", err)
	}
	for _, table := range []string{"pipeline_runs", "chats", "access_attempts", "restricted_workflow_jobs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("validation produced %s effects: count=%d err=%v", table, count, err)
		}
	}
	draft.Name = "Newer edit"
	call("PUT", path, draft.guestRoutineDraftContent, 200)
	call("POST", path+"/validate", reference, 409)
}
