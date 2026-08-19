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

// rejectTypeWildcard refuses a glob in a journal entry-type filter.
//
// --type and --exclude-type are open sets (116 entry types and growing), so
// they are passed through unvalidated on purpose — an allowlist here would
// reject a legitimate filter every time the server shipped a type the CLI had
// not heard of. That openness has one sharp edge: the filter compiles to
// `entry_type IN (...)` on the server (internal/journal/queries.go), with no
// LIKE, prefix or glob path anywhere, so a value containing `*` or `?` matches
// nothing at all and the command exits 0 with an empty page.
//
// Nothing about that output says the question was unanswerable, which makes it
// the most dangerous possible result from an audit surface: docs/cli/routine.mdx
// points operators at the `approval.*` family to find out who disarmed a
// routine gate, and a reader who types that literally would conclude nobody
// ever had. Refusing costs one error message; the alternative costs a wrong
// answer that looks exactly like a right one.
//
// The suggestion is built from the prefix the caller actually typed rather than
// from a canned example, so it is the command they meant, ready to edit.
func rejectTypeWildcard(label, raw string) error {
	if raw == "" || !strings.ContainsAny(raw, "*?") {
		return nil
	}
	hint := "list the types explicitly, comma-separated"
	// `approval.*` → suggest the shape with the prefix preserved.
	if prefix, _, ok := strings.Cut(raw, "*"); ok && strings.HasSuffix(prefix, ".") {
		hint = fmt.Sprintf("list them explicitly, e.g. --%s %sgranted,%sdenied", label, prefix, prefix)
	}
	return fmt.Errorf(
		"invalid --%s value %q: wildcards are not supported — entry types are matched exactly, "+
			"so this filter would match nothing and return an empty result that looks like "+
			"'no such events happened'.\n%s\n"+
			"`crewship journal --help` and the entry-type catalogue in docs/cli/journal.mdx list the names",
		label, raw, hint)
}
