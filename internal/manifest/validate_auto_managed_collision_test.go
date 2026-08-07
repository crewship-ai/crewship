package manifest

// #1712 item 2: a manifest that declares a credential the auto-managed path
// will generate for one of its own services is rejected at validate time.
//
// The rule already existed for auto_credentials the operator writes out
// (checkAutoCredentials), but the common case is the one nobody writes: a
// `postgres:*` service gets a POSTGRES_PASSWORD auto-credential from the
// sidecar catalog without any auto_credentials block at all. A manifest that
// also declares POSTGRES_PASSWORD in credentials[] therefore passed
// `--dry-run` and then failed PART-WAY THROUGH apply, after the crew, its
// agents and the manual credential row had already been created:
//
//	+ credential POSTGRES_PASSWORD (auto-managed for python-backend/postgres):
//	  auto-managed POSTGRES_PASSWORD: credential already exists with
//	  provider=NONE; delete it manually or rename the auto-credential to
//	  resolve the conflict
//
// The repo's own examples/manifests/python-with-services.crew.yaml shipped in
// exactly that state, which is how it was found.

import (
	"strings"
	"testing"
)

func TestValidate_SugarAutoCredentialCollidesWithDeclaredCredential(t *testing.T) {
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
		name           string
		body           string
		wantErr        bool
		errMustContain string
	}{
		{
			// The example's shape: catalog image, no auto_credentials block,
			// and a hand-declared credential of the name the catalog mints.
			name: "catalog sugar name declared in credentials[]",
			body: `
  credentials:
    - { env: POSTGRES_PASSWORD, provider: NONE, type: GENERIC_SECRET }
  services:
    - { name: postgres, image: postgres:16-alpine }`,
			wantErr:        true,
			errMustContain: "POSTGRES_PASSWORD",
		},
		{
			// Redis mints its secret through the command channel rather than
			// an env var, so a collision there has to be caught the same way.
			name: "command-injected sugar name declared in credentials[]",
			body: `
  credentials:
    - { env: REDIS_PASSWORD, provider: NONE, type: GENERIC_SECRET }
  services:
    - { name: redis, image: redis:7-alpine }`,
			wantErr:        true,
			errMustContain: "REDIS_PASSWORD",
		},
		{
			// The false positive this must not produce. An operator who pins
			// the auth env on the service owns that channel: nothing is
			// generated, so their credentials[] entry collides with nothing.
			// The check asks the expander what it WOULD create rather than
			// re-deriving the catalog's answer, which is why this passes.
			name: "operator owns the auth channel — no generation, no clash",
			body: `
  credentials:
    - { env: POSTGRES_PASSWORD, provider: NONE, type: GENERIC_SECRET }
  services:
    - name: postgres
      image: postgres:16-alpine
      env: { POSTGRES_PASSWORD: operator-owned }`,
			wantErr: false,
		},
		{
			// The everyday case: let the platform mint it, reference nothing
			// by hand. This is what the fixed example does.
			name: "no declaration, auto-managed handles it",
			body: `
  services:
    - { name: postgres, image: postgres:16-alpine }`,
			wantErr: false,
		},
		{
			// A crew with no services cannot generate anything, and must not
			// be slowed down or second-guessed by this rule.
			name: "unrelated credential on a serviceless crew",
			body: `
  credentials:
    - { env: ANTHROPIC_API_KEY, provider: ANTHROPIC, type: API_KEY }`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := loadBundleOrFail(t, []byte(head+tc.body+tail))
			err := b.Validate()
			switch {
			case tc.wantErr && err == nil:
				t.Fatal("expected a validation error; without it the apply gets as far as creating " +
					"the crew and its agents before the credential step fails (#1712)")
			case !tc.wantErr && err != nil:
				t.Fatalf("unexpected validation error: %v", err)
			case tc.wantErr && tc.errMustContain != "" && !strings.Contains(err.Error(), tc.errMustContain):
				t.Errorf("error message should name %q, got: %v", tc.errMustContain, err)
			}
		})
	}
}

// The generated secret must never travel back into the manifest the caller
// handed us. Validate probes the expander to learn what it would create, and
// the expander's whole job is writing values into service env / command — on a
// copy, or `crewship apply --dry-run` would print a live password and the plan
// would ship an operator-visible literal.
func TestValidate_DoesNotMutateTheCallerSpec(t *testing.T) {
	b := loadBundleOrFail(t, []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - { name: postgres, image: postgres:16-alpine }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`))
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	spec := b.Documents[0].Spec
	if v, ok := spec.Services[0].Env["POSTGRES_PASSWORD"]; ok {
		t.Errorf("validation wrote a generated secret into the caller's service env (POSTGRES_PASSWORD=%q)", v)
	}
	if len(spec.Agents[0].EnvRefs) != 0 {
		t.Errorf("validation rewrote the caller's agent env_refs: %v", spec.Agents[0].EnvRefs)
	}
}
