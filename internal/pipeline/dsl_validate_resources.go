package pipeline

import "fmt"

// maxRoutineResources caps how many datastores / tools a routine may declare in
// its Resources block. A guardrail against a malformed/abusive definition
// inflating the manifest payload; the manifest is advisory, so the cap is
// generous.
const maxRoutineResources = 32

// validateResources is the SHAPE gate for the declarative Resources block.
// Declaring a resource is ALWAYS allowed (it never gates a run) — this only
// rejects malformed entries: a missing/blank type, a type that isn't a short
// slug, or more than maxRoutineResources of either kind. Lenient by design:
// names and notes are free-form and unchecked.
func validateResources(d *DSL) error {
	if d == nil || d.Resources == nil {
		return nil
	}
	if n := len(d.Resources.Datastores); n > maxRoutineResources {
		return fmt.Errorf("pipeline: too many resources.datastores (%d > %d)", n, maxRoutineResources)
	}
	if n := len(d.Resources.Tools); n > maxRoutineResources {
		return fmt.Errorf("pipeline: too many resources.tools (%d > %d)", n, maxRoutineResources)
	}
	for i, ds := range d.Resources.Datastores {
		if !slugRE.MatchString(ds.Type) {
			return fmt.Errorf("pipeline: resources.datastores[%d] type %q must be a short slug (lowercase a-z 0-9 - _, 1–64 chars)", i, ds.Type)
		}
	}
	for i, tl := range d.Resources.Tools {
		if !slugRE.MatchString(tl.Type) {
			return fmt.Errorf("pipeline: resources.tools[%d] type %q must be a short slug (lowercase a-z 0-9 - _, 1–64 chars)", i, tl.Type)
		}
	}
	return nil
}
