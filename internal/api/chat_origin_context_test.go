package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestChatOriginHeaderRequiresAuthenticatedHost(t *testing.T) {
	db := setupTestDB(t)
	h := NewInternalHandler(db, "origin-master", newTestLogger())
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"host", "origin-master", 200},
		{"workspace", internaltoken.DeriveWorkspaceToken("origin-master", "ws"), 403},
		{"crew", internaltoken.DeriveCrewToken("origin-master", "ws", "crew"), 403},
		{"unrecognized", "forged", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/internal/issues", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("X-Internal-Token", tc.token)
			req.Header.Set("X-Crewship-Chat-Origin", "server-origin")
			rr := httptest.NewRecorder()
			called := false
			h.requireInternal(withHostChatOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if runauthority.OriginIDFromContext(r.Context()) != "server-origin" {
					t.Error("host origin lost")
				}
				w.WriteHeader(200)
			}))).ServeHTTP(rr, req)
			if rr.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("%d called=%v %s", rr.Code, called, rr.Body.String())
			}
		})
	}
}

func TestChatOriginRejectsUnsupportedIssueWrites(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/internal/issues/issue/work", 403},
		{"DELETE", "/api/v1/internal/issues/issue", 403},
		{"POST", "/api/v1/internal/issues/issue/attachments", 403},
		{"PATCH", "/api/v1/internal/issues/issue", 204},
		{"POST", "/api/v1/internal/issues/issue/comments", 204},
		{"POST", "/api/v1/internal/issues/issue/show", 204},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-Crewship-Chat-Origin", "server-origin")
			rr := httptest.NewRecorder()
			withHostChatOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestPipelineExecutionMarkerRequiresAuthenticatedHost(t *testing.T) {
	db := setupTestDB(t)
	h := NewInternalHandler(db, "pipeline-master", newTestLogger())
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"host", "pipeline-master", 204}, {"workspace", internaltoken.DeriveWorkspaceToken("pipeline-master", "ws"), 403}, {"crew", internaltoken.DeriveCrewToken("pipeline-master", "ws", "crew"), 403}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/internal/issues", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("X-Internal-Token", tc.token)
			req.Header.Set("X-Crewship-Pipeline-Run", "actual-run")
			rr := httptest.NewRecorder()
			called := false
			h.requireInternal(withHostChatOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if hostPipelineRunID(r.Context()) != "actual-run" {
					t.Error("lost pipeline marker")
				}
				w.WriteHeader(204)
			}))).ServeHTTP(rr, req)
			if rr.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("marker accepted incorrectly: %d %t %s", rr.Code, called, rr.Body.String())
			}
		})
	}
}
