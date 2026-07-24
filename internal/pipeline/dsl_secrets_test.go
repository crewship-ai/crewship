package pipeline

// dsl_secrets_test.go — pins the {{ secrets.<type> }} render namespace
// (#1418): a code/script/notify/http step resolves a workspace-vault
// credential BY TYPE at render time, the injected value reaches the
// runner, and — critically — the resolved value is SCRUBBED from the
// step output, the error text, and never lands in the versioned DSL.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stripeSecretValue is a fake decrypted credential value used to prove
// injection + scrub. Not a real secret — a fixed sentinel we assert is
// present where injected and ABSENT from every author-visible surface.
const stripeSecretValue = "sk_live_SECRETVALUE_1418" // gitleaks:allow

// fakeSecretResolver returns stripeSecretValue for type "stripe"
// (case-insensitive), an error for everything else — mirroring the vault
// resolver's "no ACTIVE credential of this type" contract.
func fakeSecretResolver(_ context.Context, _ RunScope, credType string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(credType), "stripe") {
		return stripeSecretValue, nil
	}
	return "", errors.New("no active credential of type " + credType)
}

// fakeCodeRunner echoes a canned stdout / error so the scrub can be
// asserted against runner output the author never controls.
type fakeCodeRunner struct {
	last   CodeRunRequest
	result CodeRunResult
	err    error
}

func (f *fakeCodeRunner) RunCode(_ context.Context, req CodeRunRequest) (CodeRunResult, error) {
	f.last = req
	return f.result, f.err
}

func TestSecretsNamespace_ScriptStep_InjectsAndScrubs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	// The runner echoes the secret in stdout AND returns it inside an
	// error — both are surfaces the resolved value must NEVER survive on.
	fake := &fakeScriptRunner{
		result: ScriptRunResult{Stdout: "charged via " + stripeSecretValue + " ok", ExitCode: 0},
	}
	exec := NewExecutor(store, resolver, nil, nil).
		WithScriptRunner(fake).
		WithCredentialResolver(fakeSecretResolver)

	step := Step{
		ID:   "charge",
		Type: StepScript,
		Script: &ScriptStep{
			Path: "scripts/charge.py",
			Args: []string{"--key", "{{ secrets.stripe }}"},
			Env:  map[string]string{"STRIPE_KEY": "{{ secrets.stripe }}"},
		},
	}
	rc := RenderContext{Inputs: map[string]any{}}
	in := RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"}

	out, _, _, err := exec.runScriptStep(context.Background(), step, rc, in, "run_1")
	if err != nil {
		t.Fatalf("script step: %v", err)
	}

	// 1) The runner actually RECEIVED the injected value (this is the DX
	//    win — a script step gets a sanctioned secret path).
	if fake.last.Env["STRIPE_KEY"] != stripeSecretValue {
		t.Errorf("STRIPE_KEY env = %q, want the injected secret value", fake.last.Env["STRIPE_KEY"])
	}
	if len(fake.last.Args) != 2 || fake.last.Args[1] != stripeSecretValue {
		t.Errorf("args = %v, want the injected secret in position 1", fake.last.Args)
	}

	// 2) The step OUTPUT is scrubbed — the echoed value never propagates
	//    downstream (into step_outputs_json) as plaintext.
	if strings.Contains(out, stripeSecretValue) {
		t.Errorf("step output leaked the secret value: %q", out)
	}
	if !strings.Contains(out, secretRedactionMarker) {
		t.Errorf("step output missing redaction marker: %q", out)
	}

	// 3) The versioned DSL carries only the TEMPLATE, never the value.
	raw, _ := json.Marshal(step)
	if strings.Contains(string(raw), stripeSecretValue) {
		t.Errorf("versioned DSL leaked the secret value: %s", raw)
	}
	if !strings.Contains(string(raw), "{{ secrets.stripe }}") {
		t.Errorf("versioned DSL lost the secret template ref: %s", raw)
	}
}

