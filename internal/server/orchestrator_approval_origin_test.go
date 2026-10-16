package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type approvalOriginEmitter struct{ decide func() }

func (e approvalOriginEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	if entry.Type == journal.EntryApprovalRequest {
		e.decide()
	}
	return "test-event", nil
}

func (e approvalOriginEmitter) Flush(context.Context) error { return nil }

type approvalOriginHooks struct{ reached bool }

func (h *approvalOriginHooks) Dispatch(_ context.Context, event string, _ orchestrator.HookEventContext) error {
	if event == "pre_agent_start" {
		h.reached = true
		return errors.New("test stop before runtime")
	}
	return nil
}

type approvalOriginContainer struct {
	mockContainer
	execs int
}

func (c *approvalOriginContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.execs++
	return c.mockContainer.Exec(ctx, cfg)
}

func TestWholeRunApprovalOriginActualOrchestratorAdapter(t *testing.T) {
	for _, tc := range []struct {
		name, trigger                        string
		missing, revoke, stale, revokeBefore bool
		wantChat, wantBoundary               bool
	}{
		{name: "durable human", trigger: "USER", wantChat: true, wantBoundary: true},
		{name: "revoked while approving", trigger: "USER", revoke: true, wantChat: true},
		{name: "revoked while gate waits then admin refuses", trigger: "USER", revokeBefore: true, wantChat: true},
		{name: "stale before gate", trigger: "USER", stale: true},
		{name: "same agent delegated context", trigger: "PEER_QUERY", wantBoundary: true},
		{name: "legacy missing start", missing: true, wantBoundary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('approval-human','approval-human@test.local')`,
				`INSERT INTO users(id,email) VALUES('approval-operator','approval-operator@test.local')`,
				`INSERT INTO workspaces(id,name,slug) VALUES('approval-ws','Approval','approval-ws')`,
				`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('approval-member','approval-ws','approval-human','OWNER')`,
				`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('approval-operator-member','approval-ws','approval-operator','ADMIN')`,
				`INSERT INTO crews(id,workspace_id,name,slug) VALUES('approval-crew','approval-ws','Approval','approval-crew')`,
				`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('approval-agent','approval-ws','approval-crew','Approval','approval-agent')`,
				`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('approval-chat','approval-ws','approval-agent','approval-human','private')`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := chataudience.CaptureTrusted(t.Context(), db, "approval-chat", "approval-human", nil)
			if err != nil || receipt == nil {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			if !tc.missing {
				writer := journal.NewWriter(db, quietLogger(), journal.WriterOptions{})
				defer writer.Close()
				handler := goapi.NewInternalHandler(db, "synthetic-host-token", quietLogger())
				handler.SetJournal(writer)
				body, _ := json.Marshal(map[string]any{"id": "approval-run", "agent_id": "approval-agent", "workspace_id": "approval-ws", "chat_id": "approval-chat", "trigger_type": tc.trigger, "human_authority": receipt, "trigger_message_id": "approval-message"})
				req := httptest.NewRequest("POST", "/api/v1/internal/runs", strings.NewReader(string(body)))
				rec := httptest.NewRecorder()
				handler.CreateRun(rec, req)
				if rec.Code != 201 {
					t.Fatalf("real synchronous CreateRun: %d %s", rec.Code, rec.Body.String())
				}
			}
			if tc.stale {
				if _, err := db.Exec(`UPDATE users SET suspended_at=? WHERE id='approval-human'`, tsformat.Format(time.Now())); err != nil {
					t.Fatal(err)
				}
			}
			emitter := approvalOriginEmitter{decide: func() {
				var id string
				if err := db.QueryRow(`SELECT id FROM approvals_queue WHERE status='pending'`).Scan(&id); err != nil {
					t.Error(err)
					return
				}
				if tc.revokeBefore {
					if _, err := db.Exec(`UPDATE users SET suspended_at=? WHERE id='approval-human'`, tsformat.Format(time.Now())); err != nil {
						t.Error(err)
						return
					}
					if err := harbormaster.Decide(t.Context(), db, nil, "approval-ws", id, harbormaster.StatusApproved, "approval-operator", ""); !errors.Is(err, harbormaster.ErrDecisionDenied) {
						t.Errorf("stale requester approved: %v", err)
						return
					}
					var status string
					if err := db.QueryRow(`SELECT status FROM approvals_queue WHERE id=?`, id).Scan(&status); err != nil || status != "pending" {
						t.Errorf("refused approval durable state %q %v", status, err)
						return
					}
					if err := harbormaster.Decide(t.Context(), db, nil, "approval-ws", id, harbormaster.StatusDenied, "approval-operator", ""); err != nil {
						t.Errorf("ADMIN refusal must settle gate: %v", err)
					}
					return
				}
				if err := harbormaster.Decide(t.Context(), db, nil, "approval-ws", id, harbormaster.StatusApproved, "approval-operator", ""); err != nil {
					t.Error(err)
					return
				}
				// The canonical writer now rejects stale requesters itself. Revoke only
				// after its committed winner to exercise the separate pre-runtime fence.
				if tc.revoke {
					if _, err := db.Exec(`UPDATE users SET suspended_at=? WHERE id='approval-human'`, tsformat.Format(time.Now())); err != nil {
						t.Error(err)
					}
				}
			}}
			adapter := newApprovalGateAdapter(db, emitter)
			// Exercise the same explicit crew opt-in as a live operator, rather
			// than replacing the production evaluator with a test-only rule.
			if _, err := db.Exec(`UPDATE crews SET whole_run_approval=1 WHERE id='approval-crew'`); err != nil {
				t.Fatal(err)
			}
			container := &approvalOriginContainer{}
			hooks := &approvalOriginHooks{}
			orch := orchestrator.New(container, newMockState(), logging.New("error", "json", nil))
			orch.SetApprovalGate(adapter)
			orch.SetHooksDispatcher(hooks)
			bounded, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx := runauthority.WithTriggerMessage(chataudience.WithReceipt(bounded, receipt), "approval-message")
			err = orch.RunAgent(ctx, orchestrator.AgentRunRequest{RunID: "approval-run", AgentID: "approval-agent", AgentSlug: "approval-agent", AgentRole: "AGENT", WorkspaceID: "approval-ws", CrewID: "approval-crew", ChatID: "approval-chat", ApprovalMode: "sync", UserMessage: "review this exact attempt"}, nil)
			if err == nil {
				t.Fatal("expected test boundary or stale-requester stop")
			}
			if hooks.reached != tc.wantBoundary || container.execs != 0 {
				t.Fatalf("boundary=%v execs=%d err=%v", hooks.reached, container.execs, err)
			}
			if !tc.wantBoundary && !tc.revokeBefore && !strings.Contains(err.Error(), "requester authority") {
				t.Fatalf("wrong refusal: %v", err)
			}
			if tc.stale {
				var count int
				db.QueryRow(`SELECT COUNT(*) FROM approvals_queue`).Scan(&count)
				if count != 0 {
					t.Fatal("stale human enqueued approval")
				}
				return
			}
			if tc.revokeBefore {
				var status string
				var outcomes int
				if err := db.QueryRow(`SELECT status FROM approvals_queue`).Scan(&status); err != nil || status != "denied" {
					t.Fatalf("gate not refused %q %v", status, err)
				}
				if err := db.QueryRow(`SELECT COUNT(*) FROM gate_reward_history`).Scan(&outcomes); err != nil || outcomes != 1 {
					t.Fatalf("refusal outcome=%d %v", outcomes, err)
				}
			}
			var raw string
			if err := db.QueryRow(`SELECT payload FROM approvals_queue`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			correlation := payload["correlation"].(map[string]any)
			if _, ok := correlation["chat_id"]; ok != tc.wantChat {
				t.Fatalf("guessed/inherited chat correlation: %+v", correlation)
			}
			if correlation["run_id"] != "approval-run" || payload["tool"] != "agent_run" || payload["gate_mode"] != "sync" {
				t.Fatal(payload)
			}
		})
	}
}

func TestApprovalOriginLeavesPerToolGateAlone(t *testing.T) {
	input := orchestrator.ApprovalCheckInput{Tool: "shell_exec", Correlation: map[string]any{"run_id": "run", "chat_id": "chat", "trigger_message_id": "trigger"}}
	adapter := newApprovalGateAdapter(nil, nil)
	correlation, origin, err := adapter.approvalOrigin(t.Context(), input)
	if err != nil || origin != nil || correlation["chat_id"] != "chat" {
		t.Fatalf("per-tool behavior changed: %+v %+v %v", correlation, origin, err)
	}
}
