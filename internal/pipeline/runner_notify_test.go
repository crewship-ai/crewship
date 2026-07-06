package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// fakeInboxNotifier captures the inbox items a notify step emits so tests
// can assert the composed Item without a real DB.
type fakeInboxNotifier struct {
	items []inbox.Item
	err   error
}

func (f *fakeInboxNotifier) Notify(_ context.Context, item inbox.Item) error {
	f.items = append(f.items, item)
	return f.err
}

func notifyExecutor(fake InboxNotifier) *Executor {
	e := NewExecutor(nil, nil, nil, nil)
	if fake != nil {
		e = e.WithInboxNotifier(fake)
	}
	return e
}

func notifyRunInput(ws, invokingUser string) RunInput {
	in := RunInput{WorkspaceID: ws, InvokingUserID: invokingUser, Mode: ModeRun}
	in.dsl = &DSL{Name: "invoice-bot"}
	return in
}

// TestRunNotifyStep_PostsMessageToInbox pins the core contract: a notify
// step renders its title/body from the run context and emits a
// non-blocking `message` inbox item with an idempotent SourceID.
func TestRunNotifyStep_PostsMessageToInbox(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := notifyExecutor(fake)

	step := Step{ID: "tell", Type: StepNotify, Notify: &NotifyStep{
		To:    "workspace",
		Title: "Invoices parsed",
		Body:  "Parsed {{ steps.extract.output }} invoices.",
	}}
	render := RenderContext{StepOutputs: map[string]string{"extract": "3"}}

	out, cost, _, err := exec.runNotifyStep(context.Background(), step, render, notifyRunInput("ws_1", ""), "run_1")
	if err != nil {
		t.Fatalf("runNotifyStep: %v", err)
	}
	if cost != 0 {
		t.Errorf("notify step must be free, got cost %v", cost)
	}
	if !strings.HasPrefix(out, "notified:") {
		t.Errorf("output marker %q must start with notified:", out)
	}
	if len(fake.items) != 1 {
		t.Fatalf("want 1 inbox item, got %d", len(fake.items))
	}
	it := fake.items[0]
	if it.Kind != inbox.KindMessage {
		t.Errorf("kind = %q, want %q", it.Kind, inbox.KindMessage)
	}
	if it.Blocking {
		t.Error("notify must be non-blocking (Blocking=false)")
	}
	if it.SourceID != "run_1:tell" {
		t.Errorf("SourceID = %q, want run_1:tell (idempotency key)", it.SourceID)
	}
	if it.WorkspaceID != "ws_1" {
		t.Errorf("WorkspaceID = %q, want ws_1", it.WorkspaceID)
	}
	if it.SenderType != "pipeline" {
		t.Errorf("SenderType = %q, want pipeline", it.SenderType)
	}
	if it.SenderName != "invoice-bot" {
		t.Errorf("SenderName = %q, want the routine name", it.SenderName)
	}
	if it.Title != "Invoices parsed" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.BodyMD != "Parsed 3 invoices." {
		t.Errorf("BodyMD = %q, want the template rendered", it.BodyMD)
	}
	if it.Payload["subkind"] != "routine_update" {
		t.Errorf("Payload[subkind] = %v, want routine_update (filterable lane)", it.Payload["subkind"])
	}
	if it.Payload["pipeline_run_id"] != "run_1" || it.Payload["step_id"] != "tell" {
		t.Errorf("Payload run/step = %v/%v", it.Payload["pipeline_run_id"], it.Payload["step_id"])
	}
}

// TestRunNotifyStep_TargetResolution pins how the `to` field maps to
// inbox targeting, including the trigger→workspace fallback.
func TestRunNotifyStep_TargetResolution(t *testing.T) {
	cases := []struct {
		to           string
		invokingUser string
		wantUser     string
		wantRole     string
		wantErr      bool
	}{
		{"workspace", "u1", "", "", false},
		{"", "u1", "", "", false},
		{"trigger", "u_trig", "u_trig", "", false},
		{"trigger", "", "", "", false}, // no attributed user → workspace fallback
		{"user:u_bob", "u1", "u_bob", "", false},
		{"role:MANAGER", "u1", "", "MANAGER", false},
		{"role:owner", "u1", "", "OWNER", false}, // case-insensitive
		{"role:VIEWER", "u1", "", "", true},      // not a targetable role
		{"user:", "u1", "", "", true},            // empty id
		{"crew:sales", "u1", "", "", true},       // deferred to Phase 1
		{"garbage", "u1", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.to+"/"+tc.invokingUser, func(t *testing.T) {
			fake := &fakeInboxNotifier{}
			exec := notifyExecutor(fake)
			step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: tc.to, Body: "hi"}}
			_, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", tc.invokingUser), "run_1")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("to=%q: want error, got none", tc.to)
				}
				if len(fake.items) != 0 {
					t.Errorf("to=%q: bad target must not emit an inbox item", tc.to)
				}
				return
			}
			if err != nil {
				t.Fatalf("to=%q: unexpected error: %v", tc.to, err)
			}
			it := fake.items[0]
			if it.TargetUserID != tc.wantUser {
				t.Errorf("to=%q: TargetUserID = %q, want %q", tc.to, it.TargetUserID, tc.wantUser)
			}
			if it.TargetRole != tc.wantRole {
				t.Errorf("to=%q: TargetRole = %q, want %q", tc.to, it.TargetRole, tc.wantRole)
			}
		})
	}
}

