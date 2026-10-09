package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #2862: refusals a client branches on carry a machine code and the field
// they concern next to the unchanged sentence, so the web app can put a slug
// conflict under the Slug field instead of toasting raw JSON.
func decodeConflict(t *testing.T, rr *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	return body
}

func TestAgentConflictsCarryCodeAndField(t *testing.T) {
	tests := []struct {
		name      string
		do        func(t *testing.T, h *AgentHandler, userID, wsID string) *httptest.ResponseRecorder
		wantCode  string
		wantField string
		wantText  string
	}{
		{
			name: "create with a taken slug",
			do: func(t *testing.T, h *AgentHandler, userID, wsID string) *httptest.ResponseRecorder {
				seedAgentRow(t, h.db, "ag-1", wsID, "crew-a", "One", "taken", "AGENT")
				return createAgent(t, h, userID, wsID, "crew-b", "taken")
			},
			wantCode: errCodeAgentSlugTaken, wantField: "slug", wantText: "Agent slug already taken in this workspace",
		},
		{
			name: "create with a reserved slug",
			do: func(t *testing.T, h *AgentHandler, userID, wsID string) *httptest.ResponseRecorder {
				seedAgentRow(t, h.db, "ag-old", wsID, "crew-a", "Old", "kept", "AGENT")
				deleteAgent(t, h, userID, wsID, "ag-old")
				return createAgent(t, h, userID, wsID, "crew-a", "kept")
			},
			wantCode: errCodeAgentSlugReserved, wantField: "slug", wantText: "was used by another agent",
		},
		{
			name: "create a second lead",
			do: func(t *testing.T, h *AgentHandler, userID, wsID string) *httptest.ResponseRecorder {
				seedAgentRow(t, h.db, "ag-lead", wsID, "crew-a", "Lead", "lead", "LEAD")
				rr := httptest.NewRecorder()
				h.Create(rr, slugReq(t, "POST", "/api/v1/agents", userID, wsID, map[string]any{
					"name": "Lead2", "slug": "lead2", "crew_id": "crew-a", "cli_adapter": "CLAUDE_CODE", "agent_role": "LEAD",
				}))
				return rr
			},
			wantCode: errCodeCrewLeadExists, wantField: "agent_role", wantText: "Crew already has a lead agent",
		},
		{
			// Used to reach the UPDATE and fail its UNIQUE constraint as a 500.
			name: "rename onto a taken slug",
			do: func(t *testing.T, h *AgentHandler, userID, wsID string) *httptest.ResponseRecorder {
				seedAgentRow(t, h.db, "ag-x", wsID, "crew-a", "X", "x-slug", "AGENT")
				seedAgentRow(t, h.db, "ag-y", wsID, "crew-b", "Y", "y-slug", "AGENT")
				return patchAgent(t, h, userID, wsID, "ag-y", map[string]any{"slug": "x-slug"})
			},
			wantCode: errCodeAgentSlugTaken, wantField: "slug", wantText: "Agent slug already taken in this workspace",
		},
		{
			name: "rename onto a reserved slug",
			do: func(t *testing.T, h *AgentHandler, userID, wsID string) *httptest.ResponseRecorder {
				seedAgentRow(t, h.db, "ag-r", wsID, "crew-a", "R", "rena", "AGENT")
				seedAgentRow(t, h.db, "ag-s", wsID, "crew-a", "S", "sena", "AGENT")
				if rr := patchAgent(t, h, userID, wsID, "ag-r", map[string]any{"slug": "renb"}); rr.Code != http.StatusOK {
					t.Fatalf("rename: %d %s", rr.Code, rr.Body.String())
				}
				return patchAgent(t, h, userID, wsID, "ag-s", map[string]any{"slug": "rena"})
			},
			wantCode: errCodeAgentSlugReserved, wantField: "slug", wantText: "was used by another agent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := slugFixture(t)
			body := decodeConflict(t, tc.do(t, h, userID, wsID))
			if body["code"] != tc.wantCode || body["field"] != tc.wantField {
				t.Errorf("code/field = %q/%q, want %q/%q (%v)", body["code"], body["field"], tc.wantCode, tc.wantField, body)
			}
			// The sentence is the CLI's contract and stays as it was.
			if !strings.Contains(body["error"], tc.wantText) {
				t.Errorf("error = %q, want it to contain %q", body["error"], tc.wantText)
			}
		})
	}
}

// A rename onto the slug of a soft-deleted agent in ANOTHER crew is free
// (the create path already frees it); it must not hit the UNIQUE constraint.
func TestAgentRenameOntoDeletedAgentsSlugInAnotherCrew(t *testing.T) {
	h, userID, wsID := slugFixture(t)
	seedAgentRow(t, h.db, "ag-gone", wsID, "crew-a", "Gone", "shared", "AGENT")
	deleteAgent(t, h, userID, wsID, "ag-gone")
	seedAgentRow(t, h.db, "ag-b", wsID, "crew-b", "B", "bee", "AGENT")
	if rr := patchAgent(t, h, userID, wsID, "ag-b", map[string]any{"slug": "shared"}); rr.Code != http.StatusOK {
		t.Fatalf("rename = %d %s, want 200", rr.Code, rr.Body.String())
	}
}

// The UNIQUE races behind the check-then-act SELECTs answer with the same
// sentence and code as the SELECT path. SQLite names the violated columns,
// not the index, so the lead race used to read as a taken slug.
func TestReplyAgentUniqueConflictClassifiesSQLiteErrors(t *testing.T) {
	h, _, wsID := slugFixture(t)
	seedAgentRow(t, h.db, "ag-l1", wsID, "crew-a", "L1", "l1", "LEAD")
	seedAgentRow(t, h.db, "ag-s1", wsID, "crew-a", "S1", "s1", "AGENT")

	for _, tc := range []struct {
		name, stmt, wantCode, wantField string
	}{
		{"second lead", `UPDATE agents SET agent_role='LEAD' WHERE id='ag-s1'`, errCodeCrewLeadExists, "agent_role"},
		{"duplicate slug", `UPDATE agents SET slug='l1' WHERE id='ag-s1'`, errCodeAgentSlugTaken, "slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.db.Exec(tc.stmt)
			if !isUniqueConstraintErr(err) {
				t.Fatalf("expected a UNIQUE violation, got %v", err)
			}
			rr := httptest.NewRecorder()
			replyAgentUniqueConflict(rr, err)
			body := decodeConflict(t, rr)
			if body["code"] != tc.wantCode || body["field"] != tc.wantField {
				t.Errorf("%v → code/field %q/%q, want %q/%q", err, body["code"], body["field"], tc.wantCode, tc.wantField)
			}
		})
	}
}

func TestReplyErrorCodeOmitsEmptyCodeAndField(t *testing.T) {
	rr := httptest.NewRecorder()
	replyErrorCode(rr, http.StatusConflict, "plain", "", "")
	if strings.TrimSpace(rr.Body.String()) != `{"error":"plain"}` {
		t.Fatalf("body = %s", rr.Body.String())
	}
	rr = httptest.NewRecorder()
	writeProblemCode(rr, httptest.NewRequest("POST", "/x", nil), http.StatusConflict, "taken", errCodeAgentSlugTaken, "slug")
	var p map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &p)
	if p["detail"] != "taken" || p["code"] != errCodeAgentSlugTaken || p["field"] != "slug" || p["status"] != float64(409) {
		t.Fatalf("problem = %v", p)
	}
}
