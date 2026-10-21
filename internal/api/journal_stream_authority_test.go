package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

type authorityStreamEmitter struct {
	*emitRecorder
	notify chan struct{}
}

func (e *authorityStreamEmitter) Notify() <-chan struct{} { return e.notify }

type authorityStreamWriter struct {
	*httptest.ResponseRecorder
	afterFlush func()
}

func (w *authorityStreamWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.afterFlush()
}

func TestJournalStreamRechecksAuthorityAfterInitialFlush(t *testing.T) {
	for _, state := range []string{"suspended", "empty suspension", "membership removed", "workspace deleted", "Guest elsewhere"} {
		t.Run(state, func(t *testing.T) {
			h, user, workspace, emitter := newJournalHandlerTest(t)
			notifier := &authorityStreamEmitter{emitRecorder: emitter, notify: make(chan struct{})}
			h.journal = notifier
			now := time.Now().UTC()
			seedJournalRow(t, h, "initial-entry", workspace, string(journal.EntryRunStarted), "info", "initial update", now.Add(-time.Minute))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/journal/stream", nil)
			request = withWorkspaceUser(request, user, workspace, "OWNER")
			recorder := httptest.NewRecorder()
			first := true
			writer := &authorityStreamWriter{ResponseRecorder: recorder, afterFlush: func() {
				if !first {
					cancel()
					return
				}
				first = false
				if !strings.Contains(recorder.Body.String(), "initial-entry") {
					t.Fatal("authorized initial replay missing")
				}
				switch state {
				case "suspended":
					execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-16T00:00:00Z' WHERE id=?`, user)
				case "empty suspension":
					execOrFatal(t, h.db, `UPDATE users SET suspended_at='' WHERE id=?`, user)
				case "membership removed":
					execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace)
				case "workspace deleted":
					execOrFatal(t, h.db, `UPDATE workspaces SET deleted_at='2026-10-16T00:00:00Z' WHERE id=?`, workspace)
				case "Guest elsewhere":
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, user)
					execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('stream-guest-workspace','Guest workspace','stream-guest-workspace')`)
					execOrFatal(t, h.db, `INSERT INTO workspace_members(id,user_id,workspace_id,role,access_mode) VALUES('stream-guest-member',?,'stream-guest-workspace','MEMBER','restricted')`, user)
				}
				seedJournalRow(t, h, "revoked-entry", workspace, string(journal.EntryRunStarted), "info", "later update", now.Add(time.Second))
				close(notifier.notify)
			}}
			h.Stream(writer, request)
			if strings.Contains(recorder.Body.String(), "revoked-entry") {
				t.Fatal("journal entry delivered after authority revocation")
			}
		})
	}
}
