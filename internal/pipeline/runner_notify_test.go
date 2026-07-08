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

// fakeNoticeCounter reports a fixed prior-notice count per (run, recipient)
// key so the soft-cap tests can drive runNotifyStep to either side of the
// cap without a real inbox. recipientKey mirrors the production counter's
// grouping: user id, else role, else "" for a workspace notice.
type fakeNoticeCounter struct {
	counts map[string]int
	calls  int
	err    error
}

func (c *fakeNoticeCounter) count(_ context.Context, _, runID, targetUserID, targetRole string) (int, error) {
	c.calls++
	if c.err != nil {
		return 0, c.err
	}
	recip := targetUserID
	if recip == "" {
		recip = targetRole
	}
	return c.counts[runID+"|"+recip], nil
}

// TestRunNotifyStep_SoftCap_SkipsOverCap pins the per-recipient anti-spam
// soft cap: once a run has already delivered perRunNotifyCap notices to a
// recipient, a further notify to that SAME recipient is dropped (non-fatal,
// no inbox write) while the run continues.
func TestRunNotifyStep_SoftCap_SkipsOverCap(t *testing.T) {
	fake := &fakeInboxNotifier{}
	counter := &fakeNoticeCounter{counts: map[string]int{
		"run_1|u_bob": perRunNotifyCap, // already at the cap for u_bob
	}}
	exec := notifyExecutor(fake).WithNoticeCounter(counter.count)

	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "user:u_bob", Body: "flood"}}
	out, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1")
	if err != nil {
		t.Fatalf("runNotifyStep must not fail the run when capped: %v", err)
	}
	if len(fake.items) != 0 {
		t.Fatalf("over-cap notice must NOT be written, got %d items", len(fake.items))
	}
	if !strings.HasPrefix(out, "notified:capped") {
		t.Errorf("output marker = %q, want notified:capped", out)
	}
}

// TestRunNotifyStep_SoftCap_UnderCapPosts confirms a notice below the cap
// is delivered normally, and that the cap is per-recipient: a different
// recipient with its own budget is unaffected.
func TestRunNotifyStep_SoftCap_UnderCapPosts(t *testing.T) {
	fake := &fakeInboxNotifier{}
	counter := &fakeNoticeCounter{counts: map[string]int{
		"run_1|u_bob": perRunNotifyCap,     // bob is capped …
		"run_1|u_ann": perRunNotifyCap - 1, // … ann still has one slot
	}}
	exec := notifyExecutor(fake).WithNoticeCounter(counter.count)

	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "user:u_ann", Body: "hi"}}
	out, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1")
	if err != nil {
		t.Fatalf("runNotifyStep: %v", err)
	}
	if len(fake.items) != 1 {
		t.Fatalf("under-cap notice must be written, got %d items", len(fake.items))
	}
	if strings.HasPrefix(out, "notified:capped") {
		t.Errorf("under-cap notice wrongly marked capped: %q", out)
	}
}

// TestRunNotifyStep_SoftCap_FailsOpen pins the best-effort contract: a
// counter error must NOT drop the notice or fail the run — anti-spam is a
// courtesy, delivery is the priority.
func TestRunNotifyStep_SoftCap_FailsOpen(t *testing.T) {
	fake := &fakeInboxNotifier{}
	counter := &fakeNoticeCounter{err: errors.New("db down")}
	exec := notifyExecutor(fake).WithNoticeCounter(counter.count)

	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "user:u_bob", Body: "hi"}}
	_, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1")
	if err != nil {
		t.Fatalf("counter error must not fail the run: %v", err)
	}
	if len(fake.items) != 1 {
		t.Fatalf("counter error must fail open (deliver), got %d items", len(fake.items))
	}
}

