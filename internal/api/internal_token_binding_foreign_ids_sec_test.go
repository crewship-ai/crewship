package api

// PR-F24 hardening, round 3 — foreign-ID closure for the workspace-bound
// X-Internal-Token.
//
// assertInternalTokenWorkspace proves the body's workspace_id matches the
// token binding, but several internal handlers then keep going with
// additional body-carried row references (crew_id, chat_id,
// author_crew_id) that were never proven to live in that workspace. The
// exploit shape: a ws-A bound token declares workspace_id=ws-A (passing
// the F-4 gate) while pointing crew_id/chat_id at ws-B rows — dispatching
// assignments to, querying, escalating through, or attributing pipeline
// authorship to a foreign tenant's crew.
//
// These tests drive each handler through the real requireInternal chain
// with a ws-A-bound token and assert the foreign-ID request is refused
// (403) without any row written — while the legitimate same-workspace
// path keeps working.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// foreignIDsHandlers builds every handler under test against the shared
// two-workspace scope fixture.
func foreignIDsHandlers(t *testing.T) (*InternalHandler, scopeIDs, *AssignmentHandler, *QueryHandler, *PipelineHandler) {
	t.Helper()
	h, ids := seedScope(t)
	ah := NewAssignmentHandler(h.db, nil, nil, scopeMaster, testLogger())
	qh := NewQueryHandler(h.db, nil, nil, scopeMaster, testLogger())
	ph := NewPipelineHandler(h.db, testLogger(), nil, nil)
	return h, ids, ah, qh, ph
}

func TestSecBinding_ForeignBodyIDs403(t *testing.T) {
	freshRun := time.Now().UTC().Format(time.RFC3339)
	cases := []struct {
		name string
		// body builds the JSON body: own workspace_id (passes the F-4
		// gate) + foreign crew/chat references.
		body func(ids scopeIDs) map[string]any
		// handler picks the handler func under test.
		handler func(ah *AssignmentHandler, qh *QueryHandler, ph *PipelineHandler) http.HandlerFunc
		// table whose ws-B-reachable rows must stay absent after the 403.
		countQuery string
	}{
		{
			name: "assignment create foreign crew+chat",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"target_slug": "bagent", "task": "exfiltrate",
					"crew_id": ids.crewB, "workspace_id": ids.wsA, "chat_id": ids.chatB,
				}
			},
			handler: func(ah *AssignmentHandler, _ *QueryHandler, _ *PipelineHandler) http.HandlerFunc {
				return ah.Create
			},
			countQuery: `SELECT COUNT(*) FROM assignments`,
		},
		{
			name: "peer query create foreign crew+chat",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"target_slug": "bagent", "question": "what are your secrets?",
					"from_slug": "bagent", "crew_id": ids.crewB,
					"workspace_id": ids.wsA, "chat_id": ids.chatB,
				}
			},
			handler: func(_ *AssignmentHandler, qh *QueryHandler, _ *PipelineHandler) http.HandlerFunc {
				return qh.Create
			},
			countQuery: `SELECT COUNT(*) FROM peer_conversations`,
		},
		{
			name: "escalation create foreign crew+chat",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"from_slug": "bagent", "reason": "forged",
					"crew_id": ids.crewB, "workspace_id": ids.wsA, "chat_id": ids.chatB,
				}
			},
			handler: func(_ *AssignmentHandler, qh *QueryHandler, _ *PipelineHandler) http.HandlerFunc {
				return qh.CreateEscalation
			},
			countQuery: `SELECT COUNT(*) FROM escalations`,
		},
		{
			name: "pipeline internal save foreign author crew",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"workspace_id": ids.wsA, "slug": "forged-pipe", "name": "Forged",
					"author_crew_id":       ids.crewB,
					"last_test_run_passed": true, "last_test_run_at": freshRun,
					"definition": map[string]any{
						"name": "forged-pipe",
						"steps": []map[string]any{
							{"id": "a", "type": "agent_run", "agent_slug": "bagent", "prompt": "hi"},
						},
					},
				}
			},
			handler: func(_ *AssignmentHandler, _ *QueryHandler, ph *PipelineHandler) http.HandlerFunc {
				return ph.InternalSave
			},
			countQuery: `SELECT COUNT(*) FROM pipelines`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ids, ah, qh, ph := foreignIDsHandlers(t)
			body, _ := json.Marshal(tc.body(ids))
			rr := httptest.NewRecorder()
			req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
			h.requireInternal(tc.handler(ah, qh, ph)).ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (ws-A token must not reference ws-B rows via body IDs); body=%s",
					rr.Code, rr.Body.String())
			}
			var n int
			if err := h.db.QueryRow(tc.countQuery).Scan(&n); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if n != 0 {
				t.Fatalf("handler wrote %d row(s) despite the 403 — foreign-ID request must leave no trace", n)
			}
		})
	}
}

