package pipeline

import (
	"sort"
	"strings"
)

// ReferencedCredentialTypes reports statically declared vault types in a
// routine. Dynamic lookups performed by an agent or a script are unknowable
// from the definition and are deliberately excluded.
func ReferencedCredentialTypes(d *DSL) []string {
	if d == nil {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, value := range RequiredCredentialTypes(d) {
		add(value)
	}
	var visit func(*Step)
	visit = func(step *Step) {
		if step == nil {
			return
		}
		if step.HTTP != nil && step.HTTP.CredentialRef != nil {
			add(step.HTTP.CredentialRef.Type)
		}
		for value := range secretTypesInStep(*step) {
			add(value)
		}
		if step.Hooks != nil {
			visit(step.Hooks.Before)
			visit(step.Hooks.After)
		}
		if step.Foreach != nil {
			for i := range step.Foreach.Steps {
				visit(&step.Foreach.Steps[i])
			}
		}
	}
	for i := range d.Steps {
		visit(&d.Steps[i])
	}
	if d.Hooks != nil {
		visit(d.Hooks.BeforeAll)
		visit(d.Hooks.AfterAll)
		visit(d.Hooks.OnFailure)
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
