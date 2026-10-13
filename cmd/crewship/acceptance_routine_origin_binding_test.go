package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptance_RoutineOriginBindingUsesReviewedCAS(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			w.Write([]byte(`[{"id":"cworkspacetest000000001","slug":"ws_test","name":"Synthetic"}]`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/pipelines/daily-report/origin-binding") {
			t.Errorf("path=%s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		calls++
		if r.Method == "POST" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["expected_origin_id"] != nil || body["reason"] != "Reviewed" || body["action"] != "clear" {
				t.Errorf("CAS=%+v", body)
			}
		}
		w.Write([]byte(`{"pipeline_id":"pipeline","origin_id":null,"schedules":[]}`))
	}))
	defer server.Close()
	for _, args := range [][]string{{"routine", "origin-binding", "get", "daily-report"}, {"routine", "origin-binding", "clear", "daily-report", "--expected-origin", "null", "--reason", "Reviewed", "--format", "yaml"}} {
		out, err := runChatListCLI(t, server.URL, args...)
		if err != nil || !strings.Contains(out, "pipeline_id") {
			t.Fatalf("%v %s", err, out)
		}
	}
	before := calls
	if out, err := runChatListCLI(t, server.URL, "routine", "origin-binding", "clear", "daily-report", "--reason", "Reviewed"); err == nil {
		t.Fatalf("missing CAS accepted: %s", out)
	}
	if calls != before {
		t.Fatal("missing CAS reached server")
	}
}
