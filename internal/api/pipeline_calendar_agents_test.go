package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type calendarAgentsEvent struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Agents []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"agents"`
}

// The Agents lanes view and the agent filter of the Calendar need to know
// which agents a routine's events involve: the agent_run steps of its
// definition, resolved to agents of this workspace.
func TestRoutineCalendar_EventsCarryTheirAgents(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	now := time.Now().UTC()
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'a'), ('crew-b', ?, 'B', 'b')`, ws, ws)
	agent := func(id, crew, slug, name string, deleted bool) {
		execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status, cli_adapter, tool_profile, memory_enabled)
			VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1)`, id, crew, ws, name, slug)
		if deleted {
			execOrFatal(t, db, `UPDATE agents SET deleted_at = datetime('now') WHERE id = ?`, id)
		}
	}
	agent("ag-scout", "crew-b", "scout", "Scout", false)
	agent("ag-writer", "crew-a", "writer", "Writer", false)
	agent("ag-gone", "crew-a", "gone", "Gone", true)

	pipe := func(id, slug, crew, def string) {
		execOrFatal(t, db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, author_crew_id)
			VALUES (?, ?, ?, ?, ?, 'h', ?)`, id, ws, slug, slug, def, crew)
	}
	pipe("p-two", "two-agents", "crew-b", `{"name":"x","steps":[{"id":"a","type":"agent_run","agent_slug":"scout","prompt":"p"},{"id":"b","type":"agent_run","agent_slug":"writer","prompt":"p"},{"id":"c","type":"agent_run","agent_slug":"scout","prompt":"p"}]}`)
	pipe("p-none", "no-agents", "crew-a", `{"name":"x","steps":[]}`)
	pipe("p-gone", "gone-agent", "crew-a", `{"name":"x","steps":[{"id":"a","type":"agent_run","agent_slug":"gone","prompt":"p"},{"id":"b","type":"agent_run","agent_slug":"nobody","prompt":"p"}]}`)

	store := pipeline.NewPendingRunStore(db)
	for i, p := range []struct{ id, slug string }{{"p-two", "two-agents"}, {"p-none", "no-agents"}, {"p-gone", "gone-agent"}} {
		if _, _, err := store.Enqueue(context.Background(), pipeline.PendingRun{
			ID: "pend-" + p.id, WorkspaceID: ws, PipelineID: p.id, PipelineSlug: p.slug, FireAt: now.Add(time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	seedRunRow(t, db, ws, "p-two", "two-agents", "run-two", "completed")

	req := withWorkspaceUser(httptest.NewRequest("GET", "/calendar?from="+now.Add(-24*time.Hour).Format(time.RFC3339)+"&to="+now.Add(24*time.Hour).Format(time.RFC3339), nil), user, ws, "VIEWER")
	rr := httptest.NewRecorder()
	h.RoutineCalendar(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	var data struct {
		Events []calendarAgentsEvent `json:"events"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	byID := map[string]calendarAgentsEvent{}
	for _, e := range data.Events {
		byID[e.ID] = e
	}
	ids := func(e calendarAgentsEvent) []string {
		out := []string{}
		for _, a := range e.Agents {
			out = append(out, a.ID)
		}
		return out
	}
	tests := []struct {
		event string
		want  []string
	}{
		// each distinct agent once, in slug order, however many steps use it
		{"pend-p-two", []string{"ag-scout", "ag-writer"}},
		{"run-two", []string{"ag-scout", "ag-writer"}},
		{"pend-p-none", []string{}},
		// a deleted agent and an unknown slug are not offered as lanes
		{"pend-p-gone", []string{}},
	}
	for _, tc := range tests {
		e, ok := byID[tc.event]
		if !ok {
			t.Errorf("%s missing from %s", tc.event, rr.Body.String())
			continue
		}
		got := ids(e)
		if len(got) != len(tc.want) {
			t.Errorf("%s agents = %v, want %v", tc.event, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s agents = %v, want %v", tc.event, got, tc.want)
			}
		}
	}
}
