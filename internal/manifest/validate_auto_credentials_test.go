package manifest

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// loadBundleOrFail is a tiny test helper — every auto-credential
// validation test starts by `Load`-ing a manifest body and we'd
// rather see "Load failed: ..." than have each test repeat the
// error-handling boilerplate.
func loadBundleOrFail(t *testing.T, body []byte) *Bundle {
	t.Helper()
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

// TestValidate_AutoCredentialShape is the table-driven exhaustion of
// the validator's auto_credentials clauses. Each row pins one rule:
// the YAML is minimal, the expected outcome is a (wantErr,
// errMustContain) pair. New shape rules go here, not as separate
// top-level Tests — keeps the validator's contract surface in one
// place per the project's table-driven test convention.
func TestValidate_AutoCredentialShape(t *testing.T) {
	const head = `
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
`
	const tail = `
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`

	cases := []struct {
		name            string
		body            string
		wantErr         bool
		errMustContain  string
		errMustNotMatch string // optional sanity hook for happy-path
	}{
		{
			name: "bad name lowercase",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: "lower-case-bad" }`,
			wantErr:        true,
			errMustContain: "lower-case-bad",
		},
		{
			name: "length below floor",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 8 }`,
			wantErr: true,
		},
		{
			// A manifest is attacker-controlled input: the byte count
			// reaches make([]byte, n) in generateAutoCredentialValue,
			// so an unbounded value is a remote allocation primitive
			// (1<<30 here is a 1 GiB buffer per auto_credential).
			name: "length above ceiling",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 1073741824 }`,
			wantErr:        true,
			errMustContain: "above the 512-byte maximum",
		},
		{
			name: "length at ceiling allowed",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 512 }`,
			wantErr: false,
		},
		{
			name: "length zero allowed as default",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 0 }`,
			wantErr: false,
		},
		{
			name: "bad inject_as_env digit-leading",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, inject_as_env: "9starts-with-digit" }`,
			wantErr: true,
		},
		{
			name: "duplicate within same service",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD }
        - { name: POSTGRES_PASSWORD, inject_as_env: PG_PWD }`,
			wantErr: true,
		},
		{
			name: "clashes with credentials[] block",
			body: `
  credentials:
    - { env: POSTGRES_PASSWORD, provider: NONE, type: GENERIC_SECRET }
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD }`,
			wantErr:        true,
			errMustContain: "credentials[] declaration",
		},
		{
			name: "happy path with overrides",
			body: `
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: PG_REPLICATION_PASSWORD, inject_as_env: PG_REPL, length: 24, description: replication }`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := loadBundleOrFail(t, []byte(head+tc.body+tail))
			err := b.Validate()
			switch {
			case tc.wantErr && err == nil:
				t.Fatal("expected validation error, got nil")
			case !tc.wantErr && err != nil:
				t.Fatalf("unexpected validation error: %v", err)
			case tc.wantErr && tc.errMustContain != "" && !strings.Contains(err.Error(), tc.errMustContain):
				t.Errorf("error message should contain %q, got: %v", tc.errMustContain, err)
			}
		})
	}
}

// TestValidate_OverCeilingLengthDoesNotAllocate pins the property the
// ceiling check alone does NOT give us.
//
// Rejecting the row is not the same as never allocating for it. The
// validator accumulates failures and never returns early, and
// checkAutoManagedCollisions runs expandAutoCredentialsInCrewSpec as a
// probe *after* checkAutoCredentials has recorded the ceiling error —
// so the over-ceiling length reaches generateAutoCredentialValue on
// the ordinary validate path, error already in hand. Only the clamp
// inside the generator stops the make([]byte, n).
//
// That path is remotely reachable and needs no apply: the sidecar's
// validate_manifest MCP tool (internal/sidecar/routine_mcp.go) feeds
// agent-supplied YAML straight to ValidateBundle. With the clamp
// deleted this test allocates ~335 MB; with it, a few KB.
//
// The credentials[] entry is load-bearing: checkAutoManagedCollisions
// returns early unless the scope declares at least one credential, so
// without it the probe never runs and the test proves nothing.
func TestValidate_OverCeilingLengthDoesNotAllocate(t *testing.T) {
	// 64 MiB of declared bytes — big enough that a regression is
	// unmissable, small enough not to wedge a shared CI box.
	const declared = 1 << 26

	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  credentials:
    - { env: UNRELATED_SECRET, provider: NONE, type: GENERIC_SECRET }
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 67108864 }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := b.Validate()
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("expected the ceiling error, got nil")
	}
	if !strings.Contains(err.Error(), "above the 512-byte maximum") {
		t.Errorf("error should name the ceiling, got: %v", err)
	}

	// A deterministic second opinion on the same clamp, so the test
	// still fails loudly if the allocation budget below ever proves
	// unreliable: the expander is the engine the probe runs, and an
	// over-ceiling length must come back out of it as a 512-byte value.
	if planned, perr := expandAutoCredentialsInCrewSpec(
		&CrewSpec{Services: []Service{{
			Name:            "pg",
			Image:           "postgres:16-alpine",
			AutoCredentials: []AutoCredential{{Name: "POSTGRES_PASSWORD", Length: declared}},
		}}}, ""); perr != nil {
		t.Fatalf("probe expander: %v", perr)
	} else if len(planned) != 1 {
		t.Fatalf("probe expander planned %d creds, want 1", len(planned))
	} else if got := len(planned[0].Value); got != 2*maxAutoCredentialBytes {
		t.Errorf("expander produced a %d-char value, want %d — the generator's clamp is gone",
			got, 2*maxAutoCredentialBytes)
	}

	// TotalAlloc is cumulative and process-wide, but Go runs
	// non-parallel top-level tests one at a time, so the delta is this
	// call's. 4 MiB sits ~1000x above the clean measurement and ~80x
	// below the regressed one; nothing marginal lands in between.
	const budget = 4 << 20
	if delta := after.TotalAlloc - before.TotalAlloc; delta > budget {
		t.Errorf("Validate allocated %d bytes for a rejected length: %d (budget %d) — "+
			"the clamp in generateAutoCredentialValue is gone and the validator "+
			"is an allocation primitive again", delta, declared, budget)
	}
}

// Cross-crew collision detection: an AUTO_MANAGED credential already
// exists in the workspace for a *different* crew. The apply must
// refuse, not silently bind to the wrong row.
func TestPlan_CrossCrewAutoCredentialCollision(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: B, slug: b}
spec:
  services:
    - { name: pg, image: postgres:16-alpine }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	fake := newFakeAPI(t)
	// Pre-seed: an AUTO_MANAGED credential already exists for crew "a".
	other := "a/pg"
	fake.credsByName["POSTGRES_PASSWORD"] = map[string]any{
		"id":                      "cred_pre",
		"name":                    "POSTGRES_PASSWORD",
		"provider":                "AUTO_MANAGED",
		"status":                  "ACTIVE",
		"provisioned_for_service": other,
	}
	client := NewClient(fake)
	_, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true})
	if err == nil {
		t.Fatal("expected error for cross-crew POSTGRES_PASSWORD collision")
	}
	if !strings.Contains(err.Error(), "auto-managed for a/pg") {
		t.Errorf("error should mention the existing tag: %v", err)
	}
}

// Same-crew re-apply: the existing AUTO_MANAGED row has a matching
// provisioned_for_service tag → re-apply is idempotent, no error.
func TestPlan_SameCrewAutoCredentialReapplyIsIdempotent(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: B, slug: b}
spec:
  services:
    - { name: pg, image: postgres:16-alpine }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	fake := newFakeAPI(t)
	same := "b/pg"
	fake.credsByName["POSTGRES_PASSWORD"] = map[string]any{
		"id":                      "cred_pre",
		"name":                    "POSTGRES_PASSWORD",
		"provider":                "AUTO_MANAGED",
		"status":                  "ACTIVE",
		"provisioned_for_service": same,
	}
	client := NewClient(fake)
	if _, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true}); err != nil {
		t.Fatalf("re-apply of same crew should be idempotent, got error: %v", err)
	}
}
