package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// --author-agent, and the run-time 400 it was supposed to prevent
// ---------------------------------------------------------------------------
//
// `crewship routine save --author-agent` was advertised in the help text and
// discarded in the client (`_ = authorAgent`), because userSaveRequest had no
// field for it. Every CLI-authored routine therefore had author_agent_id = "",
// and the `crewship` step's issue.comment verb — whose acting agent the
// dispatcher injects from exactly that column — failed at RUN time with
// `400: agent_id is required`. The most useful verb on the step kind was
// unreachable from any CLI-authored routine, and the flag that would have
// fixed it did nothing.
//
// These tests pin both halves: the field is honoured (and validated), and the
// unfixable case is refused at SAVE rather than at 03:00.

// crewshipCommentDef is a routine whose only step is the verb that requires an
// acting agent.
const crewshipCommentDef = `{"name":"comment","steps":[{"id":"report","type":"crewship",` +
	`"action":"issue.comment","args":{"identifier":"ENG-1","body":"the nightly check failed"}}]}`

func authorAgentSaveBody(t *testing.T, slug, crewID, agentID, definition string) string {
	t.Helper()
	body := map[string]any{
		"slug":           slug,
		"name":           slug + " name",
		"definition":     json.RawMessage(definition),
		"author_crew_id": crewID,
		"skip_test_gate": true,
	}
	if agentID != "" {
		body["author_agent_id"] = agentID
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal save body: %v", err)
	}
	return string(b)
}

// The flag's whole point: the id the user names lands on the row, so the
// dispatcher has an agent to inject and issue.comment can work at all.
func TestPipelineSave_HonoursAuthorAgentID(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)

	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(authorAgentSaveBody(t, "attributed", crewID, "covpc_agent", covPCDef("attributed"))))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var stored string
	if err := h.db.QueryRow(
		`SELECT COALESCE(author_agent_id, '') FROM pipelines WHERE workspace_id = ? AND slug = ?`,
		wsID, "attributed").Scan(&stored); err != nil {
		t.Fatalf("read author_agent_id: %v", err)
	}
	if stored != "covpc_agent" {
		t.Errorf("author_agent_id = %q, want covpc_agent — the flag is still a no-op", stored)
	}
}

// The saving user does not get to attribute a routine to an agent they cannot
// reach. Same message for "another tenant's agent" and "no such agent", so the
// route is not an existence oracle for ids in a workspace the caller cannot
// read.
func TestPipelineSave_RejectsAuthorAgentOutsideTheAuthorCrew(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	otherCrew := seedCrewRow(t, h.db, "covpc_crew_b", wsID, "Other Crew", "other-crew")
	seedAgentRow(t, h.db, "covpc_agent_b", wsID, otherCrew, "Other", "agent_other", "LEAD")

	cases := map[string]string{
		"sibling crew in the same workspace": "covpc_agent_b",
		"no such agent":                      "agt_does_not_exist",
	}
	for name, agentID := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x",
				strings.NewReader(authorAgentSaveBody(t, "foreign-"+agentID, crewID, agentID, covPCDef("foreign"))))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Save(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "author crew") {
				t.Errorf("body %q does not say the agent must be in the author crew", rr.Body.String())
			}
		})
	}
}

// The failure this whole change exists to move earlier: a routine that
// comments on an issue with nobody to comment AS must not save. Before the
// gate it saved 201 and then 400'd on every run.
func TestPipelineSave_RefusesIssueCommentWithNoActingAgent(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)

	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(authorAgentSaveBody(t, "nightly", crewID, "", crewshipCommentDef)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "--author-agent") {
		t.Errorf("refusal %q does not name the remedy", rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = ?`,
		wsID, "nightly").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rows = %d, want 0 — the refused routine landed anyway", n)
	}
}

// …and with an acting agent named, the same definition saves. Without this the
// test above would pass against a gate that refused issue.comment outright.
func TestPipelineSave_AcceptsIssueCommentWhenAnActingAgentIsNamed(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)

	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(authorAgentSaveBody(t, "nightly", crewID, "covpc_agent", crewshipCommentDef)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// The agent-authored door is held to the same two rules. A sidecar naming an
// agent outside its own author crew is refused, and the acting-agent gate
// applies there too — an agent that authors a commenting routine for a crew
// with no author agent has built the same 03:00 failure.
func TestInternalPipelineSave_ValidatesAuthorAgentAndGatesIssueComment(t *testing.T) {
	h, _, wsID, crewID := covPCHandler(t)
	otherCrew := seedCrewRow(t, h.db, "covpc_crew_c", wsID, "Other Crew", "other-crew-c")
	seedAgentRow(t, h.db, "covpc_agent_c", wsID, otherCrew, "Other", "agent_other_c", "LEAD")

	internalBody := func(slug, agentID, definition string) string {
		b, _ := json.Marshal(map[string]any{
			"workspace_id":    wsID,
			"slug":            slug,
			"name":            slug,
			"definition":      json.RawMessage(definition),
			"author_crew_id":  crewID,
			"author_agent_id": agentID,
		})
		return string(b)
	}

	t.Run("author agent outside the author crew", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x",
			strings.NewReader(internalBody("i-foreign", "covpc_agent_c", covPCDef("i-foreign"))))
		rr := httptest.NewRecorder()
		h.InternalSave(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("issue.comment with no author agent", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x",
			strings.NewReader(internalBody("i-nightly", "", crewshipCommentDef)))
		rr := httptest.NewRecorder()
		h.InternalSave(rr, req)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
		}
		// 422 is also what the test-run gate answers, so assert on the reason —
		// otherwise this passes against the bug.
		if !strings.Contains(rr.Body.String(), "acting agent") {
			t.Errorf("refusal %q is not the acting-agent gate", rr.Body.String())
		}
	})
}
