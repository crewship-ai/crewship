package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// pipelines_crud.go — Rollback + ImportPipeline.
//
// Rollback is a small store-write wrapper; testable end-to-end against
// the in-memory migrated DB. ImportPipeline's happy path needs valid
// DSL + agent-slug resolution; we cover the validation gates exhaustively
// here and leave the full save round-trip to the larger e2e suite.
// ---------------------------------------------------------------------------

// ---- Rollback ----

func TestRollback_BadJSON_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader("not-json"))
	req.SetPathValue("slug", "any")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestRollback_VersionMustBePositive(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	for _, v := range []int{0, -1, -100} {
		body := `{"version":` + pcrudItoa(v) + `}`
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.SetPathValue("slug", "any")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Rollback(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("version=%d: code = %d, want 400", v, rr.Code)
		}
	}
}

func TestRollback_PipelineNotFound_404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":1}`))
	req.SetPathValue("slug", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestRollback_VersionNotFound_404(t *testing.T) {
	// Pipeline exists with versions 1..2; ask for version 99. Store.Rollback
	// returns ErrNotFound from the inner GetVersion call, surfaced as 404.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-rb", "rb-slug", 2)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":99}`))
	req.SetPathValue("slug", "rb-slug")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (version missing)", rr.Code)
	}
}

func TestRollback_HappyPath_SwapsHeadDefinitionAndHash(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-rb2", "rb2", 3)
	// Pre-condition: head_version = 3 (from seed) and definition_hash = "hash-head".
	// After rollback to v1: head_version = 1 and definition_hash = "hash-1".

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":1}`))
	req.SetPathValue("slug", "rb2")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["definition_hash"] != "hash-1" {
		t.Errorf("definition_hash = %v, want hash-1 (the v1 hash)", got["definition_hash"])
	}
	// Verify head_version in the DB also moved — this is the
	// rollback contract that downstream queries (list-runs by head
	// version, e.g.) depend on.
	var head int
	if err := h.db.QueryRow(`SELECT head_version FROM pipelines WHERE id = 'pln-rb2'`).Scan(&head); err != nil {
		t.Fatalf("readback head_version: %v", err)
	}
	if head != 1 {
		t.Errorf("head_version = %d, want 1 after rollback", head)
	}
	// History preserved: pipeline_versions row count stays at 3.
	var versionsCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id = 'pln-rb2'`).Scan(&versionsCount); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versionsCount != 3 {
		t.Errorf("versions count = %d, want 3 (rollback preserves history)", versionsCount)
	}
}

// ---- ImportPipeline ----

func TestImportPipeline_BadJSON_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader("not-json"))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestImportPipeline_WrongFormat_400EchoesFormat(t *testing.T) {
	// Source builds a JSON object response (not the plain replyError),
	// echoing the offending format string back so the user can fix it.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	body := `{"format":"some-other-bundle/v9","pipeline":{"name":"x","definition":{"name":"x","steps":[]}},"author_crew_id":"crew-1"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if resp["error"] == "" || !strings.Contains(resp["error"], "unsupported") {
		t.Errorf("error body = %+v, want unsupported-bundle-format error", resp)
	}
	if resp["format"] != "some-other-bundle/v9" {
		t.Errorf("format echo = %q, want \"some-other-bundle/v9\" (so the user knows what failed)", resp["format"])
	}
}

func TestImportPipeline_MissingNameOrDefinition_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	cases := []struct{ name, body string }{
		{"missing-name", `{"format":"crewship-pipeline-bundle/v1","pipeline":{"definition":{"name":"x","steps":[]}},"author_crew_id":"c"}`},
		{"missing-definition", `{"format":"crewship-pipeline-bundle/v1","pipeline":{"name":"x"},"author_crew_id":"c"}`},
		{"both-missing", `{"format":"crewship-pipeline-bundle/v1","pipeline":{},"author_crew_id":"c"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ImportPipeline(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: code = %d, want 400", tc.name, rr.Code)
			}
		})
	}
}

func TestImportPipeline_MissingAuthorCrewID_400(t *testing.T) {
	// Source explicitly requires author_crew_id since the bundle
	// deliberately doesn't carry one — the receiving workspace's crew
	// becomes the owner, not whoever exported it.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	body := `{"format":"crewship-pipeline-bundle/v1","pipeline":{"name":"x","definition":{"name":"x","steps":[]}}}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "author_crew_id") {
		t.Errorf("body should mention author_crew_id: %s", rr.Body.String())
	}
}

func TestImportPipeline_MalformedDSL_422(t *testing.T) {
	// pipeline.Parse rejects the bundle's definition. Surfaces as 422
	// (Unprocessable Entity) — not 400, because the JSON envelope IS
	// valid; it's the inner DSL that fails semantic parse.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	body := `{"format":"crewship-pipeline-bundle/v1","pipeline":{"name":"x","definition":{"name":"","steps":[]}},"author_crew_id":"crew-1"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (DSL parse failure)", rr.Code)
	}
}

func TestImportPipeline_ValidationFailure_AgentSlugMissing_422(t *testing.T) {
	// pipeline.Validate cross-checks every agent_run step against the
	// receiving workspace's agent slugs. With no agents seeded, an
	// agent_run step pinned to "anna" must fail validation and surface
	// as 422 with the hint suggesting the receiving workspace add the
	// missing slug.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	body := `{
		"format":"crewship-pipeline-bundle/v1",
		"pipeline":{
			"name":"P","slug":"p",
			"definition":{
				"name":"p",
				"steps":[{"id":"s1","type":"agent_run","agent":"anna","prompt":"hi"}]
			}
		},
		"author_crew_id":"crew-1"
	}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if !strings.Contains(resp["error"], "validation") {
		t.Errorf("error body = %+v, want a validation-failure message", resp)
	}
	if !strings.Contains(resp["hint"], "agent slugs") {
		t.Errorf("hint = %q, want mention of agent slugs", resp["hint"])
	}
}
