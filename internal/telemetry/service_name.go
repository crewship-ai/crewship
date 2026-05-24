package telemetry

import "os"

// serviceNameEnv is the environment variable operators set when they
// want the OTel resource attribute service.name to differ from the
// callsite default (e.g. a side-car wanting to register as
// "crewship-sidecar" while the main API binary stays "crewship", or
// EE deployments tagging traces with the customer slug).
const serviceNameEnv = "CREWSHIP_SERVICE_NAME"

// ServiceNameFromEnv resolves the service.name resource attribute the
// caller should pass to Init.
//
// Resolution order (most specific wins):
//  1. CREWSHIP_SERVICE_NAME environment variable, if set and non-empty
//  2. the explicit fallback argument
//  3. the literal string "crewship" (matches Init's internal default,
//     restated here so callers that don't yet check Init's behaviour
//     stay aligned)
//
// Why a helper and not just os.Getenv at the callsite: every binary
// that boots a tracer needs the same three-step decision, and burying
// the env var name in different cmd packages would make rotation /
// rename harder. One helper, one source of truth, exercised by
// TestServiceNameFromEnv.
func ServiceNameFromEnv(fallback string) string {
	if v := os.Getenv(serviceNameEnv); v != "" {
		return v
	}
	if fallback != "" {
		return fallback
	}
	return "crewship"
}
