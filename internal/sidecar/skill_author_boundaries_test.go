package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSkillAuthorScopesStagingAndPreservesReviewRefusals(t *testing.T) {
	const body = `{"content":"---\nname: synthetic-fixture\ndescription: fixture\n---\n# Synthetic"}`
	for _, status := range []int{http.StatusCreated, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/api/v1/internal/skills/author" || r.URL.Query().Get("workspace_id") != "workspace &" || r.URL.Query().Get("crew_id") != "crew &" || r.Header.Get("X-Internal-Token") != "ipc-token" {
					t.Errorf("incorrect author scope: %s %s", r.Method, r.URL)
				}
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != body {
					t.Errorf("skill source changed: %q %v", got, err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"state":"pending_review"}`)
			}))
			defer host.Close()
			s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: host.URL, Token: "ipc-token", WorkspaceID: "workspace &", CrewID: "crew &"})
			r := httptest.NewRequest("POST", "/skills/author?workspace_id=foreign&crew_id=foreign", strings.NewReader(body))
			r.Host = "127.0.0.1:9119"
			r.RemoteAddr = "127.0.0.1:50000"
			w := httptest.NewRecorder()
			s.buildHandler(nil).ServeHTTP(w, r)
			if calls.Load() != 1 || w.Code != status || strings.TrimSpace(w.Body.String()) != `{"state":"pending_review"}` {
				t.Fatalf("staging result changed: calls=%d status=%d body=%s", calls.Load(), w.Code, w.Body.String())
			}
		})
	}
	s := newRoutineMCPTestServer(t, nil)
	w := httptest.NewRecorder()
	s.handleSkillAuthor(w, httptest.NewRequest("POST", "/skills/author", strings.NewReader(body)))
	if w.Code != 503 {
		t.Fatalf("missing IPC admitted authoring: %d", w.Code)
	}
}
