package main

import (
	"fmt"
	"sort"
	"strings"
)

// validPriorities mirrors journal.ValidPriority — duplicated here so the
// CLI can validate user input before round-tripping to the server. The
// API would also reject a bad value, but a clear error message at the
// edge beats a generic 400 surfaced through HTTP.
var validPriorities = map[string]struct{}{
	"normal": {}, "high": {}, "pin": {}, "permanent": {},
}

// validActorTypes mirrors journal.ActorType. Same rationale — fail fast
// in the CLI rather than after a network round-trip.
var validActorTypes = map[string]struct{}{
	"agent": {}, "user": {}, "system": {}, "keeper": {}, "sidecar": {}, "orchestrator": {},
}

// validSeverities mirrors journal.Severity. Cosmetic guard so a typo
// doesn't silently filter to nothing.
var validSeverities = map[string]struct{}{
	"info": {}, "notice": {}, "warn": {}, "error": {},
}

// validateCSV parses a comma-separated list and rejects values that
// aren't in `allowed`. Empty items (`warn,` or `,high`) are also
// rejected — those almost always indicate a typo that the server-
// side parser would silently drop, which is harder to diagnose than a
// fast-fail at the CLI edge.
func validateCSV(label, raw string, allowed map[string]struct{}) error {
	if raw == "" {
		return nil
	}
	for _, s := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return fmt.Errorf("invalid --%s: empty list item (got %q)", label, raw)
		}
		if _, ok := allowed[trimmed]; !ok {
			keys := make([]string, 0, len(allowed))
			for k := range allowed {
				keys = append(keys, k)
			}
			// Map iteration order is randomised, but the error
			// message is user-facing — sort so it's stable across
			// runs (matters for snapshot tests, grep, and operator
			// recall).
			sort.Strings(keys)
			return fmt.Errorf("invalid --%s value %q (allowed: %s)", label, trimmed, strings.Join(keys, "|"))
		}
	}
	return nil
}
