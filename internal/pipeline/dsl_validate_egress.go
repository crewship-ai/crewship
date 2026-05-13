package pipeline

import (
	"fmt"
	"strings"
)

// validateStepEgress runs the step-body shape checks for the
// network-and-action-touching step kinds: http, code, wait, transform.
// It also catches unsupported step types — the default branch lives
// here so the original error ("unsupported type ...") surfaces in the
// same position as the pre-split Validate() switch.
//
// Slug-typed step bodies (agent_run, call_pipeline) are handled in
// validateStepSlugs. The HTTP credential_ref check is in
// validateStepCredentials so authoring errors on the credential
// binding stay grouped with the credential-resolution code.
func validateStepEgress(st Step) error {
	switch st.Type {
	case StepAgentRun, StepCallPipeline:
		// Bodies validated in validateStepSlugs.
		return nil
	case StepHTTP:
		if st.HTTP == nil {
			return fmt.Errorf("pipeline: step %q (http) missing http body", st.ID)
		}
		if st.HTTP.Method == "" {
			return fmt.Errorf("pipeline: step %q (http) missing method", st.ID)
		}
		switch strings.ToUpper(st.HTTP.Method) {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
			// ok
		default:
			return fmt.Errorf("pipeline: step %q (http) method %q invalid (allowed: GET POST PUT PATCH DELETE)", st.ID, st.HTTP.Method)
		}
		if st.HTTP.URL == "" {
			return fmt.Errorf("pipeline: step %q (http) missing url", st.ID)
		}
		if st.HTTP.MaxResponseBytes < 0 {
			return fmt.Errorf("pipeline: step %q (http) max_response_bytes cannot be negative", st.ID)
		}
		if st.HTTP.MaxResponseBytes > 50_000_000 {
			return fmt.Errorf("pipeline: step %q (http) max_response_bytes too high (>50MB) — use code step for large payloads", st.ID)
		}
	case StepCode:
		if st.Code == nil {
			return fmt.Errorf("pipeline: step %q (code) missing code body", st.ID)
		}
		switch st.Code.Runtime {
		case "python", "go", "bash":
			// ok
		default:
			return fmt.Errorf("pipeline: step %q (code) runtime %q invalid (allowed: python go bash)", st.ID, st.Code.Runtime)
		}
		if st.Code.Code == "" {
			return fmt.Errorf("pipeline: step %q (code) missing code", st.ID)
		}
		if len(st.Code.Code) > 1_000_000 {
			return fmt.Errorf("pipeline: step %q (code) script >1MB — externalize via skills/files instead", st.ID)
		}
	case StepWait:
		if st.Wait == nil {
			return fmt.Errorf("pipeline: step %q (wait) missing wait body", st.ID)
		}
		switch st.Wait.Kind {
		case "approval":
			if st.Wait.ApprovalPrompt == "" {
				return fmt.Errorf("pipeline: step %q (wait approval) missing approval_prompt", st.ID)
			}
		case "datetime":
			if st.Wait.Until == "" {
				return fmt.Errorf("pipeline: step %q (wait datetime) missing until", st.ID)
			}
		case "event":
			if st.Wait.EventType == "" {
				return fmt.Errorf("pipeline: step %q (wait event) missing event_type", st.ID)
			}
		default:
			return fmt.Errorf("pipeline: step %q (wait) kind %q invalid (allowed: approval datetime event)", st.ID, st.Wait.Kind)
		}
	case StepTransform:
		if st.Transform == nil {
			return fmt.Errorf("pipeline: step %q (transform) missing transform body", st.ID)
		}
		if st.Transform.Input == "" {
			return fmt.Errorf("pipeline: step %q (transform) missing input", st.ID)
		}
		if st.Transform.Expression == "" {
			return fmt.Errorf("pipeline: step %q (transform) missing expression", st.ID)
		}
	default:
		return fmt.Errorf("pipeline: step %q has unsupported type %q (allowed: agent_run, call_pipeline, http, code, wait, transform)", st.ID, st.Type)
	}
	return nil
}
