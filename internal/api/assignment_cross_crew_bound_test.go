package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestAssignmentCreate_CrewBoundTokenAcrossLink pins #4845.
//
// A crew sidecar authenticates with a crew-bound token (crwv1) and forwards a
// lead's `/assign {"crew":"ops"}` with crew_id = the TARGET crew. The crew
// binding is about who is calling, so it must constrain the caller (the chat's
// agent and the acting agent). The target is governed by the crew link. Before
// the fix, every crew_id other than the token's own crew was refused ahead of
// the link check, so delegation across a live link could never succeed from a
// sidecar.
//
// The negative rows keep what the binding was introduced for (#1222, PR-F24).
// An eng-bound token cannot act through another crew's chat or agent. It cannot
// name a crew in another workspace, and that 403 is the same non-oracle refusal.
// It cannot reach an unlinked crew.
func TestAssignmentCreate_CrewBoundTokenAcrossLink(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	otherWS := "xc-ws-far"
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Far', 'xc-ws-far')`, otherWS)

	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xc-eng', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xc-ops', ?, 'Ops', 'ops')`, wsID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xc-qa', ?, 'QA', 'qa')`, wsID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xc-far', ?, 'Far', 'far')`, otherWS)
	for _, a := range [][3]string{
		{"xc-alex", "xc-eng", "alex"}, {"xc-sam", "xc-eng", "sam"},
		{"xc-morgan", "xc-ops", "morgan"}, {"xc-jordan", "xc-qa", "jordan"},
	} {
		execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES (?, ?, ?, ?, ?, 'IDLE')`,
			a[0], a[1], wsID, a[2], a[2])
	}
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('xc-far1', 'xc-far', ?, 'morgan', 'morgan', 'IDLE')`, otherWS)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('xc-chat-eng', 'xc-alex', ?, 'CHAT', 'ACTIVE')`, wsID)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('xc-chat-ops', 'xc-morgan', ?, 'CHAT', 'ACTIVE')`, wsID)
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at)
		VALUES ('xc-link', ?, 'xc-eng', 'xc-ops', 'bidirectional', 'active', datetime('now'))`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)
	t.Cleanup(h.WaitDispatches)

	cases := []struct {
		name      string
		chat      string
		target    string
		crew      string
		actor     string
		wantCode  int
		wantInErr string
	}{
		{name: "linked crew is reachable", chat: "xc-chat-eng", target: "morgan", crew: "xc-ops", wantCode: http.StatusCreated},
		{name: "own crew by id", chat: "xc-chat-eng", target: "sam", crew: "xc-eng", wantCode: http.StatusCreated},
		{name: "unlinked crew is refused by the link check", chat: "xc-chat-eng", target: "jordan", crew: "xc-qa",
			wantCode: http.StatusForbidden, wantInErr: "not connected"},
		{name: "crew of another workspace is refused", chat: "xc-chat-eng", target: "morgan", crew: "xc-far",
			wantCode: http.StatusForbidden, wantInErr: "workspace"},
		{name: "chat of another crew cannot be used by this token", chat: "xc-chat-ops", target: "sam", crew: "xc-eng",
			wantCode: http.StatusForbidden, wantInErr: "crew bound to the internal token"},
		{name: "acting agent of another crew cannot be named by this token", chat: "xc-chat-eng", target: "sam", crew: "xc-eng",
			actor: "xc-morgan", wantCode: http.StatusForbidden, wantInErr: "crew bound to the internal token"},
	}
	for i, tc := range cases {
		task := "probe-" + string(rune('a'+i))
		t.Run(tc.name, func(t *testing.T) {
			body := `{"target_slug":"` + tc.target + `","task":"` + task + `","workspace_id":"` + wsID + `","chat_id":"` + tc.chat + `"`
			if tc.crew != "" {
				body += `,"crew_id":"` + tc.crew + `"`
			}
			if tc.actor != "" {
				body += `,"actor_agent_id":"` + tc.actor + `"`
			}
			body += `}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(req.Context(), ctxInternalTokenWS, wsID)
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, "xc-eng")
			w := httptest.NewRecorder()
			h.Create(w, req.WithContext(ctx))
			h.WaitDispatches()

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantInErr != "" && !strings.Contains(w.Body.String(), tc.wantInErr) {
				t.Errorf("refusal %q does not contain %q", w.Body.String(), tc.wantInErr)
			}
			var rows int
			if err := db.QueryRowContext(context.Background(),
				`SELECT COUNT(*) FROM assignments WHERE chat_id = ? AND task = ? AND assigned_to_id IN (SELECT id FROM agents WHERE slug = ? AND crew_id = COALESCE(NULLIF(?, ''), 'xc-eng'))`,
				tc.chat, task, tc.target, tc.crew).Scan(&rows); err != nil {
				t.Fatalf("count assignments: %v", err)
			}
			if tc.wantCode == http.StatusCreated && rows == 0 {
				t.Errorf("201 without an assignment row for %s in %s", tc.target, tc.crew)
			}
			if tc.wantCode != http.StatusCreated && rows != 0 {
				t.Errorf("refused request still wrote %d assignment row(s)", rows)
			}
		})
	}
}
