package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestManualRunExpectedHashPinsReviewedRecipe(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(h.db))
	in, first := seedPinnable(t, h, user, ws, "expected-hash")
	in.DefinitionJSON = `{"name":"expected-hash","description":"v2","steps":[]}`
	second, err := h.store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	run := func(body, key string) (int, map[string]any) {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)), user, ws, "OWNER")
		req.SetPathValue("slug", "expected-hash")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rr := httptest.NewRecorder()
		h.Run(rr, req)
		var response map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &response)
		return rr.Code, response
	}
	if status, _ := run(`{"expected_definition_hash":"`+first.DefinitionHash+`"}`, ""); status != http.StatusConflict {
		t.Fatalf("stale HEAD = %d, want 409", status)
	}
	var rejectedRuns int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id=?`, first.ID).Scan(&rejectedRuns); err != nil || rejectedRuns != 0 {
		t.Fatalf("stale preview created %d runs: %v", rejectedRuns, err)
	}
	if status, _ := run(`{"expected_definition_hash":"bad"}`, ""); status != http.StatusBadRequest {
		t.Fatalf("invalid hash = %d", status)
	}
	if status, _ := run(`{"expected_definition_hash":"`+second.DefinitionHash+`","delay_seconds":1}`, ""); status != http.StatusBadRequest {
		t.Fatalf("deferred hash = %d", status)
	}
	status, response := run(`{"expected_definition_hash":"`+second.DefinitionHash+`"}`, "retry-key")
	if status != http.StatusOK {
		t.Fatalf("run = %d: %v", status, response)
	}
	var version int
	var hash string
	if err := h.db.QueryRow(`SELECT pipeline_version, definition_hash FROM pipeline_runs WHERE id=?`, response["run_id"]).Scan(&version, &hash); err != nil {
		t.Fatal(err)
	}
	if version != 2 || hash != second.DefinitionHash {
		t.Fatalf("executed version=%d hash=%s", version, hash)
	}
	// An uncertain client response can be retried with the same key after
	// another publish; the original run is returned, never executed again.
	in.DefinitionJSON = `{"name":"expected-hash","description":"v3","steps":[]}`
	if _, err := h.store.Save(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	status, retried := run(`{"expected_definition_hash":"`+second.DefinitionHash+`"}`, "retry-key")
	if status != http.StatusOK || retried["run_id"] != response["run_id"] || retried["status"] != "DEDUPED" {
		t.Fatalf("retry = %d: %v", status, retried)
	}
	if status, _ := run(`{"expected_definition_hash":"`+second.DefinitionHash+`"}`, "new-key"); status != http.StatusConflict {
		t.Fatalf("new request against moved HEAD = %d, want 409", status)
	}
	if status, _ := run(`{"pinned_version":1,"expected_definition_hash":"`+second.DefinitionHash+`"}`, ""); status != http.StatusConflict {
		t.Fatalf("mismatched historical hash = %d, want 409", status)
	}
	status, historical := run(`{"pinned_version":1,"expected_definition_hash":"`+first.DefinitionHash+`"}`, "historic-key")
	if status != http.StatusOK {
		t.Fatalf("historical pin = %d: %v", status, historical)
	}
	if err := h.db.QueryRow(`SELECT pipeline_version, definition_hash FROM pipeline_runs WHERE id=?`, historical["run_id"]).Scan(&version, &hash); err != nil {
		t.Fatal(err)
	}
	if version != 1 || hash != first.DefinitionHash {
		t.Fatalf("historical version=%d hash=%s", version, hash)
	}
	if _, err := h.db.Exec(`DELETE FROM pipeline_versions WHERE pipeline_id=? AND version=1`, first.ID); err != nil {
		t.Fatal(err)
	}
	status, recovered := run(`{"pinned_version":1,"expected_definition_hash":"`+first.DefinitionHash+`"}`, "historic-key")
	if status != http.StatusOK || recovered["run_id"] != historical["run_id"] || recovered["status"] != "DEDUPED" {
		t.Fatalf("archived version removed after run, retry = %d: %v", status, recovered)
	}
}
