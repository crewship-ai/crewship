package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyAdmissionUsesOnlyAuthenticatedCurrentRun(t *testing.T) {
	for _, run := range []string{"run-one", "run-two", ""} {
		t.Run("context-"+run, func(t *testing.T) {
			var got map[string]string
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"allowed":true}`))
			}))
			defer host.Close()
			s := &Server{ipc: &IPCConfig{BaseURL: host.URL, Token: "synthetic", WorkspaceID: "ws", AgentID: "boot-agent", RunID: "boot-run"}}
			ctx := context.Background()
			if run != "" {
				ctx = context.WithValue(ctx, authenticatedLLMRunKey{}, run)
			}
			if err := s.buildLLMAdmission()(ctx, "current-agent", "credential", "OPENAI"); err != nil {
				t.Fatal(err)
			}
			if got["agent_id"] != "current-agent" || got["credential_id"] != "credential" || got["run_id"] != run {
				t.Fatalf("wrong request identity: %+v", got)
			}
			if run == "" {
				if _, present := got["run_id"]; present {
					t.Fatal("legacy call invented a run identity")
				}
			}
			if got["run_id"] == "boot-run" {
				t.Fatal("concurrent caller inherited sidecar boot run")
			}
		})
	}
}
