package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
)

type approvalDecisionRig struct {
	f  chatIssueFixture
	r  *Router
	a  chataudience.Receipt
	id string
}

func newApprovalDecisionRig(t *testing.T) approvalDecisionRig {
	t.Helper()
	f := newChatIssueFixture(t)
	a, err := chataudience.CaptureTrusted(t.Context(), f.h.db, f.chat, f.user, nil)
	if err != nil || a == nil {
		t.Fatalf("audience %v %v", a, err)
	}
	id, err := harbormaster.Enqueue(t.Context(), f.h.db, nil, harbormaster.Request{WorkspaceID: f.workspace, CrewID: f.crew, AgentID: f.agent, RequestedBy: f.agent, Kind: harbormaster.KindCustom, Reason: "Review this entire run", Payload: map[string]any{"tool": "agent_run", "args": map[string]any{}, "gate_mode": "sync", "correlation": map[string]any{"run_id": f.run, "chat_id": f.chat, "trigger_message_id": "trigger-" + f.run, "trigger_type": "human_chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(f.h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	return approvalDecisionRig{f, &Router{db: f.h.db, logger: newTestLogger(), journal: writer}, *a, id}
}
func (d approvalDecisionRig) read(t *testing.T) ChatCardProjection {
	t.Helper()
	p, err := d.r.runApprovalDecisionAdapter().Read(t.Context(), d.a, d.id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (d approvalDecisionRig) resolve(t *testing.T, version, key, action string) error {
	t.Helper()
	answer := chatentities.DecisionAnswer{SourceID: d.id, ExpectedVersion: version, Action: action, Values: map[string]json.RawMessage{}}
	raw, _ := json.Marshal(struct {
		User   string
		Answer chatentities.DecisionAnswer
	}{d.a.UserID, answer})
	sum := sha256.Sum256(raw)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", nil), d.a.UserID, d.a.WorkspaceID, "OWNER")
	return d.r.resolveRunApprovalDecision(req, &decisionOperation{Audience: d.a, Version: version, Key: key, Hash: fmt.Sprintf("%x", sum)}, answer)
}
func (d approvalDecisionRig) inbox(status string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", strings.NewReader(`{"status":"`+status+`"}`)), d.a.UserID, d.a.WorkspaceID, "OWNER")
	req.SetPathValue("id", d.id)
	rr := httptest.NewRecorder()
	NewApprovalsHandler(d.f.h.db, newTestLogger(), d.r.Journal()).Decide(rr, req)
	return rr
}
func (d approvalDecisionRig) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := d.f.h.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestChatDecisionApprovalSourceReplayAndIntent(t *testing.T) {
	d := newApprovalDecisionRig(t)
	p := d.read(t)
	if p.Ref.Decision.DecisionType != "whole_run_approval" || fmt.Sprint(p.Ref.Decision.AllowedActions) != "[view approve reject]" {
		t.Fatalf("projection %+v", p.Ref.Decision)
	}
	version := p.Ref.Decision.Source.Version
	if err := d.resolve(t, version, "same", "approve"); err != nil {
		t.Fatal(err)
	}
	if err := d.resolve(t, version, "same", "approve"); err != nil {
		t.Fatalf("lost reply replay %v", err)
	}
	if err := d.resolve(t, version, "same", "reject"); !errors.Is(err, errDecisionKeyCollision) {
		t.Fatalf("changed intent %v", err)
	}
	if err := d.resolve(t, version, "other", "reject"); !errors.Is(err, errDecisionVersion) {
		t.Fatalf("stale %v", err)
	}
	if got := d.read(t).Ref.Decision.Status; got != "approved" {
		t.Fatal(got)
	}
	if n := d.count(t, "gate_reward_history"); n != 1 {
		t.Fatalf("AfterDecide count=%d", n)
	}
	// A same-key replay is a current read, never renewed write authority.
	execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE id=?`, d.a.MemberID)
	live, _ := chataudience.CaptureTrusted(t.Context(), d.f.h.db, d.a.ChatID, d.a.UserID, nil)
	if live == nil {
		t.Fatal("viewer read")
	}
	d.a = *live
	if err := d.resolve(t, version, "same", "approve"); err != nil {
		t.Fatalf("viewer replay %v", err)
	}
	execOrFatal(t, d.f.h.db, `UPDATE users SET suspended_at='2026-10-16' WHERE id=?`, d.a.UserID)
	if err := d.resolve(t, version, "same", "approve"); !errors.Is(err, errDecisionDenied) {
		t.Fatalf("revoked replay %v", err)
	}
}
func TestChatDecisionApprovalInboxAndChatOneWinner(t *testing.T) {
	d := newApprovalDecisionRig(t)
	version := d.read(t).Ref.Decision.Source.Version
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	results := make(chan bool, 2)
	go func() { defer wg.Done(); <-start; results <- d.resolve(t, version, "chat", "approve") == nil }()
	go func() { defer wg.Done(); <-start; results <- d.inbox("denied").Code == 200 }()
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for won := range results {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
	if n := d.count(t, "gate_reward_history"); n != 1 {
		t.Fatalf("reward writes=%d", n)
	}
	if err := d.r.Journal().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var emissions int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type IN ('approval.granted','approval.denied')`).Scan(&emissions); err != nil || emissions != 1 {
		t.Fatalf("postcommit journal=%d %v", emissions, err)
	}
	if d.f.h.db.Stats().OpenConnections < 2 {
		t.Fatal("race must exercise separate connections")
	}
}
func TestChatDecisionApprovalNarrowSourceAndAnswer(t *testing.T) {
	for _, tc := range []struct{ name, path, value string }{{"async", "$.gate_mode", "async"}, {"per-tool", "$.tool", "shell.exec"}, {"foreign-chat", "$.correlation.chat_id", "other"}, {"legacy", "$.correlation.trigger_type", ""}, {"missing-run", "$.correlation.run_id", "missing"}} {
		t.Run(tc.name, func(t *testing.T) {
			d := newApprovalDecisionRig(t)
			execOrFatal(t, d.f.h.db, `UPDATE approvals_queue SET payload=json_set(payload,?,?) WHERE id=?`, tc.path, tc.value, d.id)
			if _, err := d.r.runApprovalDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("hidden source %v", err)
			}
		})
	}
	d := newApprovalDecisionRig(t)
	version := d.read(t).Ref.Decision.Source.Version
	for _, answer := range []chatentities.DecisionAnswer{{SourceID: d.id, ExpectedVersion: version, Action: "answer"}, {SourceID: d.id, ExpectedVersion: version, Action: "approve", ActionID: "release"}, {SourceID: d.id, ExpectedVersion: version, Action: "approve", Values: map[string]json.RawMessage{"secret": json.RawMessage(`"bad"`)}}} {
		req := httptest.NewRequest("POST", "/", nil)
		if err := d.r.resolveRunApprovalDecision(req, &decisionOperation{Audience: d.a, Version: version, Key: "bad", Hash: "bad"}, answer); !errors.Is(err, errDecisionInput) {
			t.Fatalf("invalid action %v", err)
		}
	}
	if n := d.count(t, "gate_reward_history"); n != 0 {
		t.Fatal(n)
	}
}
func TestChatDecisionApprovalExpiryAndLiveRoleSharedGuard(t *testing.T) {
	for _, change := range []string{"expired", "role", "requester", "terminal"} {
		for _, door := range []string{"chat", "inbox", "store"} {
			t.Run(change+"/"+door, func(t *testing.T) {
				d := newApprovalDecisionRig(t)
				version := d.read(t).Ref.Decision.Source.Version
				switch change {
				case "expired":
					execOrFatal(t, d.f.h.db, `UPDATE approvals_queue SET timeout_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), d.id)
				case "role":
					execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='MEMBER' WHERE id=?`, d.a.MemberID)
				case "requester":
					execOrFatal(t, d.f.h.db, `UPDATE chats SET authority_revision=authority_revision+1 WHERE id=?`, d.a.ChatID)
				case "terminal":
					execOrFatal(t, d.f.h.db, `INSERT INTO journal_entries(id,workspace_id,trace_id,agent_id,entry_type,severity,actor_type,actor_id,summary,payload,refs,ts) VALUES('ended',?,?,?,'run.completed','info','orchestrator','host','done','{}','{}',?)`, d.f.workspace, d.f.run, d.f.agent, time.Now().UTC().Format(time.RFC3339))
				}
				switch door {
				case "chat":
					if err := d.resolve(t, version, "no", "approve"); err == nil {
						t.Fatal("chat allowed")
					}
				case "inbox":
					if rr := d.inbox("approved"); rr.Code == 200 {
						t.Fatal("Inbox allowed")
					}
				case "store":
					if err := harbormaster.Decide(context.Background(), d.f.h.db, nil, d.f.workspace, d.id, harbormaster.StatusApproved, d.a.UserID, ""); err == nil {
						t.Fatal("store allowed")
					}
				}
				row, err := harbormaster.Get(t.Context(), d.f.h.db, d.f.workspace, d.id)
				if err != nil || row.Status != harbormaster.StatusPending {
					t.Fatalf("durable outcome %+v %v", row, err)
				}
				if n := d.count(t, "gate_reward_history"); n != 0 {
					t.Fatal(n)
				}
			})
		}
	}
}
func TestChatDecisionApprovalLegacyPerToolAsyncStillDecide(t *testing.T) {
	for _, tc := range []struct{ name, path, value string }{{"async", "$.gate_mode", "async"}, {"per-tool", "$.tool", "shell.exec"}, {"legacy", "$.correlation.trigger_type", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			d := newApprovalDecisionRig(t)
			execOrFatal(t, d.f.h.db, `UPDATE approvals_queue SET timeout_at=?,payload=json_set(payload,?,?) WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), tc.path, tc.value, d.id)
			if rr := d.inbox("approved"); rr.Code != 200 {
				t.Fatalf("legacy %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestChatDecisionApprovalCurrentRequesterSeparateFromDecider(t *testing.T) {
	d := newApprovalDecisionRig(t)
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES('approval-admin','approval-admin@example.test')`)
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('approval-admin-member',?,'approval-admin','ADMIN')`, d.f.workspace)
	admin, err := chataudience.CaptureTrusted(t.Context(), d.f.h.db, d.f.chat, "approval-admin", nil)
	if err != nil || admin == nil {
		t.Fatalf("admin audience %v %v", admin, err)
	}
	d.a = *admin
	version := d.read(t).Ref.Decision.Source.Version
	execOrFatal(t, d.f.h.db, `UPDATE users SET suspended_at='2026-10-16' WHERE id=?`, d.f.user)
	if _, err = d.r.runApprovalDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale requester card visible: %v", err)
	}
	if err = d.resolve(t, version, "withdrawn-requester", "approve"); !errors.Is(err, errDecisionDenied) {
		t.Fatalf("requester withdrawn: %v", err)
	}
	if rr := d.inbox("approved"); rr.Code != 403 {
		t.Fatalf("Inbox requester fence %d %s", rr.Code, rr.Body.String())
	}
	row, _ := harbormaster.Get(t.Context(), d.f.h.db, d.f.workspace, d.id)
	if row.Status != harbormaster.StatusPending {
		t.Fatal(row.Status)
	}
	if n := d.count(t, "gate_reward_history"); n != 0 {
		t.Fatal(n)
	}
}

func TestChatDecisionApprovalChangedPromptVersionCannotTransfer(t *testing.T) {
	d := newApprovalDecisionRig(t)
	version := d.read(t).Ref.Decision.Source.Version
	execOrFatal(t, d.f.h.db, `UPDATE approvals_queue SET payload=json_set(payload,'$.review.prompt','changed intent') WHERE id=?`, d.id)
	if err := d.resolve(t, version, "old-intent", "approve"); !errors.Is(err, errDecisionVersion) {
		t.Fatalf("old version %v", err)
	}
	row, _ := harbormaster.Get(t.Context(), d.f.h.db, d.f.workspace, d.id)
	if row.Status != harbormaster.StatusPending {
		t.Fatal(row.Status)
	}
	if err := d.resolve(t, d.read(t).Ref.Decision.Source.Version, "new-intent", "reject"); err != nil {
		t.Fatal(err)
	}
	if got := d.read(t).Ref.Decision.Status; got != "rejected" {
		t.Fatal(got)
	}
}

func TestChatDecisionApprovalRealGateBlocksOnlyActualSync(t *testing.T) {
	for _, tuned := range []bool{false, true} {
		t.Run(fmt.Sprint("auto-tuned=", tuned), func(t *testing.T) {
			d := newApprovalDecisionRig(t)
			execOrFatal(t, d.f.h.db, `DELETE FROM approvals_queue WHERE id=?`, d.id)
			if tuned {
				for i := 0; i < 10; i++ {
					if err := harbormaster.RecordOutcome(t.Context(), d.f.h.db, d.f.workspace, "agent_run", map[string]any{}, harbormaster.OutcomeApproved, d.f.user, fmt.Sprint("past-", i)); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				decision harbormaster.Decision
				err      error
			}
			done := make(chan result, 1)
			go func() {
				decision, err := harbormaster.Gate(ctx, d.f.h.db, d.r.Journal(), harbormaster.NewEvaluator(harbormaster.RuleMatcher{Name: "fixture-only whole run", ToolPattern: regexp.MustCompile(`^agent_run$`), MapsToKind: harbormaster.KindCustom}), harbormaster.GateInput{Mode: harbormaster.ModeSync, Tool: "agent_run", Args: map[string]any{}, WorkspaceID: d.f.workspace, CrewID: d.f.crew, AgentID: d.f.agent, RequestedBy: d.f.agent, PollInterval: time.Millisecond, TimeoutSecs: 10, Correlation: map[string]any{"run_id": d.f.run, "chat_id": d.f.chat, "trigger_message_id": "trigger-" + d.f.run, "trigger_type": "human_chat"}})
				done <- result{decision, err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				err := d.f.h.db.QueryRow(`SELECT id FROM approvals_queue WHERE workspace_id=? AND agent_id=?`, d.f.workspace, d.f.agent).Scan(&d.id)
				if err == nil {
					break
				}
				if !errors.Is(err, sql.ErrNoRows) || time.Now().After(deadline) {
					t.Fatalf("Gate row %v", err)
				}
				time.Sleep(time.Millisecond)
			}
			if tuned {
				select {
				case got := <-done:
					if got.err != nil || !got.decision.Pending || got.decision.EffectiveMode != harbormaster.ModeAsync {
						t.Fatalf("async %+v", got)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("actual async blocked")
				}
				if _, err := d.r.runApprovalDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("actual async became E2 blocker %v", err)
				}
				if rr := d.inbox("approved"); rr.Code != 200 {
					t.Fatalf("async Inbox %d", rr.Code)
				}
				return
			}
			select {
			case got := <-done:
				t.Fatalf("sync returned before decision %+v", got)
			default:
			}
			pending := d.read(t)
			if err := d.resolve(t, pending.Ref.Decision.Source.Version, "real-gate", "approve"); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if got.err != nil || !got.decision.Approved || got.decision.EffectiveMode != harbormaster.ModeSync {
					t.Fatalf("sync %+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("sync did not observe decision")
			}
		})
	}
}
