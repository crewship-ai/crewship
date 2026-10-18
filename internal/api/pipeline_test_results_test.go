package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ODCH-RT-13: a Test run's result is kept against the definition it tested,
// so the draft and the versions holding exactly that definition can say
// "Test run passed"; an edited definition has no result until it is tested.

// saveDraftOf saves doc as the draft of the published routine "tr": a draft of
// a published routine carries the base the store reads for it.
func saveDraftOf(t *testing.T, db *sql.DB, doc []byte) {
	t.Helper()
	base, err := pipeline.NewStore(db).GetDraft(t.Context(), "ws_smoke", "tr")
	if err != nil {
		t.Fatalf("draft baseline: %v", err)
	}
	if base.ID != "" {
		if _, err := db.Exec(`DELETE FROM pipeline_drafts WHERE id = ?`, base.ID); err != nil {
			t.Fatal(err)
		}
		base, _ = pipeline.NewStore(db).GetDraft(t.Context(), "ws_smoke", "tr")
	}
	base.Document, base.UpdatedBy = doc, "user_tr"
	if _, err := pipeline.NewStore(db).SaveDraft(t.Context(), *base); err != nil {
		t.Fatalf("save draft: %v", err)
	}
}

const testResultDef = `{"name":"tr-routine","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`

func testRunOnce(t *testing.T, h *PipelineHandler, def string) {
	t.Helper()
	body := `{"definition":` + def + `,"sample_inputs":{}}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = req.WithContext(context.WithValue(withWorkspace(req.Context(), "ws_smoke", "MANAGER"), ctxUser, &AuthUser{ID: "user_tr"}))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	h.TestRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("test_run = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTestResults_DraftAndVersions(t *testing.T) {
	db := openSmokeDB(t)
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "unused-in-dry-run"}, nil)
	h.SetSaveTokenSecret([]byte("test-secret-32-bytes-long-padxxx"))
	seedSmokePipelineDef(t, db, "tr", testResultDef)

	draftOf := func(definition string) *pipeline.Draft {
		t.Helper()
		doc, _ := json.Marshal(map[string]any{"slug": "tr", "name": "tr", "definition": json.RawMessage(definition)})
		if _, err := db.Exec(`DELETE FROM pipeline_drafts WHERE workspace_id='ws_smoke' AND slug='tr'`); err != nil {
			t.Fatal(err)
		}
		saveDraftOf(t, db, doc)
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("slug", "tr")
		req = withWorkspaceUser(req, "user_tr", "ws_smoke", "MANAGER")
		rr := httptest.NewRecorder()
		h.GetDraft(rr, req)
		var d pipeline.Draft
		if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil || rr.Code != 200 {
			t.Fatalf("get draft: %d %v %s", rr.Code, err, rr.Body.String())
		}
		return &d
	}
	versions := func() []struct {
		Version    int `json:"version"`
		TestResult *struct {
			Passed bool   `json:"passed"`
			Status string `json:"status"`
		} `json:"test_result"`
	} {
		t.Helper()
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("slug", "tr")
		req = withWorkspaceUser(req, "user_tr", "ws_smoke", "MANAGER")
		rr := httptest.NewRecorder()
		h.ListVersions(rr, req)
		var out []struct {
			Version    int `json:"version"`
			TestResult *struct {
				Passed bool   `json:"passed"`
				Status string `json:"status"`
			} `json:"test_result"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("versions: %v %s", err, rr.Body.String())
		}
		return out
	}

	// Nothing tested yet: no result anywhere.
	if d := draftOf(testResultDef); d.TestResult != nil {
		t.Fatalf("untested draft has a result: %+v", d.TestResult)
	}
	if v := versions(); len(v) != 1 || v[0].TestResult != nil {
		t.Fatalf("untested version has a result: %+v", v)
	}

	testRunOnce(t, h, testResultDef)

	d := draftOf(testResultDef)
	if d.TestResult == nil || !d.TestResult.Passed || d.TestResult.Status != "DRY_RUN_OK" || d.TestResult.TestedBy != "user_tr" || d.TestResult.TestedAt == "" {
		t.Fatalf("draft after the test = %+v", d.TestResult)
	}
	if v := versions(); v[0].TestResult == nil || !v[0].TestResult.Passed {
		t.Fatalf("the version holding the tested definition = %+v", v)
	}

	// An edit has no result until it is tested.
	edited := strings.Replace(testResultDef, `"prompt":"hi"`, `"prompt":"hello"`, 1)
	if d := draftOf(edited); d.TestResult != nil {
		t.Fatalf("edited draft shows the old result: %+v", d.TestResult)
	}
}

func TestTestResults_Store(t *testing.T) {
	db := openSmokeDB(t)
	s := pipeline.NewStore(db)
	if err := s.RecordTestResult(t.Context(), "ws_smoke", "h1", true, "DRY_RUN_OK", "u"); err != nil {
		t.Fatal(err)
	}
	// The latest test of a definition replaces the earlier one.
	if err := s.RecordTestResult(t.Context(), "ws_smoke", "h1", false, "FAILED", "u2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.TestResults(t.Context(), "ws_smoke", []string{"h1", "h2"})
	if err != nil {
		t.Fatal(err)
	}
	if r := got["h1"]; r == nil || r.Passed || r.Status != "FAILED" || r.TestedBy != "u2" || got["h2"] != nil {
		t.Fatalf("results = %+v", got)
	}
	if other, _ := s.TestResults(t.Context(), "ws_other", []string{"h1"}); len(other) != 0 {
		t.Fatalf("another workspace sees the result: %+v", other)
	}
	for _, bad := range [][2]string{{"", "h"}, {"ws_smoke", ""}} {
		if err := s.RecordTestResult(t.Context(), bad[0], bad[1], true, "x", ""); err == nil {
			t.Errorf("RecordTestResult(%q,%q) must be refused", bad[0], bad[1])
		}
	}
}

// The list and the detail carry the draft's test result in the draft summary,
// so the routine page can say "Test run passed" without a second request.
func TestTestResults_DraftSummaryOnTheList(t *testing.T) {
	db := openSmokeDB(t)
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "unused-in-dry-run"}, nil)
	h.SetSaveTokenSecret([]byte("test-secret-32-bytes-long-padxxx"))
	seedSmokePipelineDef(t, db, "tr", testResultDef)
	edited := strings.Replace(testResultDef, `"prompt":"hi"`, `"prompt":"hello"`, 1)
	doc, _ := json.Marshal(map[string]any{"slug": "tr", "name": "tr", "definition": json.RawMessage(edited)})
	saveDraftOf(t, db, doc)
	draftOnList := func() *pipelineDraftRef {
		t.Helper()
		req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/workspaces/ws_smoke/pipelines", nil), "ws_smoke")
		rr := httptest.NewRecorder()
		h.List(rr, req)
		var rows []pipelineResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].Draft == nil {
			t.Fatalf("list: %v %s", err, rr.Body.String())
		}
		return rows[0].Draft
	}
	if d := draftOnList(); d.TestResult != nil {
		t.Fatalf("untested draft has a result: %+v", d.TestResult)
	}
	testRunOnce(t, h, testResultDef) // the live definition, not the draft's
	if d := draftOnList(); d.TestResult != nil {
		t.Fatalf("a test of other content must not show on the draft: %+v", d.TestResult)
	}
	testRunOnce(t, h, edited)
	if d := draftOnList(); d.TestResult == nil || !d.TestResult.Passed {
		t.Fatalf("draft after its own test = %+v", d.TestResult)
	}
}