func TestSecretsNamespace_ScriptStep_ScrubsError(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	fake := &fakeScriptRunner{
		result: ScriptRunResult{Stdout: "", Stderr: "auth failed for " + stripeSecretValue, ExitCode: 7},
	}
	exec := NewExecutor(store, resolver, nil, nil).
		WithScriptRunner(fake).
		WithCredentialResolver(fakeSecretResolver)

	step := Step{
		ID:   "charge",
		Type: StepScript,
		Script: &ScriptStep{
			Path: "scripts/charge.py",
			Env:  map[string]string{"STRIPE_KEY": "{{ secrets.stripe }}"},
		},
	}
	_, _, _, err := exec.runScriptStep(context.Background(), step,
		RenderContext{Inputs: map[string]any{}},
		RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"}, "run_1")
	if err == nil {
		t.Fatal("expected error from non-zero exit code")
	}
	if strings.Contains(err.Error(), stripeSecretValue) {
		t.Errorf("error message leaked the secret value: %v", err)
	}
}

func TestSecretsNamespace_CodeStep_InjectsAndScrubs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	fake := &fakeCodeRunner{
		result: CodeRunResult{Stdout: "used " + stripeSecretValue, ExitCode: 0},
	}
	exec := NewExecutor(store, resolver, nil, nil).
		WithCodeRunner(fake).
		WithCredentialResolver(fakeSecretResolver)

	step := Step{
		ID:   "call",
		Type: StepCode,
		Code: &CodeStep{
			Runtime: "python",
			Code:    "print('x')",
			Env:     map[string]string{"KEY": "{{ secrets.stripe }}"},
		},
	}
	out, _, _, err := exec.runCodeStep(context.Background(), step,
		RenderContext{Inputs: map[string]any{}},
		RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"})
	if err != nil {
		t.Fatalf("code step: %v", err)
	}
	if fake.last.InputEnv["KEY"] != stripeSecretValue {
		t.Errorf("code env KEY = %q, want the injected secret", fake.last.InputEnv["KEY"])
	}
	if strings.Contains(out, stripeSecretValue) {
		t.Errorf("code step output leaked the secret: %q", out)
	}
}

// TestSecretsNamespace_NoRefs_NoResolverCalls proves the zero-overhead
// contract: a step with no {{ secrets.* }} refs never calls the resolver
// (no vault hit) and its output is returned untouched.
func TestSecretsNamespace_NoRefs_NoResolverCalls(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "plain output", ExitCode: 0}}
	called := false
	exec := NewExecutor(store, resolver, nil, nil).
		WithScriptRunner(fake).
		WithCredentialResolver(func(_ context.Context, _ RunScope, _ string) (string, error) {
			called = true
			return "", nil
		})

	step := Step{ID: "s", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.sh"}}
	out, _, _, err := exec.runScriptStep(context.Background(), step,
		RenderContext{Inputs: map[string]any{}},
		RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"}, "run_1")
	if err != nil {
		t.Fatalf("script step: %v", err)
	}
	if called {
		t.Error("resolver was called for a step with no secret refs")
	}
	if out != "plain output" {
		t.Errorf("output = %q, want untouched", out)
	}
}

// TestSecretsNamespace_HTTPStep_InjectsAndScrubs proves the http path
// unifies with credential_ref: a {{ secrets.* }} value renders into the
// request body (reaches the server) and — if the endpoint reflects it —
// is scrubbed back out of the response that becomes the step output.
func TestSecretsNamespace_HTTPStep_InjectsAndScrubs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("echo: " + seenBody)) // reflect the secret back
	}))
	defer srv.Close()

	exec := NewExecutor(store, resolver, nil, nil).WithCredentialResolver(fakeSecretResolver)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{ID: "post", Type: StepHTTP, HTTP: &HTTPStep{
		Method: "POST", URL: srv.URL,
		Body: `{"key":"{{ secrets.stripe }}"}`,
	}}
	out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{},
		RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"})
	if err != nil {
		t.Fatalf("http step: %v", err)
	}
	// The server RECEIVED the real injected value.
	if !strings.Contains(seenBody, stripeSecretValue) {
		t.Errorf("server body = %q, want the injected secret", seenBody)
	}
	// The step OUTPUT (reflected response) is scrubbed.
	if strings.Contains(out, stripeSecretValue) {
		t.Errorf("http step output leaked the reflected secret: %q", out)
	}
	if !strings.Contains(out, secretRedactionMarker) {
		t.Errorf("http step output missing redaction marker: %q", out)
	}
}

// TestSecretsNamespace_NotifyStep_SecretInToDoesNotLeak pins the leak
// fix: a notify `to:` selector is a routing ADDRESS, never a place to
// resolve a vault value. If {{ secrets.* }} in `to:` were resolved, the
// rendered value would land in toRaw — which is logged AND persisted as a
// run warning when the (now-secret-shaped) target fails to resolve — and
// the deferred scrub only covers the step's return value, not those side
// channels. So `to:` must not be part of the secret-resolution set at all;
// the secret value must never reach the run warning or the marker.
func TestSecretsNamespace_NotifyStep_SecretInToDoesNotLeak(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()

	// A pipeline_runs row is required for AppendWarning to persist onto.
	if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at)
VALUES ('run_leak', 'ws_runs', 'pln_x', 'leak-bot', 'running', '2026-07-24T00:00:00Z')`); err != nil {
		t.Fatalf("seed run row: %v", err)
	}

	fake := &fakeInboxNotifier{}
	exec := NewExecutor(nil, nil, nil, nil).
		WithRunStore(store).
		WithInboxNotifier(fake).
		WithCredentialResolver(fakeSecretResolver)

	step := Step{ID: "tell", Type: StepNotify, Notify: &NotifyStep{
		To:    "{{ secrets.stripe }}",
		Title: "done",
		Body:  "all good",
	}}
	in := RunInput{WorkspaceID: "ws_runs", Mode: ModeRun}
	in.dsl = &DSL{Name: "leak-bot"}

	out, _, _, err := exec.runNotifyStep(context.Background(), step, RenderContext{}, in, "run_leak")
	if err != nil {
		t.Fatalf("runNotifyStep: %v", err)
	}

	// 1) The returned marker must not carry the resolved secret.
	if strings.Contains(out, stripeSecretValue) {
		t.Errorf("notify marker leaked the secret value: %q", out)
	}

	// 2) No persisted run warning may contain the secret value. With the
	//    bug, `to:` resolves to the raw secret, which is not a valid
	//    selector, so the degrade path records `target "<secret>" not
	//    delivered ...` — a plaintext leak into warnings_json.
	rec, gerr := store.Get(context.Background(), "run_leak")
	if gerr != nil {
		t.Fatalf("get run: %v", gerr)
	}
	for _, w := range rec.Warnings() {
		if strings.Contains(w.Message, stripeSecretValue) {
			t.Errorf("run warning leaked the secret value: %q", w.Message)
		}
	}
}

// TestSecretsNamespace_Validates confirms {{ secrets.<type> }} is an
// accepted namespace at save-time template validation (like env / run).
func TestSecretsNamespace_Validates(t *testing.T) {
	dsl := &DSL{
		DSLVersion: SupportedDSLVersion,
		Name:       "pays",
		Steps: []Step{{
			ID:     "charge",
			Type:   StepScript,
			Script: &ScriptStep{Path: "scripts/charge.py", Env: map[string]string{"K": "{{ secrets.stripe }}"}},
		}},
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("secrets namespace should validate, got: %v", err)
	}
}
