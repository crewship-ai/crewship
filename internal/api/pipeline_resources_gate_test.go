package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedCrewResourceConfig sets the crew's services_json / devcontainer_config
// columns so ResolveCrewResources surfaces datastores + tools. Either arg may
// be "" to leave that capability empty.
func seedCrewResourceConfig(t *testing.T, db *sql.DB, crewID, servicesJSON, devcontainerJSON string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE crews SET services_json = ?, devcontainer_config = ? WHERE id = ?`,
		nullIfEmpty(servicesJSON), nullIfEmpty(devcontainerJSON), crewID); err != nil {
		t.Fatalf("seed crew resource config: %v", err)
	}
}

// resDef builds a routine definition declaring the given resources block.
// datastores / tools are JSON array literals (or "" for none).
func resDef(datastores, tools string) string {
	res := ""
	if datastores != "" || tools != "" {
		parts := []string{}
		if datastores != "" {
			parts = append(parts, `"datastores":`+datastores)
		}
		if tools != "" {
			parts = append(parts, `"tools":`+tools)
		}
		res = `,"resources":{` + strings.Join(parts, ",") + `}`
	}
	return `{"dsl_version":"1.0","name":"res-routine"` + res +
		`,"steps":[{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`
}

const (
	pgService      = `[{"name":"db","image":"postgres:16","ports":["5432"]}]`
	ansibleFeature = `{"features":{"ghcr.io/devcontainers-extra/features/ansible:2":{}}}`
	pgRequire      = `[{"type":"postgres","name":"db"}]`
	ansibleRequire = `[{"type":"ansible","name":"deploy.yml"}]`
)

func TestResourceGate_BlocksWhenDatastoreMissing(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_dsmiss", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_dsmiss", wsID, crewID, "Eva", "eva", "LEAD")
	// Crew has no services → requiring postgres must block.
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_dsmiss", "dsmiss", resDef(pgRequire, ""), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "dsmiss"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	var prob struct {
		Detail           string `json:"detail"`
		MissingResources []struct {
			Kind string `json:"kind"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"missing_resources"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(prob.MissingResources) != 1 {
		t.Fatalf("missing_resources = %#v, want 1 entry", prob.MissingResources)
	}
	m := prob.MissingResources[0]
	if m.Kind != "datastore" || m.Type != "postgres" {
		t.Errorf("missing = %#v, want kind=datastore type=postgres", m)
	}
	if !strings.Contains(prob.Detail, "postgres") || !strings.Contains(prob.Detail, "Ops") {
		t.Errorf("detail = %q, want mention of postgres + crew name", prob.Detail)
	}
	if runner.calls != 0 {
		t.Errorf("runner invoked %d times; run must NOT execute when blocked", runner.calls)
	}
}

func TestResourceGate_PassesWhenDatastorePresent(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_dsok", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_dsok", wsID, crewID, "Eva", "eva", "LEAD")
	seedCrewResourceConfig(t, h.db, crewID, pgService, "")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_dsok", "dsok", resDef(pgRequire, ""), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "dsok"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestResourceGate_BlocksWhenToolMissing(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_tlmiss", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_tlmiss", wsID, crewID, "Eva", "eva", "LEAD")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_tlmiss", "tlmiss", resDef("", ansibleRequire), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "tlmiss"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	var prob struct {
		MissingResources []struct {
			Kind string `json:"kind"`
			Type string `json:"type"`
		} `json:"missing_resources"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(prob.MissingResources) != 1 || prob.MissingResources[0].Kind != "tool" || prob.MissingResources[0].Type != "ansible" {
		t.Fatalf("missing_resources = %#v, want [{tool ansible}]", prob.MissingResources)
	}
}

func TestResourceGate_PassesWhenToolPresent(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_tlok", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_tlok", wsID, crewID, "Eva", "eva", "LEAD")
	seedCrewResourceConfig(t, h.db, crewID, "", ansibleFeature)
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_tlok", "tlok", resDef("", ansibleRequire), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "tlok"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestResourceGate_BothMissingListedTogether(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_both", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_both", wsID, crewID, "Eva", "eva", "LEAD")
	// Crew has neither postgres nor ansible.
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_both", "bothp", resDef(pgRequire, ansibleRequire), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "bothp"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	var prob struct {
		MissingResources []struct {
			Kind string `json:"kind"`
			Type string `json:"type"`
		} `json:"missing_resources"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(prob.MissingResources) != 2 {
		t.Fatalf("missing_resources = %#v, want 2 (datastore + tool)", prob.MissingResources)
	}
	kinds := map[string]bool{}
	for _, m := range prob.MissingResources {
		kinds[m.Kind] = true
	}
	if !kinds["datastore"] || !kinds["tool"] {
		t.Errorf("kinds = %#v, want both datastore and tool", kinds)
	}
}

func TestResourceGate_NoOpWhenNoResourcesDeclared(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_resnoop", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_resnoop", wsID, crewID, "Eva", "eva", "LEAD")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_resnoop", "resnoop", resDef("", ""), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "resnoop"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestResourceGate_FailOpenOnResolverError(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_resfo", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_resfo", wsID, crewID, "Eva", "eva", "LEAD")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_resfo", "resfo", resDef(pgRequire, ""), crewID)
	// Force ResolveCrewResources to error WITHOUT nulling the routine's
	// author_crew_id: drop a column its SELECT reads. (Dropping the whole
	// crews table would cascade ON DELETE SET NULL and instead exercise the
	// empty-crew branch.) The crew id stays populated, so this hits the
	// resolver-error → fail-open path specifically.
	if _, err := h.db.Exec(`ALTER TABLE crews DROP COLUMN services_json`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "resfo"))
	if rr.Code == http.StatusUnprocessableEntity {
		t.Fatalf("fail-open expected: resolver error must allow the run, got 422; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open); body=%s", rr.Code, rr.Body.String())
	}
}

func TestResourceGate_FailOpenWhenNoAuthorCrew(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	// No author crew on the routine → nothing to resolve "has" against → allow.
	if _, err := h.db.Exec(`INSERT INTO pipelines
		(id, workspace_id, slug, name, definition_json, definition_hash, author_crew_id, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, ?, 'hash', NULL, datetime('now'), datetime('now'), datetime('now'))`,
		"pipe_nocrew", wsID, "nocrew", "nocrew", resDef(pgRequire, "")); err != nil {
		t.Fatalf("seed pipeline nocrew: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "nocrew"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open, no crew); body=%s", rr.Code, rr.Body.String())
	}
}

// Direct unit coverage of the matcher (no HTTP) — case-insensitive datastore
// type match, tool match by friendly name.
func TestGateMissingResources_DirectMatch(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_direct", wsID, "Ops", "ops")
	seedCrewResourceConfig(t, h.db, crewID, pgService, ansibleFeature)

	res, err := ResolveCrewResources(context.Background(), h.db, crewID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Datastores) == 0 || res.Datastores[0].Type != "postgres" {
		t.Fatalf("expected postgres datastore, got %#v", res.Datastores)
	}
	foundAnsible := false
	for _, tl := range res.Tools {
		if tl.Type == "ansible" {
			foundAnsible = true
		}
	}
	if !foundAnsible {
		t.Fatalf("expected ansible tool, got %#v", res.Tools)
	}
}
