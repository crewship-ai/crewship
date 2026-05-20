package manifest

import (
	"context"
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
