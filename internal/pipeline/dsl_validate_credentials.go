package pipeline

import "fmt"

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
