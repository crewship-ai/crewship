package api

import (
	"encoding/json"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// routineSearchTerms splits a q= value into lowercase words; every word has
// to match somewhere in the routine (ODCH-RT-03).
func routineSearchTerms(q string) []string {
	return strings.Fields(strings.ToLower(q))
}

// routineSearchText is the lowercase text a routine is searched by: its name,
// slug and description and every string value inside the steps of its
// definition (ids, names, agents, prompts, commands, URLs). Keys are not
// searched, so "type" does not match every routine.
func routineSearchText(p *pipeline.Pipeline) string {
	var b strings.Builder
	for _, s := range []string{p.Name, p.Slug, p.Description} {
		b.WriteString(strings.ToLower(s))
		b.WriteByte('\n')
	}
	var def struct {
		Steps []any `json:"steps"`
	}
	if json.Unmarshal([]byte(p.DefinitionJSON), &def) == nil {
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case string:
				b.WriteString(strings.ToLower(x))
				b.WriteByte('\n')
			case []any:
				for _, e := range x {
					walk(e)
				}
			case map[string]any:
				for _, e := range x {
					walk(e)
				}
			}
		}
		walk(def.Steps)
	}
	return b.String()
}

func routineMatchesTerms(p *pipeline.Pipeline, terms []string) bool {
	text := routineSearchText(p)
	for _, t := range terms {
		if !strings.Contains(text, t) {
			return false
		}
	}
	return true
}
