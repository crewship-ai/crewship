package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Controlled durable attribution DATA, not an actual Rust/model Saved claim.
func nativeProjectionFixture(t *testing.T, originalAgent ...string) (*collaborationAgentTestFixture, *Router, *http.Request) {
	t.Helper()
	x, r, req := documentAccessFixture(t)
	agent := x.f.a.AgentID
	if len(originalAgent) > 0 {
		agent = originalAgent[0]
	}
	mustExec(t, x.f.rig.db, `INSERT INTO mooring_collab_artifact_edits(page_id,workspace_id,application_id,change_id,membership_hash,first_revision,last_revision,host_session_id,actor_kind,human_user_id,agent_id,crew_id,chat_id,message_id,work_id,run_id,attempt_number,attempt_generation) VALUES(?,?,'wordcraft','projection-change','original-membership','1','1','original-session','agent',?,?,?,?,?,'original-work','original-run',1,1)`, x.key.ArtifactID, x.key.WorkspaceID, x.f.a.HumanUserID, agent, x.f.a.CrewID, x.f.a.ChatID, "original-message")
	return x, r, req
}
func projectionDecode(t *testing.T, w *httptest.ResponseRecorder) nativeProvenanceResponse {
	t.Helper()
	var out nativeProvenanceResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Artifacts == nil || !out.ArtifactsPartial {
		t.Fatal(w.Code, w.Body.String())
	}
	return out
}
func pageProjectionCall(r *Router, req *http.Request) *httptest.ResponseRecorder {
	copy := req.Clone(req.Context())
	copy.SetPathValue("slug", "@agent-shared-page")
	w := httptest.NewRecorder()
	r.chatDocumentAccessIngress(http.HandlerFunc(r.nativeDocumentProvenance)).ServeHTTP(&documentAccessDeadlineRecorder{w}, copy)
	return w
}
func TestNativeEditProjectionSeparatesHistoryFromGrantsAndTasks(t *testing.T) {
	x, r, req := nativeProjectionFixture(t)
	out := projectionDecode(t, documentAccessCall(r, req))
	if len(out.Artifacts) != 1 || out.Artifacts[0].PageID != x.key.ArtifactID || out.Artifacts[0].ChatID != x.f.a.ChatID || out.Artifacts[0].MessageID != "original-message" || out.Artifacts[0].Provenance != "agent_edited" {
		t.Fatal(out)
	}
	if documentAccessDecode(t, documentAccessCall(r, req))[0].TaskStatus != "no_active_task" {
		t.Fatal("historical metadata created task")
	}
	// Standing grant withdrawal does not erase history for the independently
	// authorized human and source-chat reader; it confers no continuing task.
	mustExec(t, x.f.rig.db, `DELETE FROM page_grants`)
	w := documentAccessCall(r, req)
	out = projectionDecode(t, w)
	if len(out.Artifacts) != 1 || len(documentAccessDecode(t, w)) != 0 {
		t.Fatal("history confused with standing grant", w.Body.String())
	}
	if page := projectionDecode(t, pageProjectionCall(r, req)); len(page.Artifacts) != 1 {
		t.Fatal(page)
	}
}
func TestNativeEditProjectionRefusesCurrentRightsAndSourceChanges(t *testing.T) {
	for _, mode := range []string{"page-hidden", "chat-hidden", "source-revoked", "foreign-workspace", "wrong-original-agent", "missing-record"} {
		t.Run(mode, func(t *testing.T) {
			var x *collaborationAgentTestFixture
			var r *Router
			var req *http.Request
			if mode == "wrong-original-agent" {
				x, r, req = nativeProjectionFixture(t, "other-agent")
			} else {
				x, r, req = nativeProjectionFixture(t)
			}
			switch mode {
			case "page-hidden":
				mustExec(t, x.f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, x.key.ArtifactID)
			case "chat-hidden":
				mustExec(t, x.f.rig.db, `DELETE FROM chats WHERE id=?`, x.f.a.ChatID)
			case "source-revoked":
				mustExec(t, x.f.rig.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z'`)
			case "foreign-workspace":
				req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, "other-workspace"))
			case "missing-record":
				mustExec(t, x.f.rig.db, `DELETE FROM mooring_collab_artifact_edits`)
			}
			for _, w := range []*httptest.ResponseRecorder{documentAccessCall(r, req), pageProjectionCall(r, req)} {
				if strings.Contains(w.Body.String(), "original-message") || strings.Contains(w.Body.String(), "original-session") {
					t.Fatal("protected metadata escaped", w.Code, w.Body.String())
				}
				if w.Code == 200 && len(projectionDecode(t, w).Artifacts) != 0 {
					t.Fatal("denied edit leaked", w.Body.String())
				}
			}
		})
	}
}
func TestNativeEditProjectionBoundsAndDeduplicatesRecentRows(t *testing.T) {
	x, r, req := nativeProjectionFixture(t)
	for i := 2; i <= 70; i++ {
		mustExec(t, x.f.rig.db, `INSERT INTO mooring_collab_artifact_edits SELECT page_id,workspace_id,application_id,?,membership_hash,?,?,host_session_id,actor_kind,human_user_id,agent_id,crew_id,chat_id,message_id,work_id,run_id,attempt_number,attempt_generation,created_at FROM mooring_collab_artifact_edits WHERE change_id='projection-change'`, fmt.Sprintf("edit-%d", i), fmt.Sprint(i), fmt.Sprint(i))
	}
	out := projectionDecode(t, documentAccessCall(r, req))
	if len(out.Artifacts) != 1 || out.Artifacts[0].LastRevision != "70" || !out.ArtifactsPartial {
		t.Fatal("unbounded/stale/duplicate projection", out)
	}
}

