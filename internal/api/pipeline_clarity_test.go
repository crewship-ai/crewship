package api

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestPipelineClarityRejectsInputBeforeAgent(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	var definition string
	if err := db.QueryRowContext(t.Context(), `SELECT definition_json FROM pipelines WHERE slug='demo'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(definition), &d); err != nil {
		t.Fatal(err)
	}
	d["inputs"] = []map[string]any{{"name": "coverage", "type": "number", "min": 0, "max": 1}, {"name": "dir", "type": "string", "format": "absolute_path"}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE pipelines SET definition_json=? WHERE slug='demo'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	runner := &stubRunner{output: "should not execute"}
	h := NewPipelineHandler(db, slog.Default(), runner, nil)
	for _, body := range []string{`{"inputs":{"coverage":70,"dir":"/crew/shared"}}`, `{"inputs":{"coverage":0.7,"dir":"../private"}}`} {
		req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(body)), "ws_smoke")
		req.SetPathValue("slug", "demo")
		w := httptest.NewRecorder()
		h.Run(w, req)
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("expected input rejection, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "input") {
			t.Fatalf("unrelated rejection: %s", w.Body.String())
		}
	}
	if runner.calls != 0 {
		t.Fatalf("invalid input invoked agent %d times", runner.calls)
	}
}

func TestPipelineClarityProjectionDetailOnly(t *testing.T) {
	p := &pipeline.Pipeline{DefinitionJSON: `{"name":"demo","steps":[{"id":"extract","type":"agent_run","agent_slug":"worker","outcomes":{"required":true,"grader_agent_slug":"checker","criteria":[{"name":"matches","rule":"matches PDF"}]}}]}`}
	if toPipelineResponse(p, false).Behavior != nil {
		t.Fatal("list should stay lightweight")
	}
	response := toPipelineResponse(p, true)
	if response.Behavior == nil || len(response.Behavior.Steps) != 1 {
		t.Fatal("missing detail rules")
	}
	checks := strings.Join(response.Behavior.Steps[0].Checks, " ")
	if !strings.Contains(checks, "Required") || !strings.Contains(checks, "matches PDF") {
		t.Fatalf("lost checker contract: %s", checks)
	}
}
