package pipeline

import (
	"fmt"
	"strings"
)

// validateConcurrencyKey rejects a concurrency_key that can render to the empty
// string — the only shape that trips the run-time ErrConcurrencyKeyEmpty gate
// ("concurrency_key rendered to empty value"). This is a PARITY check with the
// runtime, not a stricter rule: it flags exactly the keys a run can fail on,
// nothing more.
//
// The key renders empty iff BOTH hold:
//
//   - No literal text survives outside the {{ ... }} templates. A key like
//     "vendor-alert-{{ inputs.vendor_id }}" always renders at least
//     "vendor-alert-", so it can never be empty — accepted regardless of how
//     vendor_id is declared (this is the documented "literal prefix" fix).
//   - Every reference can itself render empty. A reference anchors the key
//     (keeps it non-empty for every caller) when it is an `inputs.X` that is
//     required:true or carries a non-empty default. If even one reference
//     anchors, the whole key is always non-empty.
//
// Only `inputs.X` refs can anchor: the key is rendered when a run is RESERVED,
// before any step has run, so `{{ steps.Y.output }}` / `{{ env.Z }}` refs
// always render empty there and never anchor.
func validateConcurrencyKey(dsl *DSL) error {
	key := dsl.ConcurrencyKey
	if key == "" {
		return nil
	}
	// Any surviving literal text guarantees a non-empty render — nothing to do.
	if templateRE.ReplaceAllString(key, "") != "" {
		return nil
	}

	declared := make(map[string]InputSpec, len(dsl.Inputs))
	for _, in := range dsl.Inputs {
		declared[in.Name] = in
	}

	var refs []string
	anchored := false
	for _, m := range templateRE.FindAllStringSubmatch(key, -1) {
		ref := strings.TrimSpace(m[1])
		refs = append(refs, ref)
		if concurrencyRefAnchors(ref, declared) {
			anchored = true
		}
	}
	if len(refs) == 0 || anchored {
		return nil
	}
	return fmt.Errorf("pipeline: concurrency_key %q can render to an empty value — it is built entirely from references (%s) that may each render empty, so a run omitting them fails at run time with \"concurrency_key rendered to empty value\"; make one referenced input required:true or give it a non-empty default, or add literal text to the key",
		key, strings.Join(refs, ", "))
}

// concurrencyRefAnchors reports whether a concurrency_key template ref is
// guaranteed to render non-empty for every caller, which alone keeps the whole
// key non-empty. Only an `inputs.X` that is required or has a non-empty default
// anchors; steps/env/run refs render empty at reservation time, and an
// undeclared input always renders empty.
func concurrencyRefAnchors(ref string, declared map[string]InputSpec) bool {
	parts := strings.SplitN(ref, ".", 2)
	if len(parts) < 2 || parts[0] != "inputs" {
		return false
	}
	// inputs.X.sub is a JSON path into a structured input. A required/defaulted
	// X guarantees nothing about the SUB-field, which the runtime renders
	// independently (empty if the sub-path is absent) — so a nested ref can
	// never anchor the key. Only a bare, top-level input can.
	name := parts[1]
	if strings.IndexByte(name, '.') >= 0 {
		return false
	}
	spec, ok := declared[name]
	if !ok {
		return false
	}
	return inputAlwaysBound(spec)
}

// inputAlwaysBound reports whether an input is guaranteed to render to a
// non-empty value even when the caller supplies nothing: it's either required
// (the run can't start without it) or carries a non-empty default.
func inputAlwaysBound(in InputSpec) bool {
	if in.Required {
		return true
	}
	return !isEmptyDefault(in.Default)
}

// isEmptyDefault reports whether a declared default would render to the empty
// string. nil (absent) and "" both do; any other scalar (0, false, "x") does
// not.
func isEmptyDefault(def any) bool {
	if def == nil {
		return true
	}
	if s, ok := def.(string); ok {
		return s == ""
	}
	return false
}
