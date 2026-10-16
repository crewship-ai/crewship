package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type waitpointCardRig struct {
	f     chatIssueFixture
	r     *Router
	token string
	id    string
	a     chataudience.Receipt
}

func waitpointCardForm() *pipeline.DecisionForm {
	return &pipeline.DecisionForm{Fields: []pipeline.InputSpec{{Name: "timing", Type: "string", Widget: "select", Options: []string{"now", "later"}, Required: true}, {Name: "ready", Type: "boolean"}}, Actions: []pipeline.DecisionAction{{ID: "ship", Label: "Ship", Approved: true}, {ID: "stop", Label: "Stop"}}}
}

func newWaitpointCardRig(t *testing.T, form *pipeline.DecisionForm) waitpointCardRig {
	t.Helper()
	f := newChatIssueFixture(t)
	seedVersionedPipeline(t, f.h.db, f.workspace, "card-recipe", "card-recipe")
	execOrFatal(t, f.h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at,invocation_source_run_id,invoking_agent_id) VALUES('card-pipeline-run',?,'card-recipe','card-recipe','running','2026-10-16T08:00:00Z',?,?)`, f.workspace, f.run, f.agent)
	h := NewPipelineHandler(f.h.db, newTestLogger(), nil, nil)
	store := pipeline.NewSQLWaitpointStore(f.h.db)
	t.Cleanup(store.Close)
	h.SetWaitpointStore(store)
	token, err := store.CreateApproval(t.Context(), pipeline.WaitpointApprovalRequest{WorkspaceID: f.workspace, PipelineRunID: "card-pipeline-run", StepID: "review", Prompt: "When should this run?", TimeoutSec: 3600, DecisionForm: form, InvokingCrewID: f.crew})
	if err != nil {
		t.Fatal(err)
	}
	a, err := chataudience.CaptureTrusted(t.Context(), f.h.db, f.chat, f.user, nil)
	if err != nil || a == nil {
		t.Fatalf("audience %v %v", a, err)
	}
	var id string
	if err := f.h.db.QueryRow(`SELECT chat_source_id FROM pipeline_waitpoints WHERE token=?`, token).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return waitpointCardRig{f: f, id: id, r: &Router{db: f.h.db, PipelinesHandler: h, logger: newTestLogger()}, token: token, a: *a}
}

func (d waitpointCardRig) read(t *testing.T) ChatCardResponse {
	t.Helper()
	p, err := d.r.waitpointDecisionAdapter().Read(t.Context(), d.a, d.id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Ref
}

func (d waitpointCardRig) answer(version, key, action string) (chatentities.DecisionAnswer, *decisionOperation) {
	values := map[string]json.RawMessage{"timing": json.RawMessage(`"now"`), "ready": json.RawMessage(`false`)}
	answer := chatentities.DecisionAnswer{SourceID: d.id, ExpectedVersion: version, Action: "answer", ActionID: action, Values: values}
	raw, _ := json.Marshal(answer)
	return answer, &decisionOperation{Audience: d.a, Version: version, Key: key, Hash: string(raw)}
}

func (d waitpointCardRig) resolve(t *testing.T, answer chatentities.DecisionAnswer, op *decisionOperation) error {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", nil), d.f.user, d.f.workspace, "OWNER")
	return d.r.waitpointDecisionAdapter().Resolve(req, op, answer)
}

func TestChatDecisionWaitpointReplayCurrentReadAndRevocation(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	pending := d.read(t)
	if pending.Anchor.RunID != d.f.run || pending.Anchor.TriggerMessageID != "trigger-"+d.f.run || len(pending.Decision.WorkflowActions) != 2 || pending.Decision.Fields[0].Type != "choice" {
		t.Fatalf("projection %+v", pending)
	}
	// Original proposing execution may finish; human answer does not admit it.
	execOrFatal(t, d.f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('waitpoint-origin-ended',?,?,?,'run.completed','orchestrator','done','{}')`, d.f.workspace, d.f.agent, d.f.run)
	answer, op := d.answer(pending.Decision.Source.Version, "answer-once", "ship")
	if err := d.resolve(t, answer, op); err != nil {
		t.Fatal(err)
	}
	accepted := d.read(t)
	if accepted.Decision.Status != "approved" || accepted.Decision.Resolution.ActionID != "ship" || string(accepted.Decision.Resolution.Values["ready"]) != "false" {
		t.Fatalf("outcome %+v", accepted.Decision)
	}
	// Ordinary restart preserves chat generation and replays the exact source
	// snapshot from durable state without any prior in-memory store.
	d.r.PipelinesHandler.waitpoints.(*pipeline.SQLWaitpointStore).Close()
	restartedStore := pipeline.NewSQLWaitpointStore(d.f.h.db)
	t.Cleanup(restartedStore.Close)
	restartedHandler := NewPipelineHandler(d.f.h.db, newTestLogger(), nil, nil)
	restartedHandler.SetWaitpointStore(restartedStore)
	d.r = &Router{db: d.f.h.db, PipelinesHandler: restartedHandler, logger: newTestLogger()}
	execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE id=?`, d.a.MemberID)
	live, err := chataudience.CaptureTrusted(t.Context(), d.f.h.db, d.f.chat, d.f.user, nil)
	if err != nil || live == nil {
		t.Fatalf("current audience %v %v", live, err)
	}
	d.a = *live
	op.Audience = *live
	if err := d.resolve(t, answer, op); err != nil {
		t.Fatalf("read replay %v", err)
	}
	beforeJSON, _ := json.Marshal(accepted)
	afterJSON, _ := json.Marshal(d.read(t))
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("ordinary restart changed retry source snapshot")
	}
	if actions := d.read(t).Decision.AllowedActions; len(actions) != 1 || actions[0] != "view" {
		t.Fatalf("viewer actions %v", actions)
	}
	op.Hash += "changed"
	if err := d.resolve(t, answer, op); !errors.Is(err, errDecisionKeyCollision) {
		t.Fatalf("changed intent %v", err)
	}
	execOrFatal(t, d.f.h.db, `DELETE FROM workspace_members WHERE id=?`, d.a.MemberID)
	if err := d.resolve(t, answer, op); !errors.Is(err, errDecisionDenied) {
		t.Fatalf("revoked replay %v", err)
	}
	if _, err := d.r.waitpointDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked read %v", err)
	}
}

func TestChatDecisionWaitpointInboxRaceSeparateConnections(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	version := d.read(t).Decision.Source.Version
	var seq int
	var name, file string
	if err := d.f.h.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", file+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	store := pipeline.NewSQLWaitpointStore(other)
	t.Cleanup(store.Close)
	inboxHandler := NewPipelineHandler(other, newTestLogger(), nil, nil)
	inboxHandler.SetWaitpointStore(store)
	answer, op := d.answer(version, "chat-winner", "ship")
	start := make(chan struct{})
	results := make(chan string, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := d.resolve(t, answer, op); err == nil {
			results <- "chat"
		} else if errors.Is(err, errDecisionVersion) {
			results <- "conflict"
		} else {
			results <- "error:" + err.Error()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		req := withWorkspaceUser(httptest.NewRequest("POST", "/", strings.NewReader(`{"approved":false,"action_id":"stop","data":{}}`)), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("token", d.token)
		rr := httptest.NewRecorder()
		inboxHandler.ApproveWaitpoint(rr, req)
		if rr.Code == 200 {
			results <- "inbox"
		} else if rr.Code == 409 {
			results <- "conflict"
		} else {
			results <- fmt.Sprintf("error:%d %s", rr.Code, rr.Body.String())
		}
	}()
	close(start)
	wg.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		switch result {
		case "chat", "inbox":
			winners++
		case "conflict":
			conflicts++
		default:
			t.Fatal(result)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	final := d.read(t).Decision
	if final.Status != "approved" && final.Status != "rejected" {
		t.Fatalf("terminal %+v", final)
	}
	var inboxStatus string
	if err := d.f.h.db.QueryRow(`SELECT state FROM inbox_items WHERE kind='waitpoint' AND source_id=?`, d.token).Scan(&inboxStatus); err != nil || inboxStatus != "resolved" {
		t.Fatalf("Inbox status %q %v", inboxStatus, err)
	}
}

func TestChatDecisionWaitpointAtomicReceiptFailureAndExpiry(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	version := d.read(t).Decision.Source.Version
	answer, op := d.answer(version, "receipt-key", "ship")
	execOrFatal(t, d.f.h.db, `CREATE TRIGGER reject_waitpoint_receipt BEFORE UPDATE OF answer_operation_key ON pipeline_waitpoints BEGIN SELECT RAISE(ABORT,'receipt failure'); END`)
	if err := d.resolve(t, answer, op); err == nil {
		t.Fatal("receipt failure accepted")
	}
	if card := d.read(t).Decision; card.Status != "pending" || card.Resolution != nil {
		t.Fatalf("partial result %+v", card)
	}
	execOrFatal(t, d.f.h.db, `DROP TRIGGER reject_waitpoint_receipt`)
	execOrFatal(t, d.f.h.db, `UPDATE pipeline_waitpoints SET timeout_at='2020-01-01T00:00:00Z' WHERE token=?`, d.token)
	answer.ExpectedVersion = d.read(t).Decision.Source.Version
	op.Version = answer.ExpectedVersion
	if err := d.resolve(t, answer, op); !errors.Is(err, errDecisionVersion) {
		t.Fatalf("expiry %v", err)
	}
	var status, key string
	if err := d.f.h.db.QueryRow(`SELECT status,COALESCE(answer_operation_key,'') FROM pipeline_waitpoints WHERE token=?`, d.token).Scan(&status, &key); err != nil || status != "timed_out" || key != "" {
		t.Fatalf("expiry status=%q key=%q err=%v", status, key, err)
	}
	if card := d.read(t).Decision; card.Status != "expired" || len(card.AllowedActions) != 1 {
		t.Fatalf("expired %+v", card)
	}
}

func TestChatDecisionWaitpointUnsupportedFormsKeepInbox(t *testing.T) {
	for _, kind := range []string{"array", "object", "default", "too-many", "custom"} {
		t.Run(kind, func(t *testing.T) {
			form := waitpointCardForm()
			data := map[string]any{"timing": "now"}
			switch kind {
			case "array":
				form.Fields = []pipeline.InputSpec{{Name: "items", Type: "array", Widget: "textarea"}}
				data = map[string]any{"items": []any{"one"}}
			case "object":
				form.Fields = []pipeline.InputSpec{{Name: "item", Type: "object", Widget: "textarea"}}
				data = map[string]any{"item": map[string]any{"nested": true}}
			case "default":
				form.Fields[1].Default = false
			case "custom":
				form.Fields[0].AllowCustom = true
			case "too-many":
				for len(form.Fields) < 21 {
					form.Fields = append(form.Fields, pipeline.InputSpec{Name: fmt.Sprintf("field%d", len(form.Fields)), Type: "string"})
				}
			}
			d := newWaitpointCardRig(t, form)
			if _, err := d.r.waitpointDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unsupported source exposed %v", err)
			}
			raw, _ := json.Marshal(map[string]any{"approved": true, "action_id": "ship", "data": data})
			req := withWorkspaceUser(httptest.NewRequest("POST", "/", strings.NewReader(string(raw))), d.f.user, d.f.workspace, "OWNER")
			req.SetPathValue("token", d.token)
			rr := httptest.NewRecorder()
			d.r.PipelinesHandler.ApproveWaitpoint(rr, req)
			if rr.Code != 200 {
				t.Fatalf("old Inbox broken %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestChatDecisionWaitpointProvenanceAndScope(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	version := d.read(t).Decision.Source.Version
	answer, op := d.answer(version, "scope-key", "ship")
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", nil), d.f.user, d.f.workspace, "OWNER")
	req = req.WithContext(context.WithValue(req.Context(), ctxTokenScopes, stringSet{"agents:write": {}}))
	if err := d.r.resolveWaitpointDecision(req, op, answer); !errors.Is(err, errDecisionDenied) {
		t.Fatalf("scope bypass %v", err)
	}
	execOrFatal(t, d.f.h.db, `UPDATE pipeline_runs SET invocation_source_run_id=NULL WHERE id='card-pipeline-run'`)
	if _, err := d.r.waitpointDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy inferred source %v", err)
	}
	execOrFatal(t, d.f.h.db, `UPDATE pipeline_runs SET invocation_source_run_id=? WHERE id='card-pipeline-run'`, d.f.run)
	execOrFatal(t, d.f.h.db, `UPDATE pipeline_runs SET invoking_agent_id='foreign-agent' WHERE id='card-pipeline-run'`)
	if _, err := d.r.waitpointDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign invoking agent %v", err)
	}
	execOrFatal(t, d.f.h.db, `UPDATE pipeline_runs SET invoking_agent_id=? WHERE id='card-pipeline-run'`, d.f.agent)
	foreign := d.a
	foreign.ChatID = "foreign-chat"
	if _, err := d.r.waitpointDecision(t.Context(), foreign, d.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign chat %v", err)
	}
}

func TestChatDecisionWaitpointStaleVersionAndDenial(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	version := d.read(t).Decision.Source.Version
	answer, op := d.answer("stale-version", "stale-key", "ship")
	if err := d.resolve(t, answer, op); !errors.Is(err, errDecisionVersion) {
		t.Fatalf("stale version %v", err)
	}
	if d.read(t).Decision.Status != "pending" {
		t.Fatal("stale answer changed source")
	}
	answer, op = d.answer(version, "denial-key", "stop")
	answer.Values = map[string]json.RawMessage{}
	// Canonical denial permits omission of required fields, never a supplied
	// mistyped value. The source action chooses on-fail, not whole-run approval.
	answer.Values["ready"] = json.RawMessage(`"false"`)
	if err := d.resolve(t, answer, op); !errors.Is(err, errDecisionInput) {
		t.Fatalf("mistyped denial %v", err)
	}
	answer.Values = map[string]json.RawMessage{}
	if err := d.resolve(t, answer, op); err != nil {
		t.Fatal(err)
	}
	if card := d.read(t).Decision; card.Status != "rejected" || card.Resolution.ActionID != "stop" || len(card.Resolution.Values) != 0 {
		t.Fatalf("denial %+v", card)
	}
}

func TestChatDecisionWaitpointLegacyBooleanGate(t *testing.T) {
	d := newWaitpointCardRig(t, nil)
	card := d.read(t).Decision
	if len(card.WorkflowActions) != 0 || len(card.Fields) != 0 {
		t.Fatalf("legacy contract %+v", card)
	}
	answer := chatentities.DecisionAnswer{SourceID: d.id, ExpectedVersion: card.Source.Version, Action: "reject", Values: map[string]json.RawMessage{}}
	op := &decisionOperation{Audience: d.a, Version: answer.ExpectedVersion, Key: "legacy-reject", Hash: "legacy-intent"}
	if err := d.resolve(t, answer, op); err != nil {
		t.Fatal(err)
	}
	if resolved := d.read(t).Decision; resolved.Status != "rejected" || resolved.Resolution.ActionID != "" {
		t.Fatalf("legacy resolution %+v", resolved)
	}
}

func TestChatDecisionWaitpointReleasesConnectionBeforeEffects(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	version := d.read(t).Decision.Source.Version
	d.f.h.db.SetMaxOpenConns(1)
	defer d.f.h.db.SetMaxOpenConns(5)
	answer, op := d.answer(version, "single-connection", "ship")
	done := make(chan error, 1)
	go func() { done <- d.resolve(t, answer, op) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		// Unblock a regressed implementation before cleanup, while preserving
		// the failure: holding the only connection strands postcommit effects.
		d.f.h.db.SetMaxOpenConns(5)
		<-done
		t.Fatal("reserved writer connection retained during postcommit effects")
	}
	if d.read(t).Decision.Status != "approved" {
		t.Fatal("committed answer missing")
	}
}

func TestChatDecisionWaitpointOptionalValuesAndInboxProjection(t *testing.T) {
	for _, mode := range []string{"omitted", "blank", "inbox-null"} {
		t.Run(mode, func(t *testing.T) {
			form := waitpointCardForm()
			form.Fields = append(form.Fields, pipeline.InputSpec{Name: "note", Type: "string"})
			d := newWaitpointCardRig(t, form)
			if mode == "inbox-null" {
				req := withWorkspaceUser(httptest.NewRequest("POST", "/", strings.NewReader(`{"approved":true,"action_id":"ship","data":{"timing":"now","note":"","ready":null}}`)), d.f.user, d.f.workspace, "OWNER")
				req.SetPathValue("token", d.token)
				rr := httptest.NewRecorder()
				d.r.PipelinesHandler.ApproveWaitpoint(rr, req)
				if rr.Code != 200 {
					t.Fatalf("Inbox answer %d %s", rr.Code, rr.Body.String())
				}
			} else {
				answer, op := d.answer(d.read(t).Decision.Source.Version, "optional-key", "ship")
				delete(answer.Values, "ready")
				if mode == "blank" {
					answer.Values["note"] = json.RawMessage(`""`)
				}
				if err := d.resolve(t, answer, op); err != nil {
					t.Fatal(err)
				}
			}
			card := d.read(t).Decision
			if card.Status != "approved" {
				t.Fatalf("optional terminal %+v", card)
			}
			if _, exists := card.Resolution.Values["ready"]; exists {
				t.Fatalf("absent optional value exposed %+v", card.Resolution)
			}
			if mode != "omitted" && string(card.Resolution.Values["note"]) != `""` {
				t.Fatalf("optional blank lost %+v", card.Resolution)
			}
			if mode == "inbox-null" {
				var payload string
				if err := d.f.h.db.QueryRow(`SELECT decision_payload FROM pipeline_waitpoints WHERE token=?`, d.token).Scan(&payload); err != nil || !strings.Contains(payload, `"ready":null`) {
					t.Fatalf("canonical payload changed %s %v", payload, err)
				}
			}
		})
	}
}

func TestChatDecisionWaitpointUnrepresentableAnswerHasNoEffect(t *testing.T) {
	form := waitpointCardForm()
	form.Fields = append(form.Fields, pipeline.InputSpec{Name: "note", Type: "string"})
	d := newWaitpointCardRig(t, form)
	version := d.read(t).Decision.Source.Version
	for _, mode := range []string{"explicit-null", "oversized-text"} {
		answer, op := d.answer(version, mode, "ship")
		if mode == "explicit-null" {
			answer.Values["ready"] = json.RawMessage(`null`)
		} else {
			raw, _ := json.Marshal(strings.Repeat("x", 10001))
			answer.Values["note"] = raw
		}
		if err := d.resolve(t, answer, op); !errors.Is(err, errDecisionInput) {
			t.Fatalf("%s accepted: %v", mode, err)
		}
		if card := d.read(t).Decision; card.Status != "pending" || card.Resolution != nil {
			t.Fatalf("%s changed source %+v", mode, card)
		}
	}
}
