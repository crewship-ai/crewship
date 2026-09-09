package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFixtureAPICannotPublishOrProduceValidationToken(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	body := []byte(`{"definition":{"name":"fixture-api","steps":[{"id":"a","type":"transform","transform":{"input":"hello","expression":"."}}]},"step_id":"a"}`)
	for _, role := range []string{"MEMBER", "MANAGER"} {
		r := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/fixture_test", bytes.NewReader(body)), ws), user, role)
		w := httptest.NewRecorder()
		h.FixtureTest(w, r)
		if role == "MEMBER" {
			if w.Code != 403 {
				t.Fatalf("member: %d", w.Code)
			}
			continue
		}
		if w.Code != http.StatusOK {
			t.Fatalf("fixture: %d %s", w.Code, w.Body)
		}
		var result map[string]any
		json.Unmarshal(w.Body.Bytes(), &result)
		if result["execution_mode"] != "fixtures" || result["save_token"] != nil || result["run_id"] != nil {
			t.Fatal(result)
		}
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE workspace_id=? AND slug='fixture-api'`, ws).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fixture changed live recipe: %d %v", count, err)
	}
}
