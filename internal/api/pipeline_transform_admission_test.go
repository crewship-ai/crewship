package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPipelineSave_RejectsInvalidTransformBeforePersistence(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	for _, expression := range []string{`{total: (.qty * 2)}`, `.a + .b`, `.items[0foo]`, `.qty`} {
		definition := map[string]any{"name": "transform-admission", "agentless": true, "steps": []any{
			map[string]any{"id": "extract", "type": "transform", "transform": map[string]any{"input": `{"qty":2}`, "expression": expression}},
		}}
		body := covPCSaveBody("transform-admission", "", map[string]any{"definition": definition})
		req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Save(rr, req)
		var count int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE workspace_id=? AND slug='transform-admission'`, wsID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if expression == ".qty" {
			if rr.Code != http.StatusCreated || count != 1 {
				t.Fatalf("valid transform did not save: %d %s (rows=%d)", rr.Code, rr.Body.String(), count)
			}
			continue
		}
		if rr.Code != http.StatusUnprocessableEntity || count != 0 {
			t.Fatalf("invalid transform was not rejected before persistence: %d (rows=%d)", rr.Code, count)
		}
		var problem map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &problem); err != nil || !strings.Contains(rr.Body.String(), "extract") || !strings.Contains(rr.Body.String(), "expression") {
			t.Fatalf("missing expression diagnostic: %s", rr.Body.String())
		}
	}
}