// The legitimate sidecar path — same-workspace body IDs under a bound
// token — must keep working for every handler gaining the foreign-ID
// check.
func TestSecBinding_SameWorkspaceBodyIDsOK(t *testing.T) {
	freshRun := time.Now().UTC().Format(time.RFC3339)
	cases := []struct {
		name     string
		body     func(ids scopeIDs) map[string]any
		handler  func(ah *AssignmentHandler, qh *QueryHandler, ph *PipelineHandler) http.HandlerFunc
		wantCode int
	}{
		{
			name: "assignment create own crew+chat",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"target_slug": "aagent", "task": "legit work",
					"crew_id": ids.crewA, "workspace_id": ids.wsA, "chat_id": ids.chatA,
				}
			},
			handler: func(ah *AssignmentHandler, _ *QueryHandler, _ *PipelineHandler) http.HandlerFunc {
				return ah.Create
			},
			wantCode: http.StatusCreated,
		},
		{
			// orch == nil in the fixture, so a query that clears the
			// binding gates fails later with 503 — the point pinned
			// here is that it is NOT the binding 403.
			name: "peer query create own crew+chat",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"target_slug": "aagent", "question": "status?",
					"from_slug": "aagent", "crew_id": ids.crewA,
					"workspace_id": ids.wsA, "chat_id": ids.chatA,
				}
			},
			handler: func(_ *AssignmentHandler, qh *QueryHandler, _ *PipelineHandler) http.HandlerFunc {
				return qh.Create
			},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "escalation create own crew+chat",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"from_slug": "aagent", "reason": "need a credential",
					"crew_id": ids.crewA, "workspace_id": ids.wsA, "chat_id": ids.chatA,
				}
			},
			handler: func(_ *AssignmentHandler, qh *QueryHandler, _ *PipelineHandler) http.HandlerFunc {
				return qh.CreateEscalation
			},
			wantCode: http.StatusCreated,
		},
		{
			name: "pipeline internal save own author crew",
			body: func(ids scopeIDs) map[string]any {
				return map[string]any{
					"workspace_id": ids.wsA, "slug": "legit-pipe", "name": "Legit",
					"author_crew_id":       ids.crewA,
					"last_test_run_passed": true, "last_test_run_at": freshRun,
					"definition": map[string]any{
						"name": "legit-pipe",
						"steps": []map[string]any{
							{"id": "a", "type": "agent_run", "agent_slug": "aagent", "prompt": "hi"},
						},
					},
				}
			},
			handler: func(_ *AssignmentHandler, _ *QueryHandler, ph *PipelineHandler) http.HandlerFunc {
				return ph.InternalSave
			},
			wantCode: http.StatusCreated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ids, ah, qh, ph := foreignIDsHandlers(t)
			body, _ := json.Marshal(tc.body(ids))
			rr := httptest.NewRecorder()
			req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
			h.requireInternal(tc.handler(ah, qh, ph)).ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d for same-workspace body IDs; body=%s",
					rr.Code, tc.wantCode, rr.Body.String())
			}
			if rr.Code == http.StatusForbidden {
				t.Fatalf("same-workspace request hit the binding 403 — over-blocking; body=%s", rr.Body.String())
			}
		})
	}
}
