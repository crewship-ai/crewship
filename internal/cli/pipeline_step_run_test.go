package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStepRunRoutine_PostsFixtureAndDecodesVerdict(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/workspaces/" + testWorkspaceCUID + "/pipelines/parse-invoice/step_run"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"step_id": "extract", "step_type": "agent_run", "adapter": "claude_code",
			"model": "claude-haiku-4-5", "output": `{"total":42}`, "valid": true,
			"cost_usd": 0.0021, "tokens_in": 120, "tokens_out": 40, "duration_ms": 4210,
			"simulated": true,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	res, err := c.StepRunRoutine(context.Background(), "parse-invoice", "extract",
		map[string]any{"name": "sample.pdf"}, map[string]string{"parse": "{\"total\":42}"}, "fast")
	if err != nil {
		t.Fatalf("StepRunRoutine: %v", err)
	}
	// Request carried the fixture + tier override.
	if gotBody["step_id"] != "extract" || gotBody["tier_override"] != "fast" {
		t.Errorf("request body = %+v", gotBody)
	}
	if inputs, _ := gotBody["inputs"].(map[string]any); inputs["name"] != "sample.pdf" {
		t.Errorf("inputs not forwarded: %+v", gotBody["inputs"])
	}
	if so, _ := gotBody["step_outputs"].(map[string]any); so["parse"] != `{"total":42}` {
		t.Errorf("step_outputs not forwarded: %+v", gotBody["step_outputs"])
	}
	// Response decoded.
	if !res.Valid || res.Model != "claude-haiku-4-5" || !res.Simulated || res.CostUSD != 0.0021 {
		t.Errorf("decoded result = %+v", res)
	}
}

func TestStepRunRoutine_OmitsEmptyOptionalFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"step_id": "a", "valid": true})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	if _, err := c.StepRunRoutine(context.Background(), "demo", "a", nil, nil, ""); err != nil {
		t.Fatalf("StepRunRoutine: %v", err)
	}
	if _, ok := gotBody["inputs"]; ok {
		t.Error("empty inputs should be omitted from the body")
	}
	if _, ok := gotBody["tier_override"]; ok {
		t.Error("empty tier_override should be omitted from the body")
	}
	if _, ok := gotBody["step_outputs"]; ok {
		t.Error("empty step_outputs should be omitted from the body")
	}
}

func TestStepRunRoutine_Validation(t *testing.T) {
	c := NewClient("http://127.0.0.1:0", "tok", testWorkspaceCUID)
	if _, err := c.StepRunRoutine(context.Background(), "", "a", nil, nil, ""); err == nil {
		t.Error("expected error for empty slug")
	}
	if _, err := c.StepRunRoutine(context.Background(), "demo", "", nil, nil, ""); err == nil {
		t.Error("expected error for empty step id")
	}
	// No workspace on the client → error.
	cNoWs := NewClient("http://127.0.0.1:0", "tok", "")
	if _, err := cNoWs.StepRunRoutine(context.Background(), "demo", "a", nil, nil, ""); err == nil ||
		!strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}
