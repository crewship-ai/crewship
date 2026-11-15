package pipeline

import (
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
	if err := checkCredentialRefShape(cr.Type); err != nil {
		return fmt.Errorf("pipeline: step %q (http) credential_ref.type: %w", st.ID, err)
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
		t := NormalizeCredentialRef(cr.Type)
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

// Run-time enforcement of credentials_required lives in the API layer's
// gateMissingCredentials (internal/api/pipeline_credentials_gate.go), which
// probes the workspace vault via NewVaultCredentialProbe on every dispatch
// path that resolves secrets. That is the single production enforcement
// path — this file only supplies the parse-time shape gate
// (validateStepCredentials) and the normalized required-type set
// (RequiredCredentialTypes) it consumes.

// checkCredentialRefShape accepts a credential type (no dot) or a by-name
// reference "name.<NAME>". Any other dotted value used to be looked up as a
// type that cannot exist, so the step silently ran without its credential.
func checkCredentialRefShape(ref string) error {
	ref = strings.TrimSpace(ref)
	if !strings.Contains(ref, ".") {
		return nil
	}
	if _, ok := CredentialRefName(ref); ok {
		return nil
	}
	return fmt.Errorf("%q is neither a credential type nor name.<CREDENTIAL_NAME>", ref)
}
