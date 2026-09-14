package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func richDecisionFixture() *DecisionForm {
	return &DecisionForm{Fields: []InputSpec{{Name: "count", Type: "integer", Required: true, Default: 0}, {Name: "enabled", Type: "boolean", Required: true, Default: false}, {Name: "note", Type: "string", Required: true}}, Actions: []DecisionAction{{ID: "ship", Label: "Ship", Approved: true}, {ID: "revise", Label: "Revise", Approved: true}, {ID: "stop", Label: "Stop", Approved: false}}}
}

func TestDecisionFormsTypedValidation(t *testing.T) {
	form := richDecisionFixture()
	for _, tc := range []struct {
		name, payload   string
		approved, valid bool
	}{
		{"defaults", `{"action_id":"ship","data":{"note":"ok"}}`, true, true},
		{"missing", `{"action_id":"ship","data":{}}`, true, false},
		{"wrong-type", `{"action_id":"ship","data":{"note":"ok","enabled":"false"}}`, true, false},
		{"forged-action", `{"action_id":"unknown","data":{"note":"ok"}}`, true, false},
		{"wrong-verdict", `{"action_id":"stop","data":{}}`, true, false},
		{"unknown-field", `{"action_id":"ship","data":{"note":"ok","secret":"x"}}`, true, false},
		{"reject-without-fields", `{"action_id":"stop"}`, false, true},
		{"legacy-client", ``, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NormalizeDecisionAnswer(form, tc.approved, tc.payload)
			if (err == nil) != tc.valid {
				t.Fatalf("%s %v", out, err)
			}
			if err != nil && !errors.Is(err, ErrDecisionInput) {
				t.Fatal(err)
			}
			if tc.valid {
				var a DecisionAnswer
				json.Unmarshal([]byte(out), &a)
				if a.Data["count"] != float64(0) || a.Data["enabled"] != false {
					t.Fatal(a)
				}
			}
		})
	}
	form.Fields = append(form.Fields, form.Fields[0])
	if ValidateDecisionForm(form) == nil {
		t.Fatal("duplicate field accepted")
	}
	form = richDecisionFixture()
	form.Fields[0].Type = "credential"
	if ValidateDecisionForm(form) == nil {
		t.Fatal("unsupported field accepted")
	}
}

func TestDecisionFormSnapshotRaceAndRestart(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	form := richDecisionFixture()
	token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{WorkspaceID: "ws_test", PipelineRunID: "run_form", StepID: "gate", DecisionForm: form})
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the authoring object cannot change the question already asked.
	form.Actions[0].ID = "different"
	form.Fields[2].Type = "boolean"
	if err := store.CompleteApproval(ctx, "other", token, true, "u", `{"action_id":"ship","data":{"note":"ok"}}`); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatal(err)
	}
	if err := store.CompleteApproval(ctx, "ws_test", token, true, "u", ""); !errors.Is(err, ErrDecisionInput) {
		t.Fatal(err)
	}
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, action := range []string{"ship", "revise"} {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			outcomes <- store.CompleteApproval(ctx, "ws_test", token, true, a, `{"action_id":"`+a+`","data":{"note":"{{ inputs.secret }}"}}`)
		}(action)
	}
	wg.Wait()
	close(outcomes)
	wins := 0
	for err := range outcomes {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrAlreadyDecided) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("accepted %d decisions", wins)
	}
	restarted := NewSQLWaitpointStore(store.db)
	defer restarted.Close()
	step := Step{ID: "gate", Type: StepWait, Wait: &WaitStep{Kind: "approval", ApprovalPrompt: "Review", DecisionForm: richDecisionFixture()}}
	e := &Executor{waitpoints: restarted}
	out, _, _, err := e.runWaitStep(ctx, step, emptyRender(), RunInput{WorkspaceID: "ws_test", resume: true}, "run_form", 0)
	if err != nil {
		t.Fatal(err)
	}
	var answer DecisionAnswer
	if json.Unmarshal([]byte(out), &answer) != nil || answer.Data["note"] != "{{ inputs.secret }}" || answer.Data["enabled"] != false {
		t.Fatal(out)
	}
	if answer.ActionID != "ship" && answer.ActionID != "revise" {
		t.Fatal(out)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_waitpoints`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restart minted %d tokens", count)
	}
}

func TestDecisionFormNeverUsesStandingTrust(t *testing.T) {
	// Even a fully identified gate must not reach the trust store for forms.
	s := &SQLWaitpointStore{}
	req := WaitpointApprovalRequest{DecisionForm: richDecisionFixture()}
	tc := routineTrustCtx{pipelineID: "p", definitionHash: "hash"}
	if _, ok := s.consumeTrust(context.Background(), req, tc); ok {
		t.Fatal("auto approved")
	}
	if s.trustOffer(context.Background(), req, tc)["eligible"] != false {
		t.Fatal("offered standing trust")
	}
}

func TestDecisionFormRequiresFieldsArray(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []InputSpec
		valid  bool
	}{
		{"missing", nil, false}, {"empty", []InputSpec{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := richDecisionFixture()
			form.Fields = tc.fields
			if err := ValidateDecisionForm(form); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestDecisionCrossingDeadlineCannotCommit(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := t.Context()
	token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{WorkspaceID: "ws_test", PipelineRunID: "run_deadline", StepID: "gate", DecisionForm: richDecisionFixture()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour)
	if _, err := store.db.ExecContext(ctx, `UPDATE pipeline_waitpoints SET timeout_at=? WHERE token=?`, tsformat.Format(deadline), token); err != nil {
		t.Fatal(err)
	}
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 1 {
			return deadline.Add(-time.Second)
		}
		return deadline.Add(time.Second)
	}
	err = store.completeApproval(ctx, "ws_test", token, true, "user", `{"action_id":"ship","data":{"note":"ok"}}`, clock)
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("decision crossed its deadline but was accepted: %v", err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM pipeline_waitpoints WHERE token=?`, token).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "timed_out" {
		t.Fatalf("expired decision remained %s", status)
	}
}
