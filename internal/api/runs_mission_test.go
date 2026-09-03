package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A run that worked on an issue names it: `mission_id` for filtering and
// `mission_identifier` (ENG-7) for a link a person can read. ?mission_id=
// narrows the list and accepts the identifier as well as the id, because
// the issue page links here with what it has on screen.

func (f *runsTestFixture) seedMission(t *testing.T, id, identifier string) {
	t.Helper()
	if _, err := f.h.db.ExecContext(context.Background(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status,
		    number, identifier, priority, sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, 'c-runs', ?, ?, 'Issue', 'IN_PROGRESS', 7, ?, 'medium', 0, 'issue',
		    datetime('now'), datetime('now'))`,
		id, f.wsID, f.agent, "trace-"+id, identifier); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
}

// emitMissionRunRow is emitRunRow with mission_id stamped on run.started.
func (f *runsTestFixture) emitMissionRunRow(t *testing.T, traceID, missionID string, when time.Time) {
	t.Helper()
	ts := when.UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.h.db.ExecContext(context.Background(), `
		INSERT INTO journal_entries
			(id, workspace_id, agent_id, mission_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
		VALUES (?, ?, ?, ?, ?, 'run.started', 'info', 'normal', 'orchestrator', ?, 'r', '{"trigger_type":"ASSIGNMENT"}', '{}', ?)`,
		traceID+"_s", f.wsID, f.agent, missionID, ts, f.agent, traceID); err != nil {
		t.Fatalf("insert run.started: %v", err)
	}
	if _, err := f.h.db.ExecContext(context.Background(), `
		INSERT INTO journal_entries
			(id, workspace_id, agent_id, mission_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
		VALUES (?, ?, ?, ?, ?, 'run.completed', 'info', 'normal', 'orchestrator', ?, 'r', '{"exit_code":0}', '{}', ?)`,
		traceID+"_t", f.wsID, f.agent, missionID, when.Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), f.agent, traceID); err != nil {
		t.Fatalf("insert run.completed: %v", err)
	}
}

func TestRunHandler_List_MissionIdentifierAndFilter(t *testing.T) {
	f := newRunsTestFixture(t)
	f.seedMission(t, "m_eng7", "ENG-7")
	f.seedMission(t, "m_eng8", "ENG-8")
	now := time.Now()
	f.emitMissionRunRow(t, "run_a", "m_eng7", now.Add(-3*time.Minute))
	f.emitMissionRunRow(t, "run_b", "m_eng8", now.Add(-2*time.Minute))
	f.emitRunRow(t, "run_chat", "COMPLETED", "USER", now.Add(-1*time.Minute))

	list := func(query string) []runResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs"+query, nil)
		req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
		rr := httptest.NewRecorder()
		f.h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", query, rr.Code, rr.Body.String())
		}
		var body runListResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: unmarshal: %v", query, err)
		}
		return body.Data
	}

	all := list("")
	if len(all) != 3 {
		t.Fatalf("all runs = %d, want 3", len(all))
	}
	for _, r := range all {
		switch r.ID {
		case "run_a":
			if r.MissionID == nil || *r.MissionID != "m_eng7" || r.MissionIdentifier == nil || *r.MissionIdentifier != "ENG-7" {
				t.Fatalf("run_a mission = %v / %v, want m_eng7 / ENG-7", r.MissionID, r.MissionIdentifier)
			}
		case "run_chat":
			if r.MissionID != nil || r.MissionIdentifier != nil {
				t.Fatalf("chat run must carry no mission: %v / %v", r.MissionID, r.MissionIdentifier)
			}
		}
	}

	for _, q := range []string{"?mission_id=m_eng8", "?mission_id=ENG-8"} {
		got := list(q)
		if len(got) != 1 || got[0].ID != "run_b" {
			t.Fatalf("%s: got %d rows (first %q), want only run_b", q, len(got), firstRunID(got))
		}
	}
	if got := list("?mission_id=ENG-999"); len(got) != 0 {
		t.Fatalf("unknown identifier must match nothing, got %d rows", len(got))
	}
}

func firstRunID(rs []runResponse) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[0].ID
}
