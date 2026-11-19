package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #5542: an ENDPOINT_URL projection is admitted only with the revision of the
// exact Vault value the sidecar holds. The revision must survive the boot
// payload, reach the admission callback and be sent to the host; a credential
// without one must not invent it.
func TestEndpointSourceRevisionReachesHostAdmission(t *testing.T) {
	var boot Credential
	if err := json.Unmarshal([]byte(`{"id":"ep","provider":"OPENAI_COMPAT","token":"synthetic","source_revision":"rev-1"}`), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.SourceRevision != "rev-1" {
		t.Fatalf("boot payload dropped source_revision: %+v", boot)
	}

	for _, tc := range []struct {
		name string
		cred *Credential
		want string
	}{
		{"endpoint projection", &Credential{ID: "ep", Provider: "OPENAI_COMPAT", SourceRevision: "rev-1"}, "rev-1"},
		{"ordinary key", &Credential{ID: "key", Provider: "OPENAI"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]string
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"allowed":true}`))
			}))
			defer host.Close()
			s := &Server{ipc: &IPCConfig{BaseURL: host.URL, Token: "synthetic", WorkspaceID: "ws", AgentID: "agent"}}
			p := &Proxy{onLLMAdmission: s.buildLLMAdmission()}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(context.Background())
			if !p.admitLLM(httptest.NewRecorder(), req, "agent", tc.cred, "OPENAI_COMPAT") {
				t.Fatal("admission refused")
			}
			if got["credential_id"] != tc.cred.ID {
				t.Fatalf("credential_id = %q, want %q", got["credential_id"], tc.cred.ID)
			}
			rev, present := got["source_revision"]
			if rev != tc.want || (tc.want == "" && present) {
				t.Fatalf("source_revision = %q (present=%v), want %q", rev, present, tc.want)
			}
		})
	}
}
