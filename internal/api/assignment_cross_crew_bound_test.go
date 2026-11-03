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
	// One-way link INTO eng: rel may dispatch to eng, eng may not dispatch to rel.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xc-rel', ?, 'Rel', 'rel')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('xc-riley', 'xc-rel', ?, 'riley', 'riley', 'IDLE')`, wsID)
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at)
		VALUES ('xc-oneway', ?, 'xc-rel', 'xc-eng', 'unidirectional', 'active', datetime('now'))`, wsID)
	// A workspace agent without a crew (agents_create.go allows it).
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('xc-loose', NULL, ?, 'loose', 'loose', 'IDLE')`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)
	t.Cleanup(h.WaitDispatches)
	const callerRefusal = `{"error":"assigning chat or agent is outside the crew bound to the internal token"}`

	cases := []struct {
		name      string
		chat      string
		target    string
		crew      string
		actor     string
		wantCode  int
		wantExact string
	}{
		// The sidecar always forwards actor_agent_id (internal/sidecar/assignment.go),
		// so the rows that model real traffic name one.
		{name: "linked crew is reachable", chat: "xc-chat-eng", target: "morgan", crew: "xc-ops", actor: "xc-alex", wantCode: http.StatusCreated},
		{name: "linked crew is reachable without an actor", chat: "xc-chat-eng", target: "morgan", crew: "xc-ops", wantCode: http.StatusCreated},
		{name: "own crew by id", chat: "xc-chat-eng", target: "sam", crew: "xc-eng", actor: "xc-alex", wantCode: http.StatusCreated},
		{name: "unlinked crew is refused by the link check", chat: "xc-chat-eng", target: "jordan", crew: "xc-qa", actor: "xc-alex",
			wantCode: http.StatusForbidden, wantExact: `{"error":"crews are not connected — create a crew connection first"}`},
		{name: "one-way link is refused against its direction", chat: "xc-chat-eng", target: "riley", crew: "xc-rel", actor: "xc-alex",
			wantCode: http.StatusForbidden, wantExact: `{"error":"crews are not connected — create a crew connection first"}`},
		{name: "crew of another workspace is refused", chat: "xc-chat-eng", target: "morgan", crew: "xc-far", actor: "xc-alex",
			wantCode: http.StatusForbidden, wantExact: `{"error":"crew does not belong to the workspace bound to the internal token"}`},
		{name: "unknown crew gets the identical refusal", chat: "xc-chat-eng", target: "morgan", crew: "xc-nope", actor: "xc-alex",
			wantCode: http.StatusForbidden, wantExact: `{"error":"crew does not belong to the workspace bound to the internal token"}`},
		{name: "chat of another crew without an actor", chat: "xc-chat-ops", target: "sam", crew: "xc-eng",
			wantCode: http.StatusForbidden, wantExact: callerRefusal},
		{name: "chat of another crew with an own-crew actor", chat: "xc-chat-ops", target: "morgan", crew: "xc-ops", actor: "xc-alex",
			wantCode: http.StatusForbidden, wantExact: callerRefusal},
		{name: "acting agent of another crew", chat: "xc-chat-eng", target: "sam", crew: "xc-eng", actor: "xc-morgan",
			wantCode: http.StatusForbidden, wantExact: callerRefusal},
		{name: "crewless acting agent", chat: "xc-chat-eng", target: "morgan", crew: "xc-ops", actor: "xc-loose",
			wantCode: http.StatusForbidden, wantExact: callerRefusal},
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
			if tc.wantExact != "" && strings.TrimSpace(w.Body.String()) != tc.wantExact {
				t.Errorf("refusal = %s, want exactly %s", strings.TrimSpace(w.Body.String()), tc.wantExact)
			}
			var rows int
			if err := db.QueryRowContext(context.Background(),
				`SELECT COUNT(*) FROM assignments WHERE chat_id = ? AND task = ? AND assigned_to_id IN (SELECT id FROM agents WHERE slug = ? AND crew_id = ?)`,
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

// TestAssignmentCreate_CrewBoundSubAgentKeepsLeadChat pins the one exception
// to the chat-crew rule (#4845 review B2). A sub-agent in a linked crew that is
// executing an assignment runs on its lead's chat, so that chat belongs to the
// other crew. When the server derived the chat from source_run_id
// (delegationExecutionChat proved the run), the sub-agent's own crew token may
// still delegate. The same body without source_run_id names a foreign chat and
// is refused.
func TestAssignmentCreate_CrewBoundSubAgentKeepsLeadChat(t *testing.T) {
	f := newChatIssueFixture(t)
	db := f.h.db
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('sub-crew', ?, 'Ops', 'sub-ops')`, f.workspace)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('sub-agent', 'sub-crew', ?, 'Sub', 'sub-agent', 'IDLE')`, f.workspace)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('sub-peer', 'sub-crew', ?, 'Peer', 'sub-peer', 'IDLE')`, f.workspace)
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at)
		VALUES ('sub-link', ?, ?, 'sub-crew', 'bidirectional', 'active', datetime('now'))`, f.workspace, f.crew)

	// The sub-agent's run executes on the lead's chat, as buildAssignmentRunRequest
	// does: a host-admitted ASSIGNMENT run whose run.started names that chat.
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, agent_id, entry_type, actor_type, summary, trace_id, payload)
		VALUES ('sub-run-start', ?, 'sub-agent', 'run.started', 'orchestrator', 'started', 'sub-run', ?)`,
		f.workspace, `{"trigger_type":"ASSIGNMENT","host_admitted":true,"chat_id":"`+f.chat+`"}`)

	h := NewAssignmentHandler(db, nil, nil, "host", newTestLogger())
	t.Cleanup(h.WaitDispatches)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", jsonBody(body))
		ctx := context.WithValue(req.Context(), ctxInternalTokenWS, f.workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "sub-crew")
		w := httptest.NewRecorder()
		h.Create(w, req.WithContext(ctx))
		h.WaitDispatches()
		return w
	}
	base := map[string]any{"workspace_id": f.workspace, "crew_id": "sub-crew", "actor_agent_id": "sub-agent",
		"target_slug": "sub-peer", "task": "nested", "chat_id": f.chat}

	if w := post(base); w.Code != http.StatusForbidden {
		t.Fatalf("lead's chat without source_run_id: status %d, want 403; %s", w.Code, w.Body.String())
	}
	withRun := map[string]any{"source_run_id": "sub-run"}
	for k, v := range base {
		withRun[k] = v
	}
	if w := post(withRun); w.Code != http.StatusCreated {
		t.Fatalf("server-derived lead chat: status %d, want 201; %s", w.Code, w.Body.String())
	}

	// The exemption cannot borrow another crew's run: naming the lead's live run
	// (f.run, an agent outside sub-crew) with an own-crew actor is refused.
	borrowed := map[string]any{}
	for k, v := range withRun {
		borrowed[k] = v
	}
	borrowed["source_run_id"] = f.run
	if w := post(borrowed); w.Code != http.StatusForbidden && w.Code != http.StatusConflict {
		t.Fatalf("borrowed foreign run: status %d, want a refusal; %s", w.Code, w.Body.String())
	}
	var rows int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM assignments WHERE task = 'nested'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("assignments for the nested task = %d, want only the one admitted request", rows)
	}
}
