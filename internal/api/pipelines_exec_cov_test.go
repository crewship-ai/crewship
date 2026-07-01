package api

// Coverage for pipelines_exec.go — the Run/DryRun bad-body branches that
// the existing smoke tests skip. (The public TestRun handler was removed —
// the only draft validation gate is InternalTestRun; see internal_test_run_test.go.)

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---- Run / DryRun decode-error branches ----

func TestPipelineRun_BadBody400(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "covbad")
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)

	body := bytes.NewReader([]byte(`{broken`))
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", body), "ws_smoke")
	req.SetPathValue("slug", "covbad")
	req.ContentLength = int64(body.Len())
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestPipelineRun_TierAndTriggerOverrides(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "covtier")
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)

	// Unknown tier + unknown trigger are silently coerced, not 400-ed.
	body := bytes.NewReader([]byte(`{"inputs":{},"tier_override":"galactic","triggered_via":"carrier_pigeon"}`))
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", body), "ws_smoke")
	req.SetPathValue("slug", "covtier")
	req.ContentLength = int64(body.Len())
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var res pipeline.RunResult
	if err := json.NewDecoder(rr.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status = %q (err=%q)", res.Status, res.ErrorMessage)
	}
}

func TestPipelineDryRun_BadBody400(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "covdry")
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)

	body := bytes.NewReader([]byte(`{broken`))
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", body), "ws_smoke")
	req.SetPathValue("slug", "covdry")
	req.ContentLength = int64(body.Len())
	rr := httptest.NewRecorder()
	h.DryRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestPipelineDryRun_NotFound404(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "ghost")
	rr := httptest.NewRecorder()
	h.DryRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// seedSmokePipelineDef inserts a pipeline row with a caller-supplied raw
// definition JSON so manifest-enrichment tests can exercise the declared
// blast radius (integrations, egress, credentials, datastores, tools).
func seedSmokePipelineDef(t *testing.T, db *sql.DB, slug, def string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES (?, 'ws_smoke', ?, ?, ?, ?, 0, 1, 'crew_a', 'agent_lead', 'agent_tool_call', ?, 1, ?, ?)`,
		"pln_test_"+slug, slug, slug, def, "hash_"+slug, now, now, now)
	if err != nil {
		t.Fatalf("seed def: %v", err)
	}
}

// TestPipelineDryRun_IncludesManifest pins the dry_run contract: the response
// carries the declared MANIFEST (integrations, egress, credentials, agents,
// routines, datastores, tools, has_http/has_code) alongside the would_execute
// plan, so the UI can preview the routine's full blast radius.
func TestPipelineDryRun_IncludesManifest(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	def := `{"name":"manifest-demo",` +
		`"integrations_required":["github","slack"],` +
		`"egress_targets":["discord.com"],` +
		`"credentials_required":[{"type":"stripe"}],` +
		`"resources":{"datastores":[{"type":"postgres","name":"main"}],"tools":[{"type":"ansible","name":"deploy.yml"}]},` +
		`"steps":[` +
		`{"id":"a","type":"agent_run","agent_slug":"jordan","prompt":"hi"},` +
		`{"id":"b","type":"http","http":{"method":"GET","url":"https://api.example.com/x"}}` +
		`]}`
	seedSmokePipelineDef(t, db, "mani", def)
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)

	body := bytes.NewReader([]byte(`{"inputs":{}}`))
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", body), "ws_smoke")
	req.SetPathValue("slug", "mani")
	req.ContentLength = int64(body.Len())
	rr := httptest.NewRecorder()
	h.DryRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status       string             `json:"status"`
		WouldExecute []map[string]any   `json:"would_execute"`
		Manifest     *pipeline.Manifest `json:"manifest"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "DRY_RUN_OK" {
		t.Errorf("status = %q, want DRY_RUN_OK", out.Status)
	}
	if len(out.WouldExecute) == 0 {
		t.Error("would_execute report missing from dry_run response")
	}
	if out.Manifest == nil {
		t.Fatal("manifest missing from dry_run response")
	}
	m := out.Manifest
	if got := strings.Join(m.Integrations, ","); got != "github,slack" {
		t.Errorf("integrations = %q, want github,slack", got)
	}
	if got := strings.Join(m.Egress, ","); got != "api.example.com,discord.com" {
		t.Errorf("egress = %q, want api.example.com,discord.com", got)
	}
	if len(m.Credentials) != 1 || m.Credentials[0].Type != "stripe" {
		t.Errorf("credentials = %+v, want [stripe]", m.Credentials)
	}
	if got := strings.Join(m.Agents, ","); got != "jordan" {
		t.Errorf("agents = %q, want jordan", got)
	}
	if len(m.Datastores) != 1 || m.Datastores[0].Type != "postgres" {
		t.Errorf("datastores = %+v, want [postgres]", m.Datastores)
	}
	if len(m.Tools) != 1 || m.Tools[0].Type != "ansible" {
		t.Errorf("tools = %+v, want [ansible]", m.Tools)
	}
	if !m.HasHTTP {
		t.Error("has_http = false, want true (definition has an http step)")
	}
	if m.HasCode {
		t.Error("has_code = true, want false")
	}
}

// TestPipelineDryRun_MalformedDefinition_ManifestNull pins the best-effort
// contract: a routine whose stored definition no longer parses still returns
// a (200) report — manifest is null rather than 500-ing the preview.
func TestPipelineDryRun_MalformedDefinition_ManifestNull(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	// steps must be an array; a string makes pipeline.Parse fail.
	seedSmokePipelineDef(t, db, "corrupt", `{"name":"corrupt","steps":"not-an-array"}`)
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)

	body := bytes.NewReader([]byte(`{"inputs":{}}`))
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", body), "ws_smoke")
	req.SetPathValue("slug", "corrupt")
	req.ContentLength = int64(body.Len())
	rr := httptest.NewRecorder()
	h.DryRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (best-effort report); body=%s", rr.Code, rr.Body.String())
	}
	// manifest must be JSON null, and the response body must still be present.
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mv, ok := raw["manifest"]; !ok || string(mv) != "null" {
		t.Errorf("manifest = %s, want null", string(mv))
	}
	if _, ok := raw["status"]; !ok {
		t.Error("report (status field) missing from response")
	}
}
