package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestRecoveredStopAuditRejectsLateLifecycleWrites(t *testing.T) {
	for _, op := range []string{"create", "create-race", "complete", "running"} {
		t.Run(op, func(t *testing.T) {
			h, ws, agent := covIRunFixture(t)
			writer := wireTestJournalForHandler(t, h.db, h)
			const runID = "closed-by-recovery"
			// Complete/update probes include an explicitly late original start.
			if op != "create" && op != "create-race" {
				seedRunFixture(t, h.db, runID, agent, ws, "", "USER", "")
			}
			audit := func() {
				t.Helper()
				if _, err := writer.EmitSync(t.Context(), journal.Entry{ID: "recovered-stop:" + runID, WorkspaceID: ws, AgentID: agent, TraceID: runID, Type: "run.recovered_stop", ActorType: journal.ActorSystem, Summary: "recovered stop", Payload: map[string]any{"start_recorded": false}}); err != nil {
					t.Fatal(err)
				}
			}
			if op == "create-race" {
				// Recovery commits after CreateRun's precheck, immediately
				// before its source-of-truth start write.
				h.SetJournal(recoveryBeforeStart{Writer: writer, before: audit})
			} else if op == "create" {
				audit()
			} else {
				execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.recovered_stop','notice','system','recovered stop','{"start_recorded":false}','{}',?,'normal')`, "recovered-stop:"+runID, ws, agent, runID)
			}
			execOrFatal(t, h.db, `UPDATE agents SET status='STOPPED' WHERE id=?`, agent)
			rr := httptest.NewRecorder()
			if op == "create" || op == "create-race" {
				req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": runID, "agent_id": agent, "workspace_id": ws}))
				h.CreateRun(rr, req)
			} else {
				status := "COMPLETED"
				if op == "running" {
					status = "RUNNING"
				}
				req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/"+runID, jsonBody(map[string]any{"status": status}))
				req.SetPathValue("runId", runID)
				h.UpdateRun(rr, req)
			}
			if rr.Code != http.StatusConflict {
				t.Errorf("late lifecycle status=%d body=%s, want409", rr.Code, rr.Body.String())
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			var state string
			if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, agent).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "STOPPED" {
				t.Errorf("audit resurrected agent: %s", state)
			}
			var entries int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type<>'run.recovered_stop'`, runID).Scan(&entries); err != nil {
				t.Fatal(err)
			}
			want := 0
			if op != "create" && op != "create-race" {
				want = 1
			}
			if entries != want {
				t.Errorf("late lifecycle emitted entries=%d want=%d", entries, want)
			}
		})
	}
}

func TestRecoveredStopAuditDoesNotBlockOtherWorkspace(t *testing.T) {
	h, ws, agent := covIRunFixture(t)
	writer := wireTestJournalForHandler(t, h.db, h)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('foreign-ws','Foreign','foreign-ws')`)
	execOrFatal(t, h.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('foreign-agent','foreign-ws','Foreign','foreign-agent','STOPPED')`)
	const runID = "cross-workspace-collision"
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES('foreign-audit','foreign-ws','foreign-agent',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.recovered_stop','notice','system','foreign audit','{"start_recorded":false}','{}',?,'normal')`, runID)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": runID, "agent_id": agent, "workspace_id": ws}))
	h.CreateRun(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("foreign audit blocked own run: %d %s", rr.Code, rr.Body.String())
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM agents WHERE id='foreign-agent'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "STOPPED" {
		t.Errorf("foreign agent changed: %s", status)
	}
}

// The wrapper only controls ordering; both commits use the real Writer.
type recoveryBeforeStart struct {
	*journal.Writer
	before func()
}

func (w recoveryBeforeStart) EmitSync(ctx context.Context, e journal.Entry) (string, error) {
	w.before()
	return w.Writer.EmitSync(ctx, e)
}
