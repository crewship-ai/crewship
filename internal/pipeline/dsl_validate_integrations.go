package pipeline

import (
	"fmt"
	"strings"
)

// maxIntegrationsRequired caps how many integration slugs one routine may
// declare. A guardrail against a malformed/abusive definition blowing up
// the run-gate resolution loop and the 422 payload it can produce.
const maxIntegrationsRequired = 64

// maxIntegrationSlugLen bounds a single declared integration slug.
const maxIntegrationSlugLen = 64

// validateIntegrationsRequired is the SHAPE gate for the routine's
// integrations_required list: every entry must be a non-empty slug
// (after trim) within the length cap, and the list within the count cap.
//
// It deliberately does NOT check whether the author crew has the
// integration connected — that's the run-time gate in the API layer
// (internal/api/pipeline_integrations_gate.go). Declaring an integration
// the crew lacks is always allowed at save time; the value of declaring
// it is that the run path can then block with a clear, machine-readable
// error instead of failing deep inside an agent step.
func validateIntegrationsRequired(d *DSL) error {
	if d == nil {
		return nil
	}
	if len(d.IntegrationsRequired) > maxIntegrationsRequired {
		return fmt.Errorf("pipeline: too many integrations_required (%d > %d)", len(d.IntegrationsRequired), maxIntegrationsRequired)
	}
	for _, s := range d.IntegrationsRequired {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("pipeline: integrations_required entries must be non-empty slugs")
		}
		if len(s) > maxIntegrationSlugLen {
			return fmt.Errorf("pipeline: integration slug %q too long (>%d chars)", s, maxIntegrationSlugLen)
		}
	}
	return nil
}

// NormalizedIntegrationsRequired returns the routine's declared
// integration slugs, lowercased + trimmed, with empties dropped and
// duplicates removed. The run-time gate compares this set against the
// crew's connected integrations. nil/empty in → nil out (the gate's
// no-op fast path).
func (d *DSL) NormalizedIntegrationsRequired() []string {
	if d == nil || len(d.IntegrationsRequired) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(d.IntegrationsRequired))
	out := make([]string, 0, len(d.IntegrationsRequired))
	for _, s := range d.IntegrationsRequired {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
