package api

// Coverage for pipelines_exec.go — TestRun's draft-DSL validation chain
// and the save_token mint, plus the Run/DryRun bad-body branches that the
// existing smoke tests skip.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

const covPipeDef = `{"name":"cov-test","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`

func covPipeTestRun(t *testing.T, h *PipelineHandler, body string, user *AuthUser) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(body)), "ws_smoke")
	if user != nil {
		req = req.WithContext(withUser(req.Context(), user))
	}
	rr := httptest.NewRecorder()
	h.TestRun(rr, req)
	return rr
}

func TestPipelineTestRun_NoRunner503(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	if rr := covPipeTestRun(t, h, `{}`, nil); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestPipelineTestRun_ValidationChain(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "ok"}, nil)

	t.Run("bad body", func(t *testing.T) {
		if rr := covPipeTestRun(t, h, "{nope", nil); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("missing definition", func(t *testing.T) {
		if rr := covPipeTestRun(t, h, `{"author_crew_id":"crew_a"}`, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("missing author_crew_id", func(t *testing.T) {
		if rr := covPipeTestRun(t, h, `{"definition":`+covPipeDef+`}`, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unparseable DSL 422", func(t *testing.T) {
		rr := covPipeTestRun(t, h, `{"definition":{"steps":"not-an-array"},"author_crew_id":"crew_a"}`, nil)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("invalid DSL 422", func(t *testing.T) {
		// Parses but fails Validate (no steps).
		rr := covPipeTestRun(t, h, `{"definition":{"name":"x","steps":[]},"author_crew_id":"crew_a"}`, nil)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestPipelineTestRun_HappyPath_NoSecret_NoToken(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	runner := &stubRunner{output: "draft works"}
	h := NewPipelineHandler(db, slog.Default(), runner, nil)

	rr := covPipeTestRun(t, h, `{"definition":`+covPipeDef+`,"author_crew_id":"crew_a","sample_inputs":{}}`, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		pipeline.RunResult
		SaveToken string `json:"save_token"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "COMPLETED" || out.Output != "draft works" {
		t.Errorf("result = %+v", out.RunResult)
	}
	if out.SaveToken != "" {
		t.Errorf("save_token = %q, want empty without signing secret", out.SaveToken)
	}
	if runner.calls != 1 {
		t.Errorf("runner calls = %d", runner.calls)
	}
}

func TestPipelineTestRun_HappyPath_WithSecret_MintsToken(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "ok"}, nil)
	h.SetSaveTokenSecret([]byte("super-secret-signing-key"))

	rr := covPipeTestRun(t, h, `{"definition":`+covPipeDef+`,"author_crew_id":"crew_a"}`,
		&AuthUser{ID: "user_1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status    string `json:"status"`
		SaveToken string `json:"save_token"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "COMPLETED" {
		t.Fatalf("status = %q", out.Status)
	}
	if out.SaveToken == "" {
		t.Error("save_token missing despite signing secret + COMPLETED run")
	}
}

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
