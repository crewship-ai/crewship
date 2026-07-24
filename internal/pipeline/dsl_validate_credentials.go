package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// validateStepCredentials checks the credential_ref binding on an
// HTTP step. Non-HTTP step types (and HTTP steps without a
// credential_ref) are a no-op.
//
// The runtime egress allowlist + actual credential resolution live
// in runner_http.go; this is the parse-time shape gate that catches
// authoring mistakes (missing type, mismatched inject_as / header /
// query name) before save.
func validateStepCredentials(st Step) error {
	if st.Type != StepHTTP || st.HTTP == nil || st.HTTP.CredentialRef == nil {
		return nil
	}
	cr := st.HTTP.CredentialRef
	if cr.Type == "" {
		return fmt.Errorf("pipeline: step %q (http) credential_ref missing type", st.ID)
	}
	switch cr.InjectAs {
	case "", "bearer", "header", "query":
		// ok
	default:
		return fmt.Errorf("pipeline: step %q (http) credential_ref.inject_as %q invalid (allowed: bearer header query)", st.ID, cr.InjectAs)
	}
	if cr.InjectAs == "header" && cr.HeaderName == "" {
		return fmt.Errorf("pipeline: step %q (http) credential_ref inject_as=header requires header_name", st.ID)
	}
	if cr.InjectAs == "query" && cr.QueryName == "" {
		return fmt.Errorf("pipeline: step %q (http) credential_ref inject_as=query requires query_name", st.ID)
	}
	return nil
}

// maxCredentialsRequired caps how many credential types one routine may
// declare — a guardrail against a malformed definition blowing up the
// enforcement loop + 422 payload, mirroring maxIntegrationsRequired.
const maxCredentialsRequired = 64

// RequiredCredentialTypes returns the routine's declared
// credentials_required types, trimmed + lowercased, de-duplicated, with
// empties dropped. nil/empty in → nil out (the enforcement no-op fast
// path). Comparison against the vault is case-insensitive, so lowering
// here keeps the required set canonical the same way
// NormalizedIntegrationsRequired does for integrations.
func RequiredCredentialTypes(d *DSL) []string {
	if d == nil || len(d.CredsRequired) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(d.CredsRequired))
	out := make([]string, 0, len(d.CredsRequired))
	for _, cr := range d.CredsRequired {
		t := strings.ToLower(strings.TrimSpace(cr.Type))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CredentialProbe reports whether a credential of credType is resolvable
// in the run scope. Availability only — it NEVER returns the secret value,
// so the validator can enforce credentials_required without ever holding a
// decrypted credential. NewVaultCredentialProbe builds the production
// implementation over the workspace vault.
type CredentialProbe func(ctx context.Context, credType string) (bool, error)

// ValidateRequiredCredentials enforces a routine's credentials_required:
// each declared type must be resolvable via probe, else validation fails
// with a message naming the missing type. This closes the "declared-only,
// enforced-by-nobody" gap (#1418) — credentials_required was previously
// documentary.
//
// It is fail-CLOSED, unlike the integrations run-gate's fail-open bias: a
// routine that explicitly declares it needs a credential is asserting the
// run cannot function without it, so a missing (or unverifiable) credential
// must block rather than silently proceed to a step that will fail deep in
// a runner with an opaque auth error. A nil probe (no way to confirm
// resolvability) therefore fails too, rather than rubber-stamping.
//
// Declaring is still always allowed at SAVE time (like integrations) — the
// caller runs this at the enforcement boundary (the API run gate), passing
// a probe scoped to the running workspace + author crew.
func ValidateRequiredCredentials(ctx context.Context, dsl *DSL, probe CredentialProbe) error {
	types := RequiredCredentialTypes(dsl)
	if len(types) == 0 {
		// Reject a declared-but-empty entry so a malformed
		// `credentials_required: [{}]` doesn't pass as "nothing required".
		if dsl != nil {
			for _, cr := range dsl.CredsRequired {
				if strings.TrimSpace(cr.Type) == "" {
					return errors.New("pipeline: credentials_required entry missing type")
				}
			}
		}
		return nil
	}
	if len(types) > maxCredentialsRequired {
		return fmt.Errorf("pipeline: too many credentials_required (%d > %d)", len(types), maxCredentialsRequired)
	}
	if probe == nil {
		return fmt.Errorf("pipeline: cannot verify credentials_required %v — no credential resolver available", types)
	}
	for _, t := range types {
		ok, err := probe(ctx, t)
		if err != nil {
			return fmt.Errorf("pipeline: resolving required credential %q: %w", t, err)
		}
		if !ok {
			return fmt.Errorf("pipeline: required credential %q is not resolvable in this workspace vault — connect a credential of that type", t)
		}
	}
	return nil
}