type provenanceFlushWriter struct {
	*httptest.ResponseRecorder
	check func()
}

func (*provenanceFlushWriter) SetWriteDeadline(time.Time) error { return nil }
func (*provenanceFlushWriter) SetReadDeadline(time.Time) error  { return nil }
func (w *provenanceFlushWriter) FlushError() error              { w.check(); return nil }
func TestNativeEditProjectionHoldsSQLWriterAndIngressThroughFlush(t *testing.T) {
	x, r, req := nativeProjectionFixture(t)
	flushed := false
	w := &provenanceFlushWriter{ResponseRecorder: httptest.NewRecorder(), check: func() {
		flushed = true
		if r.mooringHTTP.fence.mu.TryLock() {
			r.mooringHTTP.fence.mu.Unlock()
			t.Error("flush escaped original ingress")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		if _, err := x.f.rig.db.ExecContext(ctx, `UPDATE pages SET name='changed-before-delivery' WHERE id=?`, x.key.ArtifactID); err == nil {
			t.Error("flush escaped SQL writer")
		}
	}}
	r.chatDocumentAccessIngress(http.HandlerFunc(r.chatDocumentAccess)).ServeHTTP(w, req)
	if w.Code != 200 || !flushed {
		t.Fatal("metadata not physically flushed", w.Code, flushed)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := x.f.rig.db.ExecContext(ctx, `UPDATE pages SET name='changed-after-delivery' WHERE id=?`, x.key.ArtifactID); err != nil {
		t.Fatal("completed projection retained writer", err)
	}
}

type provenanceExpiryWriter struct {
	*documentAccessDeadlineRecorder
	observed     time.Time
	readObserved time.Time
	onFlush      func()
}

func (w *provenanceExpiryWriter) SetWriteDeadline(v time.Time) error {
	if !v.IsZero() {
		w.observed = v
	}
	return nil
}
func TestNativeEditProjectionNeverExtendsOriginalCredentialHorizon(t *testing.T) {
	x, r, req := nativeProjectionFixture(t)
	expected := time.Now().Add(time.Second).UTC()
	user := *UserFromContext(req.Context())
	user.CredentialExpiresAt = expected
	source, err := captureOriginalHumanCredential(t.Context(), x.f.rig.db, &user, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	user.OriginalCredential = source
	ctx := context.WithValue(req.Context(), ctxUser, &user)
	ctx = auth.WithOriginalCredential(ctx, *source)
	w := &provenanceExpiryWriter{documentAccessDeadlineRecorder: &documentAccessDeadlineRecorder{httptest.NewRecorder()}}
	r.chatDocumentAccessIngress(http.HandlerFunc(r.chatDocumentAccess)).ServeHTTP(w, req.WithContext(ctx))
	if w.Code != 200 || !w.observed.Equal(expected) {
		t.Fatal("original credential horizon extended", w.Code, w.observed, expected)
	}
}

func (w *provenanceExpiryWriter) SetReadDeadline(v time.Time) error {
	w.readObserved = v
	return nil
}
func (w *provenanceExpiryWriter) FlushError() error {
	if w.onFlush != nil {
		w.onFlush()
	}
	return nil
}
func TestNativeEditProjectionClampsToCurrentSQLCredentialExpiry(t *testing.T) {
	x, r, req := nativeProjectionFixture(t)
	expected := time.Now().Add(2 * time.Second).UTC()
	source := auth.OriginalCredentialFromContext(req.Context())
	originalExpiry, err := source.Expiry()
	if err != nil || !originalExpiry.After(expected.Add(time.Minute)) {
		t.Fatal("fixture must retain a longer original credential", originalExpiry, err)
	}
	mustExec(t, x.f.rig.db, `UPDATE user_sessions SET expires_at=? WHERE id=?`, expected.Format(time.RFC3339Nano), source.ID)
	// Canonical expiry mutation also advances the user authority epoch, so
	// the old authenticated source is denied, rather than refreshed.
	if old := documentAccessCall(r, req); old.Code != http.StatusForbidden || strings.Contains(old.Body.String(), "original-message") {
		t.Fatal("expiry mutation retained the old original authority", old.Code, old.Body.String())
	}
	// Explicit synthetic accepted-source horizon for the shared helper's
	// defensive deadline contract. Canonical capture itself takes the min
	// expiry; this is not evidence of an expiry-mutation authorization bypass.
	user := *UserFromContext(req.Context())
	user.CredentialExpiresAt = originalExpiry
	current, err := captureOriginalHumanCredential(t.Context(), x.f.rig.db, &user, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	current.ExpiresAt = originalExpiry.UTC().Format(time.RFC3339Nano)
	user.OriginalCredential = current
	ctx := auth.WithOriginalCredential(context.WithValue(req.Context(), ctxUser, &user), *current)
	req = req.WithContext(ctx)
	w := &provenanceExpiryWriter{documentAccessDeadlineRecorder: &documentAccessDeadlineRecorder{httptest.NewRecorder()}}
	flushed := false
	r.chatDocumentAccessIngress(http.HandlerFunc(func(out http.ResponseWriter, in *http.Request) {
		read := r.beginNativeMetadataRead(out, in)
		if read == nil {
			t.Error("current shortened credential refused while live")
			return
		}
		defer read.close()
		w.onFlush = func() {
			flushed = true
			deadline, ok := read.ctx.Deadline()
			if !ok || !deadline.Equal(expected) || !w.observed.Equal(expected) || !w.readObserved.Equal(expected) {
				t.Errorf("current SQL expiry escaped physical flush: context=%v write=%v read=%v expected=%v", deadline, w.observed, w.readObserved, expected)
			}
			if r.mooringHTTP.fence.mu.TryLock() {
				r.mooringHTTP.fence.mu.Unlock()
				t.Error("expiry flush escaped ingress")
			}
		}
		read.finish(out, nativeProvenanceResponse{Artifacts: []nativeArtifactEdit{}, ArtifactsPartial: true})
	})).ServeHTTP(w, req)
	if w.Code != 200 || !flushed {
		t.Fatal("bounded projection not flushed", w.Code, flushed)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := x.f.rig.db.ExecContext(ctx, `UPDATE pages SET name=name WHERE id=?`, x.key.ArtifactID); err != nil {
		t.Fatal("expiry projection leaked writer", err)
	}
}
