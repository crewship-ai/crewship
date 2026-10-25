package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func documentAccessFixture(t *testing.T) (*collaborationAgentTestFixture, *Router, *http.Request) {
	t.Helper()
	x := newCollaborationAgentTestFixture(t)
	router := &Router{db: x.f.rig.db, pages: x.f.pages, mooringHTTP: &collaborationHTTPManager{pages: x.f.pages, fence: x.fence, agentMandates: x.store}}
	req := httptest.NewRequest("GET", "/api/v1/chats/unused/document-access", nil).WithContext(x.human)
	req.SetPathValue("chatId", x.f.a.ChatID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, x.f.a.WorkspaceID)
	return x, router, req.WithContext(ctx)
}

type documentAccessDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (*documentAccessDeadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func documentAccessCall(router *Router, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.chatDocumentAccessIngress(http.HandlerFunc(router.chatDocumentAccess)).ServeHTTP(&documentAccessDeadlineRecorder{w}, req)
	return w
}

func documentAccessDecode(t *testing.T, w *httptest.ResponseRecorder) []chatDocumentAccess {
	t.Helper()
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("status=%d body=%s headers=%v", w.Code, w.Body.String(), w.Header())
	}
	var body chatDocumentAccessResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Documents == nil {
		t.Fatal(w.Body.String(), err)
	}
	return body.Documents
}

func TestChatDocumentAccessSeparatesDocumentGrantAndTask(t *testing.T) {
	x, router, req := documentAccessFixture(t)
	rows := documentAccessDecode(t, documentAccessCall(router, req))
	if len(rows) != 1 || rows[0].PageID != x.key.ArtifactID || rows[0].Grant != "write" || rows[0].TaskStatus != "no_active_task" || len(rows[0].Tasks) != 0 || rows[0].Provenance != "unknown" {
		t.Fatal("standing grant fabricated task or origin", rows)
	}
	x.attach(t, x.prepare(t))
	rows = documentAccessDecode(t, documentAccessCall(router, req))
	if len(rows) != 1 || rows[0].TaskStatus != "active" || len(rows[0].Tasks) != 1 || !rows[0].Tasks[0].CanWrite {
		t.Fatal("canonical active task missing", rows)
	}
	router.mooringHTTP.agentMandates = nil
	rows = documentAccessDecode(t, documentAccessCall(router, req))
	if len(rows) != 1 || rows[0].TaskStatus != "unavailable" || len(rows[0].Tasks) != 0 {
		t.Fatal("missing task source became effective permission", rows)
	}
	router.mooringHTTP = nil
	rows = documentAccessDecode(t, documentAccessCall(router, req))
	if len(rows) != 1 || rows[0].TaskStatus != "unavailable" {
		t.Fatal("disabled Mooring fabricated task", rows)
	}
}

func TestChatDocumentAccessCurrentHumanAndPageAudience(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		status    int
	}{
		{"session revoked", `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z'`, 403},
		{"membership removed", `DELETE FROM workspace_members`, 403},
		{"human restricted", `UPDATE workspace_members SET role='MEMBER'; UPDATE workspace_members SET access_mode='restricted'`, 403},
		{"chat deleted", `DELETE FROM chats`, 404},
		{"agent removed", `UPDATE agents SET deleted_at='2026-01-01T00:00:00Z'`, 404},
		{"agent grant revoked", `DELETE FROM page_grants`, 200},
		{"private page", `UPDATE pages SET private_audience=1 WHERE id='agent-shared-page'`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, router, req := documentAccessFixture(t)
			mustExec(t, x.f.rig.db, tc.sql)
			w := documentAccessCall(router, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if strings.Contains(w.Body.String(), x.key.ArtifactID) || strings.Contains(w.Body.String(), `"Shared"`) {
				t.Fatal("unavailable document metadata disclosed", w.Body.String())
			}
		})
	}
}

func TestChatDocumentAccessChatReachDoesNotGrantPageReach(t *testing.T) {
	x, router, req := documentAccessFixture(t)
	pagesSeedUser(t, x.f.pages, x.f.a.WorkspaceID, "different-owner", "different@example.invalid", "MEMBER")
	// The chat owner stays a member with chat reach, but has no Page grant.
	mustExec(t, x.f.rig.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, x.f.a.HumanUserID)
	mustExec(t, x.f.rig.db, `UPDATE pages SET owner_user_id='different-owner' WHERE id='agent-shared-page'`)
	// A now-valid different issuer keeps the agent grant alive.
	mustExec(t, x.f.rig.db, `UPDATE page_grants SET granted_by_user_id='different-owner' WHERE page_id='agent-shared-page'`)
	rows := documentAccessDecode(t, documentAccessCall(router, req))
	if len(rows) != 0 {
		t.Fatal("chat reach or stale context role disclosed Page", rows)
	}
}

// A controlled stalled writer exercises the actual ResponseController deadline
// and fence lifetime. It is not browser or real-network acceptance.
type documentAccessStalledWriter struct {
	header   http.Header
	mu       sync.Mutex
	deadline time.Time
	started  chan struct{}
	once     sync.Once
}

func (w *documentAccessStalledWriter) Header() http.Header { return w.header }
func (*documentAccessStalledWriter) WriteHeader(int)       {}
func (w *documentAccessStalledWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}
func (w *documentAccessStalledWriter) Write([]byte) (int, error) {
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	w.once.Do(func() { close(w.started) })
	if deadline.IsZero() {
		return 0, os.ErrInvalid
	}
	<-time.After(time.Until(deadline))
	return 0, os.ErrDeadlineExceeded
}
func (*documentAccessStalledWriter) FlushError() error { return os.ErrDeadlineExceeded }

func TestChatDocumentAccessBoundsStalledWriterBeforeReleasingIngress(t *testing.T) {
	_, router, req := documentAccessFixture(t)
	w := &documentAccessStalledWriter{header: make(http.Header), started: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		router.chatDocumentAccessIngress(http.HandlerFunc(router.chatDocumentAccess)).ServeHTTP(w, req)
	}()
	select {
	case <-w.started:
	case <-time.After(4 * time.Second):
		t.Fatal("handler did not reach bounded response")
	}
	retired := make(chan struct{})
	go func() { router.mooringHTTP.fence.SuspendBeforeRestore(); close(retired) }()
	select {
	case <-retired:
		t.Fatal("ingress lease released while the response write was stalled")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("HTTP write deadline did not bound the handler")
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("stalled writer retained the restore fence")
	}
}

func TestChatDocumentAccessRefusesUnboundedResponseWriter(t *testing.T) {
	_, router, req := documentAccessFixture(t)
	w := httptest.NewRecorder() // Does not support SetWriteDeadline.
	router.chatDocumentAccessIngress(http.HandlerFunc(router.chatDocumentAccess)).ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "Shared") {
		t.Fatal("unsupported write bound exposed document metadata", w.Code, w.Body.String())
	}
}
