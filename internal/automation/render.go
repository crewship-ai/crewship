package automation

import (
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// RenderInputs substitutes {{ event.* }} references in an automation's
// configured inputs against the entry that triggered it.
//
// It calls pipeline.Render — the SAME renderer routine steps use — rather
// than parsing placeholders here. That is a deliberate constraint, not an
// implementation detail: a second templating language would drift from the
// first the day one of them learned a new escape rule, and an author would
// have to know which side of a routine boundary they were writing on.
//
// Substitution reaches into nested maps and slices so an input like
// {"issue": {"id": "{{ event.mission_id }}"}} works; only string LEAVES are
// rendered. An unresolvable reference renders empty, exactly as it does in a
// routine step.
func RenderInputs(inputs map[string]any, e journal.Entry) map[string]any {
	if len(inputs) == 0 {
		return map[string]any{}
	}
	ctx := pipeline.RenderContext{Event: EventContext(e)}
	out := make(map[string]any, len(inputs))
	for k, v := range inputs {
		out[k] = renderValue(v, ctx)
	}
	return out
}

func renderValue(v any, ctx pipeline.RenderContext) any {
	switch t := v.(type) {
	case string:
		return pipeline.Render(t, ctx)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = renderValue(vv, ctx)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = renderValue(vv, ctx)
		}
		return out
	default:
		return v
	}
}

// renderInputsJSON is the write-path form: render, then encode once so the
// flusher hands pending_runs a ready string. A marshal failure falls back to
// "{}" rather than dropping the run — an automation that fires with empty
// inputs is a visible problem the run itself will report; one that silently
// never fires is not.
func renderInputsJSON(inputs map[string]any, e journal.Entry) string {
	b, err := json.Marshal(RenderInputs(inputs, e))
	if err != nil {
		return "{}"
	}
	return string(b)
}