// TestRunNotifyStep_TemplatedCrewDegradesVisibly pins the run-time behaviour
// of a templated `to` that renders to an unsupported crew:<slug> (the only
// way crew: reaches run time — literal crew: is rejected at save). It must
// NOT fail the run (non-blocking contract) but ALSO must not look like a
// clean targeted send: the notice degrades to a workspace notice AND the
// step output is marked `notified:degraded` so it's distinguishable.
func TestRunNotifyStep_TemplatedCrewDegradesVisibly(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := notifyExecutor(fake)

	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{
		To:   "crew:{{ inputs.team }}", // renders to crew:sales at run time
		Body: "done",
	}}
	render := RenderContext{Inputs: map[string]any{"team": "sales"}}
	in := notifyRunInput("ws_1", "u_trig")

	out, _, _, err := exec.runNotifyStep(context.Background(), step, render, in, "run_1")
	if err != nil {
		t.Fatalf("templated crew: must not fail the run: %v", err)
	}
	if !strings.HasPrefix(out, "notified:degraded") {
		t.Errorf("output = %q, want notified:degraded prefix (silent fallback would be a bug)", out)
	}
	if len(fake.items) != 1 {
		t.Fatalf("degraded notice should still land as a workspace notice, got %d items", len(fake.items))
	}
	if fake.items[0].TargetUserID != "" || fake.items[0].TargetRole != "" {
		t.Errorf("degraded notice must be workspace-wide (empty target), got user=%q role=%q",
			fake.items[0].TargetUserID, fake.items[0].TargetRole)
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
	}{
		{"workspace", "u1", "", ""},
		{"", "u1", "", ""},
		{"trigger", "u_trig", "u_trig", ""},
		{"trigger", "", "", ""}, // no attributed user → workspace fallback
		{"user:u_bob", "u1", "u_bob", ""},
		{"role:MANAGER", "u1", "", "MANAGER"},
		{"role:owner", "u1", "", "OWNER"}, // case-insensitive
		// Bad / unsupported targets DEGRADE to a workspace notice at run
		// time — a notify step must never fail the run. The literal forms
		// are rejected at author time (see TestResolveNotifyTarget +
		// TestValidate_NotifyStep); this covers the run-time safety net.
		{"role:VIEWER", "u1", "", ""},
		{"user:", "u1", "", ""},
		{"crew:sales", "u1", "", ""}, // deferred to Phase 1
		{"garbage", "u1", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.to+"/"+tc.invokingUser, func(t *testing.T) {
			fake := &fakeInboxNotifier{}
			exec := notifyExecutor(fake)
			step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: tc.to, Body: "hi"}}
			_, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", tc.invokingUser), "run_1")
			if err != nil {
				t.Fatalf("to=%q: notify must never fail the run, got %v", tc.to, err)
			}
			if len(fake.items) != 1 {
				t.Fatalf("to=%q: want 1 inbox item (targeted or degraded), got %d", tc.to, len(fake.items))
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

// TestResolveNotifyTarget pins the PURE resolver's error contract — the
// one author-time Validate relies on. (runNotifyStep degrades on these at
// run time; Validate rejects the literal forms up front.)
func TestResolveNotifyTarget(t *testing.T) {
	ok := []struct{ to, wantUser, wantRole string }{
		{"", "", ""}, {"workspace", "", ""},
		{"trigger", "u_trig", ""},
		{"user:u_bob", "u_bob", ""},
		{"role:MANAGER", "", "MANAGER"}, {"role:owner", "", "OWNER"},
	}
	for _, tc := range ok {
		u, r, err := resolveNotifyTarget(tc.to, "u_trig")
		if err != nil || u != tc.wantUser || r != tc.wantRole {
			t.Errorf("resolveNotifyTarget(%q) = (%q,%q,%v), want (%q,%q,nil)", tc.to, u, r, err, tc.wantUser, tc.wantRole)
		}
	}
	for _, bad := range []string{"role:VIEWER", "user:", "crew:sales", "garbage"} {
		if _, _, err := resolveNotifyTarget(bad, "u_trig"); err == nil {
			t.Errorf("resolveNotifyTarget(%q) should error", bad)
		}
	}
}

// TestRunNotifyStep_UnresolvableTemplatedTargetDegrades: a templated `to`
// that renders empty at run time (input not supplied) must degrade to a
// workspace notice, not fail the run.
func TestRunNotifyStep_UnresolvableTemplatedTargetDegrades(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := notifyExecutor(fake)
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "user:{{ inputs.assignee }}", Body: "hi"}}
	// No inputs → renders "user:" → unresolvable.
	_, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1")
	if err != nil {
		t.Fatalf("templated-empty target must not fail the run, got %v", err)
	}
	if len(fake.items) != 1 || fake.items[0].TargetUserID != "" {
		t.Errorf("expected a workspace-degraded item, got %+v", fake.items)
	}
}

// TestRunNotifyStep_MembershipGuard: a user: target that isn't a workspace
// member degrades to workspace; a member keeps the target.
func TestRunNotifyStep_MembershipGuard(t *testing.T) {
	step := Step{ID: "n", Type: StepNotify, Notify: &NotifyStep{To: "user:u_bob", Body: "hi"}}

	// Non-member → degrade to workspace.
	fakeA := &fakeInboxNotifier{}
	execA := notifyExecutor(fakeA).WithMemberChecker(func(_ context.Context, _, _ string) (bool, error) { return false, nil })
	if _, _, _, err := execA.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	if fakeA.items[0].TargetUserID != "" {
		t.Errorf("non-member target should degrade to workspace, got %q", fakeA.items[0].TargetUserID)
	}

	// Member → keep the target.
	fakeB := &fakeInboxNotifier{}
	execB := notifyExecutor(fakeB).WithMemberChecker(func(_ context.Context, _, _ string) (bool, error) { return true, nil })
	if _, _, _, err := execB.runNotifyStep(context.Background(), step, RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	if fakeB.items[0].TargetUserID != "u_bob" {
		t.Errorf("member target should be kept, got %q", fakeB.items[0].TargetUserID)
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