// TestRunNotifyStep_RedactsSecrets proves mid-run output that carries a
// secret is scrubbed before it lands in the inbox.
func TestRunNotifyStep_RedactsSecrets(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := notifyExecutor(fake)
	const secret = "AKIAIOSFODNN7EXAMPLE"
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{
		To:    "workspace",
		Title: "creds " + secret,
		Body:  "leaked " + secret,
	}}
	if _, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	it := fake.items[0]
	if strings.Contains(it.BodyMD, secret) {
		t.Errorf("BodyMD still contains the secret: %q", it.BodyMD)
	}
	if strings.Contains(it.Title, secret) {
		t.Errorf("Title still contains the secret: %q", it.Title)
	}
}

// TestRunNotifyStep_DryRunNoSideEffect pins that a draft dry-run renders
// and validates the step but never writes to the inbox.
func TestRunNotifyStep_DryRunNoSideEffect(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := notifyExecutor(fake)
	in := notifyRunInput("ws_1", "")
	in.Mode = ModeDryRun
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "workspace", Body: "hi"}}
	out, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, in, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.items) != 0 {
		t.Errorf("dry-run must not emit an inbox item, got %d", len(fake.items))
	}
	if !strings.Contains(out, "preview") {
		t.Errorf("dry-run marker = %q, want a preview marker", out)
	}
}

// TestRunNotifyStep_DryRunStillValidatesTarget: a bad `to` must surface
// in the draft dry-run (the save gate), not only at real-run time.
func TestRunNotifyStep_DryRunStillValidatesTarget(t *testing.T) {
	exec := notifyExecutor(&fakeInboxNotifier{})
	in := notifyRunInput("ws_1", "")
	in.Mode = ModeDryRun
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "role:VIEWER", Body: "hi"}}
	if _, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, in, "run_1"); err == nil {
		t.Fatal("dry-run must reject an invalid target")
	}
}

// TestRunNotifyStep_NonBlocking pins the "the bell couldn't ring must not
// fail the run" contract: a missing sink and a sink error are both
// non-fatal.
func TestRunNotifyStep_NonBlocking(t *testing.T) {
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "workspace", Body: "hi"}}

	// (a) no notifier wired (dev/test/misconfig) — skip, don't fail.
	execNil := notifyExecutor(nil)
	if _, _, _, err := execNil.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Errorf("missing notifier must be non-fatal, got %v", err)
	}

	// (b) notifier returns an error — log + carry on.
	fake := &fakeInboxNotifier{err: errors.New("db down")}
	execErr := notifyExecutor(fake)
	if _, _, _, err := execErr.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Errorf("notifier delivery error must be non-fatal, got %v", err)
	}
}

// TestRunNotifyStep_PriorityDefault pins the priority passthrough +
// default.
func TestRunNotifyStep_PriorityDefault(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := notifyExecutor(fake)
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "workspace", Body: "hi", Priority: "high"}}
	if _, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	if fake.items[0].Priority != "high" {
		t.Errorf("Priority = %q, want high", fake.items[0].Priority)
	}
}

// TestValidate_NotifyStep pins author-time validation via the offline
// Validate path (the same one `crewship routine validate` runs).
func TestValidate_NotifyStep(t *testing.T) {
	cases := []struct {
		name    string
		notify  string
		wantErr bool
	}{
		{"valid workspace", `{"to":"workspace","title":"hi","body":"there"}`, false},
		{"valid trigger", `{"to":"trigger","body":"done"}`, false},
		{"valid role", `{"to":"role:MANAGER","body":"done"}`, false},
		{"templated target ok", `{"to":"user:{{ inputs.uid }}","body":"done"}`, false},
		{"missing to", `{"title":"hi","body":"there"}`, true},
		{"missing title and body", `{"to":"workspace"}`, true},
		{"bad role", `{"to":"role:VIEWER","body":"x"}`, true},
		{"bad target", `{"to":"nonsense","body":"x"}`, true},
		{"bad priority", `{"to":"workspace","body":"x","priority":"screaming"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dslJSON := `{"dsl_version":"1.0","name":"t","steps":[{"id":"n","type":"notify","notify":` + tc.notify + `}]}`
			dsl, err := Parse([]byte(dslJSON))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(dsl, nil, nil)
			if tc.wantErr && err == nil {
				t.Errorf("want validation error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want valid, got %v", err)
			}
		})
	}
}
