package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactCreationTransportRequiresRunBearerAndForwardsCollection(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	payload := `{"slug":"report","name":"Report","html":"<p>source</p>","mutation_id":"one"}`
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/artifacts" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "crew-ipc" {
			t.Errorf("incorrect creation transport %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != payload {
			t.Errorf("creation source changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"page_id":"page","slug":"report","revision":1}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "crew-ipc"
	for _, bad := range []string{"", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"), token + "bad"} {
		req := httptest.NewRequest("POST", "/artifacts", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+bad)
		rr := httptest.NewRecorder()
		s.handleArtifactCreate(rr, req)
		if rr.Code != 403 {
			t.Fatalf("bad run bearer status=%d", rr.Code)
		}
	}
	req := httptest.NewRequest("POST", "/artifacts", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.handleArtifactCreate(rr, req)
	if rr.Code != 201 || calls != 1 || rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("creation response status=%d calls=%d body=%s", rr.Code, calls, rr.Body.String())
	}
}
