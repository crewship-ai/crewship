package devcontainer

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// provisioner.go — WithPlan, featureStepLabel, featureLeafID.
//
// WithProgress / WithPlan are the two functional-options the UI uses
// to render a stepwise checklist. The label helpers exist so the plan
// announcement and the per-step progress message match by string
// equality — if they ever drift, every UI row sits stuck on "pending"
// forever (source comment makes that consequence explicit).
// ---------------------------------------------------------------------------

func TestWithPlan_StoresCallbackOnProvisionOpts(t *testing.T) {
	// Apply WithPlan(fn) and verify the option populates provisionOpts
	// .onPlan with the same fn (not a wrapper that swallows args).
	var capturedSteps []string
	fn := func(steps []string) { capturedSteps = append([]string{}, steps...) }

	var o provisionOpts
	WithPlan(fn)(&o)

	if o.onPlan == nil {
		t.Fatal("WithPlan did not install onPlan")
	}
	// Invoke through the stored field and confirm round-trip.
	o.onPlan([]string{"step-a", "step-b"})
	if len(capturedSteps) != 2 || capturedSteps[0] != "step-a" || capturedSteps[1] != "step-b" {
		t.Errorf("stored callback did not receive args: %+v", capturedSteps)
	}
}

func TestWithPlan_NilFnLandsAsNil(t *testing.T) {
	// nil callback must NOT panic when applied — production paths
	// without a UI subscriber pass no WithPlan at all, but a defensive
	// nil-tolerant store keeps the call site simple.
	var o provisionOpts
	WithPlan(nil)(&o)
	if o.onPlan != nil {
		t.Errorf("nil callback installed something non-nil: %v", o.onPlan)
	}
}

func TestWithPlan_OverridesPriorCallback(t *testing.T) {
	// Repeat applications replace, not chain — pin that so a future
	// "compose multiple subscribers" refactor doesn't silently break
	// the existing single-subscriber contract.
	var called string
	var o provisionOpts
	WithPlan(func(_ []string) { called = "first" })(&o)
	WithPlan(func(_ []string) { called = "second" })(&o)
	o.onPlan(nil)
	if called != "second" {
		t.Errorf("called = %q, want \"second\" (second WithPlan should replace, not chain)", called)
	}
}

func TestWithPlan_AndWithProgressAreIndependent(t *testing.T) {
	// The two callbacks land on different fields. Setting one must
	// not clobber the other — pin against a regression that aliased
	// the slots.
	var o provisionOpts
	var planHit, progressHit bool
	WithPlan(func(_ []string) { planHit = true })(&o)
	WithProgress(func(_, _ int, _ string) { progressHit = true })(&o)

	if o.onPlan == nil || o.onProgress == nil {
		t.Fatalf("both callbacks should be installed; onPlan=%v onProgress=%v", o.onPlan, o.onProgress)
	}
	o.onPlan(nil)
	o.onProgress(0, 0, "")
	if !planHit || !progressHit {
		t.Errorf("planHit=%v progressHit=%v (one slot may have clobbered the other)", planHit, progressHit)
	}
}

// --- featureStepLabel ---

func TestFeatureStepLabel_Format(t *testing.T) {
	// Source: `"Installing " + featureID`. Pin the exact format because
	// the UI matches progress messages against plan entries by string
	// equality — a refactor to title-case or quoted-name would silently
	// strand every checklist row at "pending".
	cases := []struct {
		in, want string
	}{
		{"python", "Installing python"},
		{"common-utils", "Installing common-utils"},
		{"", "Installing "}, // empty featureID still produces a deterministic label
		{"with spaces in id", "Installing with spaces in id"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := featureStepLabel(tc.in); got != tc.want {
				t.Errorf("featureStepLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFeatureStepLabel_MatchesPullStepLabelStyle(t *testing.T) {
	// pullStepLabel uses the same "Installing/Pulling <noun>" pattern.
	// Pin that both end with the noun unquoted so a refactor that
	// switches one to quoted form (e.g. `"Installing \"python\""`)
	// breaks the UI-string-equality contract in step with this test.
	got := featureStepLabel("python")
	if !strings.HasPrefix(got, "Installing ") || strings.Contains(got, `"`) {
		t.Errorf("featureStepLabel = %q; expected unquoted \"Installing <id>\" shape", got)
	}
}

// --- featureLeafID ---

func TestFeatureLeafID_Cases(t *testing.T) {
	// Source comment:
	//   ghcr.io/devcontainers/features/python:1 → "python"
	//   common-utils:2                          → "common-utils"
	// The leaf is what gets displayed in the checklist AND what
	// install.sh-emitting features identify themselves by. A
	// regression that returned the tag (e.g. "python:1") would
	// break checklist matching AND feature-self-identification.
	cases := []struct {
		name, in, want string
	}{
		{"ghcr-with-tag", "ghcr.io/devcontainers/features/python:1", "python"},
		{"name-with-tag", "common-utils:2", "common-utils"},
		{"bare-name-no-tag", "python", "python"},
		{"bare-name-with-tag", "python:latest", "python"},
		{"path-no-tag", "ghcr.io/foo/bar", "bar"},
		{"trailing-slash-empty-leaf", "ghcr.io/foo/", ""}, // trailing slash → empty leaf
		{"empty", "", ""},
		{"just-tag", ":1", ""}, // strip tag → empty
		{"mixed-case-preserved", "ghcr.io/Foo/Bar:tag", "Bar"}, // no lower-casing
		{"deep-path", "registry.example.com/org/group/sub/feature:v2", "feature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := featureLeafID(tc.in); got != tc.want {
				t.Errorf("featureLeafID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFeatureLeafID_IsIdempotentOnAlreadyLeaf(t *testing.T) {
	// Calling featureLeafID on its own output must yield the same
	// string — important because some callers chain through this
	// helper without tracking whether the input is already a leaf.
	for _, in := range []string{"python", "common-utils", "git"} {
		once := featureLeafID(in)
		twice := featureLeafID(once)
		if once != twice {
			t.Errorf("idempotency broken for %q: once=%q twice=%q", in, once, twice)
		}
	}
}
