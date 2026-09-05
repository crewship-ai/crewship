package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The terminal-children rule (#2377) answers 409 with "Retry with
// ?force=true"; the CLI must be able to follow that instruction (#2381).
type issueUpdateStub struct {
	mu      sync.Mutex
	patches []string // path+query of every PATCH seen
}

func (s *issueUpdateStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/"):
			s.mu.Lock()
			s.patches = append(s.patches, r.URL.Path+"?"+r.URL.RawQuery)
			s.mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"iss_1","identifier":"ENG-1","status":"DONE"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/"):
			_, _ = io.WriteString(w, `{"id":"iss_1","identifier":"ENG-1","title":"t","status":"IN_PROGRESS","priority":"none","crew_id":"crew_1","crew_slug":"eng","mission_type":"issue","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z","labels":[]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_IssueUpdate_ForceSendsTheForceQuery(t *testing.T) {
	stub := &issueUpdateStub{}
	srv := stub.start(t)
	out, err := runIssueRunsCLI(t, srv.URL, "issue", "update", "ENG-1", "--status", "DONE", "--force")
	if err != nil {
		t.Fatalf("issue update --force: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.patches) != 1 || !strings.Contains(stub.patches[0], "force=true") {
		t.Fatalf("PATCH must carry ?force=true, got %q", stub.patches)
	}
}

func TestAcceptance_IssueUpdate_PlainUpdateSendsNoForce(t *testing.T) {
	stub := &issueUpdateStub{}
	srv := stub.start(t)
	out, err := runIssueRunsCLI(t, srv.URL, "issue", "update", "ENG-1", "--status", "DONE")
	if err != nil {
		t.Fatalf("issue update: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.patches) != 1 || strings.Contains(stub.patches[0], "force") {
		t.Fatalf("a plain update must not send force, got %q", stub.patches)
	}
}
