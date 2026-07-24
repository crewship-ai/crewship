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
		if !IsKnownCodeRuntime(st.Code.Runtime) {
			return fmt.Errorf("pipeline: step %q (code) runtime %q invalid (allowed: expr cel python go bash)", st.ID, st.Code.Runtime)
		}
		if st.Code.Code == "" {
			return fmt.Errorf("pipeline: step %q (code) missing code", st.ID)
		}
		if len(st.Code.Code) > 1_000_000 {
			return fmt.Errorf("pipeline: step %q (code) script >1MB — externalize via skills/files instead", st.ID)
		}
		// Reject runtimes with no wired runner at AUTHOR time. python/go/bash
		// are schema-legal but have no sandbox runner in this build, so a step
		// using them would save cleanly then fail at every invocation. Fail
		// fast here instead of at a 03:00 cron run (see code_runtimes.go).
		if !IsWiredCodeRuntime(st.Code.Runtime) {
			return fmt.Errorf("pipeline: step %q (code) runtime %q has no wired runner in this build — "+
				"use runtime: expr or cel for agentless logic, or convert to type: agent_run "+
				"with a shell-tool-enabled agent (see docs/manifest/routine.md `Code steps`)", st.ID, st.Code.Runtime)
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
	case StepNotify:
		if st.Notify == nil {
			return fmt.Errorf("pipeline: step %q (notify) missing notify body", st.ID)
		}
		if st.Notify.To == "" {
			return fmt.Errorf("pipeline: step %q (notify) missing to (workspace, trigger, user:<id>, role:OWNER, role:MANAGER)", st.ID)
		}
		if st.Notify.Title == "" && st.Notify.Body == "" {
			return fmt.Errorf("pipeline: step %q (notify) needs at least a title or body", st.ID)
		}
		if err := validateNotifyTarget(st.Notify.To); err != nil {
			return fmt.Errorf("pipeline: step %q (notify) %w", st.ID, err)
		}
		if st.Notify.Priority != "" && !isValidNotifyPriority(st.Notify.Priority) {
			return fmt.Errorf("pipeline: step %q (notify) priority %q invalid (allowed: urgent high medium low)", st.ID, st.Notify.Priority)
		}
	case StepScript:
		if st.Script == nil {
			return fmt.Errorf("pipeline: step %q (script) missing script body", st.ID)
		}
		abs, err := resolveScriptPath(st.Script.Path)
		if err != nil {
			return fmt.Errorf("pipeline: step %q (script) %w", st.ID, err)
		}
		if _, err := resolveInterpreter(st.Script.Interpreter, abs); err != nil {
			return fmt.Errorf("pipeline: step %q (script) %w", st.ID, err)
		}
	case StepQuery:
		if st.Query == nil {
			return fmt.Errorf("pipeline: step %q (query) missing query body", st.ID)
		}
		if st.Query.Source != "pipeline_runs" {
			return fmt.Errorf("pipeline: step %q (query) source %q invalid (allowed: pipeline_runs)", st.ID, st.Query.Source)
		}
		if st.Query.WindowHours < 0 {
			return fmt.Errorf("pipeline: step %q (query) window_hours cannot be negative", st.ID)
		}
	default:
		return fmt.Errorf("pipeline: step %q has unsupported type %q (allowed: agent_run, call_pipeline, http, code, wait, transform, notify, script, query)", st.ID, st.Type)
	}
	return nil
}

// validateEgressTargets rejects a routine-level egress entry that can never
// match a real host. The runtime allowlist (hostInEgressTargets) is a
// literal + subdomain-suffix match, NOT a glob: `*`, `*.*` and an empty host
// match nothing, so an allowlist that contains one is dead config — it
// silently denies every http step's egress instead of allowing it. Reject it
// at author time so the mistake surfaces before save, not as mystery
// connection failures at run time.
//
// Note this is the OPPOSITE of "allow all": unrestricted egress is expressed
// by OMITTING egress_targets entirely (an empty list means no routine-level
// gate). Loopback hosts (localhost / 127.*) are real, matchable targets and
// stay a doctor WARN, not a Validate error — they're legitimate on dev /
// self-hosted boxes, so save/validate don't block them.
func validateEgressTargets(dsl *DSL) error {
	for _, host := range dsl.EgressTargets {
		switch host {
		case "*", "*.*", "":
			return fmt.Errorf("pipeline: egress_targets entry %q matches no host at run time — targets are literal/subdomain-suffix matched, not globbed, so this dead entry silently denies all egress; list the real hostnames, or omit egress_targets entirely for unrestricted egress", host)
		}
	}
	return nil
}
