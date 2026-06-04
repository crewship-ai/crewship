package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests cover the defense-in-depth fix for the cross-crew override
// vulnerability: the internal mission/issue Create handlers must verify the
// lead/author agent actually belongs to the supplied crew+workspace before
// inserting a row. A compromised agent in crew_a must not be able to create a
// mission/issue in crew_b with itself as lead/author.

func TestSecMissInt_CrossCrewLeadRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	mh := NewInternalMissionHandler(h.db, nil, nil, testLogger())

	// Second crew with its own lead in the same workspace.
	crewB := "crew-b-sec"
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Bravo', 'bravo', 'BRV')`,
		crewB, wsID); err != nil {
		t.Fatalf("insert crew B: %v", err)
	}
	leadB := "agent-lead-b"
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'LeadB', 'leadb', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		leadB, wsID, crewB); err != nil {
		t.Fatalf("insert lead B: %v", err)
	}

	// Attempt: create a mission in crewA but with leadB (who belongs to crewB).
	body := bytes.NewBufferString(`{"title":"X","lead_agent_id":"` + leadB + `","crew_id":"` + crewA + `","workspace_id":"` + wsID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	mh.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-crew lead, got %d body=%s", rr.Code, rr.Body.String())
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE crew_id = ? AND lead_agent_id = ?`, crewA, leadB).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no mission row inserted, got %d", count)
	}
}

func TestSecMissInt_SameCrewLeadSucceeds(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	mh := NewInternalMissionHandler(h.db, nil, nil, testLogger())

	body := bytes.NewBufferString(`{"title":"Legit","lead_agent_id":"` + leadA + `","crew_id":"` + crewA + `","workspace_id":"` + wsID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	mh.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for in-crew lead, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["id"] == nil || resp["id"] == "" {
		t.Errorf("expected mission id in response, got %v", resp)
	}
}

func TestSecIssueInt_CrossCrewAuthorRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)

	// Second crew with its own agent in the same workspace.
	crewB := "crew-b-sec-iss"
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Bravo', 'bravoi', 'BRI')`,
		crewB, wsID); err != nil {
		t.Fatalf("insert crew B: %v", err)
	}
	// crewB needs a LEAD so the legacy lead-lookup path is not the thing failing.
	leadB := "agent-lead-b-iss"
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'LeadB', 'leadbi', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		leadB, wsID, crewB); err != nil {
		t.Fatalf("insert lead B: %v", err)
	}

	// Attempt: agent leadB (crewB) creates an issue in crewA as author.
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"` + crewA + `","title":"Sneaky","author_agent_id":"` + leadB + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-crew author, got %d body=%s", rr.Code, rr.Body.String())
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE crew_id = ? AND mission_type = 'issue' AND title = 'Sneaky'`, crewA).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no issue row inserted, got %d", count)
	}
}

func TestSecIssueInt_SameCrewAuthorSucceeds(t *testing.T) {
	// newInternalIssueHandler's 5th return is userID, not an agent; use the
	// seeded worker agent (agent-worker) which belongs to crewA.
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	authorA := "agent-worker"

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"` + crewA + `","title":"Legit issue","author_agent_id":"` + authorA + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for in-crew author, got %d body=%s", rr.Code, rr.Body.String())
	}
}
