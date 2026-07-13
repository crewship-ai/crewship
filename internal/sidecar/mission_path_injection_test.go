package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1045: an un-escaped mission id pulled from the URL path could smuggle a
// query string into the internal IPC URL. An agent sending
// GET /mission/<id>%3Fworkspace_id=<ownWS>%26crew_id=%26 decodes to a `?…` so
// the sidecar's trusted ?workspace_id=&crew_id= is appended as a bare `&`, the
// injected empty crew_id wins, and a sibling crew's mission (same workspace)
// leaks. The handler must reject such ids.

func TestHandleMissionStatus_RejectsPathInjection(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://127.0.0.1:1", Token: "t", CrewID: "crew-A", WorkspaceID: "ws"}, nil)
	// %3F → '?', %26 → '&' in the decoded URL.Path.
	req := httptest.NewRequest("GET", "/mission/MID%3Fworkspace_id=ws%26crew_id=%26", nil)
	w := httptest.NewRecorder()
	srv.handleMissionStatus(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("path-injection mission id must be 400, got %d: %s", w.Code, w.Body.String())
	}
}

// A legitimate (CUID) mission id proxies with the sidecar's OWN trusted scope
// query — the agent can't override workspace_id / crew_id.
func TestHandleMissionStatus_CleanID_UsesTrustedScope(t *testing.T) {
	var gotPath, gotRawQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "IN_PROGRESS"})
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", CrewID: "crew-A", WorkspaceID: "ws-1"}, nil)
	req := httptest.NewRequest("GET", "/mission/clh3mission0001abcd", nil)
	w := httptest.NewRecorder()
	srv.handleMissionStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("clean mission id should proxy: %d %s", w.Code, w.Body.String())
	}
	if gotPath != "/api/v1/internal/missions/clh3mission0001abcd" {
		t.Errorf("proxied path = %q", gotPath)
	}
	// The scope query must be the sidecar's trusted identity, not agent-supplied.
	if !strings.Contains(gotRawQuery, "crew_id=crew-A") || !strings.Contains(gotRawQuery, "workspace_id=ws-1") {
		t.Errorf("scope query = %q, want trusted workspace_id=ws-1 & crew_id=crew-A", gotRawQuery)
	}
}
