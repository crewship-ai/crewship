package api

// Per-routine slash commands — the half of the catalog that comes from
// the database rather than from the constant above it.
//
// A routine opts in by carrying a `slash` block in its definition
// (pipeline.SlashSpec). This file turns those routines into catalog
// entries: the id both clients dispatch on, the label a human reads,
// and a form schema translated from the routine's declared inputs.
//
// The two vocabularies this file sits between do not line up, and that
// is most of the work:
//
//	pipeline.InputSpec.Type   is a DATA type — string, integer, number,
//	                          boolean, array, object — and its Default
//	                          is `any`.
//	slashFormField.Type       is a WIDGET — text, textarea, number, … —
//	                          and its Default is a string, because that
//	                          is what an <input> holds.
//
// Translating one way is this file's job. Translating BACK — the form's
// strings into the typed `inputs` map the routine runs on — is the
// client's, and both do it from slashFormField.ValueType rather than
// from the widget. That return leg is load-bearing: inputs reach a
// `code` step with their ORIGINAL types, so a routine evaluating
// `inputs.limit > 20` fails outright when 42 arrives as "42", and no
// run-time input validation exists to catch it first. internal/cli/slash_server.go and
// components/features/chat/composer/slash-action-modal.tsx are the two
// implementations; lib/routine-inputs.ts holds the browser's half.

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// slashRoutineIDPrefix marks a catalog entry as "run this routine".
//
// The id is `routine.run:<slug>`; the command a user TYPES is the bare
// slug. Those are deliberately different strings. `/msn-etn-podklady`
// is what a person can be told to type and will remember, while the
// prefix is what lets both clients dispatch — they read it and know to
// POST the run endpoint, instead of inferring an endpoint from a name
// that could be anything a routine author chose.
const slashRoutineIDPrefix = "routine.run:"

// slashRoutineID builds the catalog id for a routine slug.
func slashRoutineID(slug string) string { return slashRoutineIDPrefix + slug }

// maxSlashRoutineCommands caps how many routines the palette will carry.
//
// The palette is a list a human scrolls, and a workspace can hold
// hundreds of routines; past a point an opt-in list stops being a
// shortcut. The cap is high enough that no honest workspace meets it
// and low enough that a misconfigured one doesn't ship a 500-row
// palette to every member. Store.List orders by invocation count, so
// what survives a truncation is what people actually run — and the
// handler logs when it truncates rather than pretending it didn't.
const maxSlashRoutineCommands = 50

// routineSlashDefinition is the narrow read of a routine's definition
// JSON — the two blocks the catalog needs and nothing else.
//
// Deliberately not pipeline.Parse: that runs the full decode for a
// document we only want two fields from, and it is the wrong failure
// mode here. A definition that no longer round-trips cleanly should
// cost that routine its palette row, not the whole catalog, and a
// narrow unmarshal fails on exactly the routines it can't read.
type routineSlashDefinition struct {
	Slash  *pipeline.SlashSpec  `json:"slash"`
	Inputs []pipeline.InputSpec `json:"inputs"`
}

// slashWidgetForInputType maps a routine input's DATA type onto the form
// widget that collects it.
//
// Every case is written out. The renderer does fall back to a text input
// for a type it doesn't know (form-field.tsx rule 1), but that fallback
// is there to survive a dashboard older than its server — leaning on it
// to express a mapping we already know would turn a compatibility
// cushion into the design.
//
// array and object get a textarea: the user types JSON, and the client
// parses it before the run request goes out, so an unparseable value is
// refused with the form still open rather than 400'd from the server.
func slashWidgetForInputType(inputType string) string {
	switch inputType {
	case "string":
		return "text"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array", "object":
		return "textarea"
	default:
		// Empty (undeclared) or a type from a newer DSL than this build.
		// A text box the server can validate beats rendering nothing.
		return "text"
	}
}

// formatInputDefault renders a routine input's `any` default as the
// string a form field can hold.
//
// The round trip has to be lossless, because whatever comes back is
// what the routine runs with:
//
//   - a number renders without acquiring a decimal point it never had
//     (42, not 42.0) — json.Unmarshal hands every JSON number over as a
//     float64, so the naive %v prints "42" for some values and "1e+06"
//     for others;
//   - a bool renders as Go/JSON spells it (true), not as Python does;
//   - an array or object renders as compact JSON, which is what the
//     textarea's contents are parsed back as;
//   - nil renders empty, and the field's `default` is omitempty, so an
//     input with no default arrives with no default rather than with an
//     empty one.
func formatInputDefault(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// 'f' with precision -1 is the shortest representation that
		// round-trips: 42 stays "42", 0.5 stays "0.5", 1e6 stays
		// "1000000" rather than becoming "1e+06".
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		// Arrays, objects, and anything else a definition can hold.
		// Compact JSON — the same encoding the client parses back.
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// slashFormSchemaForInputs translates a routine's declared inputs into
// the form schema both clients render.
//
// Inputs with no name are dropped: a field the client cannot key a value
// under is not a field, and shipping it would draw an unlabelled box
// that silently sends nothing.
func slashFormSchemaForInputs(inputs []pipeline.InputSpec) []slashFormField {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]slashFormField, 0, len(inputs))
	for _, in := range inputs {
		if in.Name == "" {
			continue
		}
		out = append(out, slashFormField{
			Name:      in.Name,
			Type:      slashWidgetForInputType(in.Type),
			Required:  in.Required,
			Default:   formatInputDefault(in.Default),
			ValueType: in.Type,
			// The author's own words about what the value means. Without
			// this the chat form shows a bare box labelled `obdobi` while
			// the routine's detail page — which reads the definition
			// directly — shows the sentence explaining it.
			Help: in.Description,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// slashCommandForRoutine builds the catalog entry for one routine, or
// reports false when the routine has not opted in.
//
// Label precedence is slash.label → the routine's name → its slug. A
// routine that opts in without naming itself still renders as something
// a human can read, and the slug is always there.
func slashCommandForRoutine(p *pipeline.Pipeline) (slashCommand, bool) {
	if p == nil || p.Slug == "" {
		return slashCommand{}, false
	}
	var def routineSlashDefinition
	if err := json.Unmarshal([]byte(p.DefinitionJSON), &def); err != nil {
		return slashCommand{}, false
	}
	if def.Slash == nil || !def.Slash.Enabled {
		return slashCommand{}, false
	}
	label := def.Slash.Label
	if label == "" {
		label = p.Name
	}
	if label == "" {
		label = p.Slug
	}
	return slashCommand{
		ID:         slashRoutineID(p.Slug),
		Label:      label,
		LabelCS:    def.Slash.LabelCS,
		Icon:       def.Slash.Icon,
		Capability: CapabilityRoutineRun,
		FormSchema: slashFormSchemaForInputs(def.Inputs),
	}, true
}

// staticSlashCommandIDs is the set of ids the platform catalog owns.
// Built once at init from slashCommandCatalog so the two cannot drift.
var staticSlashCommandIDs = func() map[string]struct{} {
	out := make(map[string]struct{}, len(slashCommandCatalog))
	for _, sc := range slashCommandCatalog {
		out[sc.ID] = struct{}{}
	}
	return out
}()

// slashRoutineCollidesWithStatic reports whether a routine's slug would
// shadow a platform slash command.
//
// The static catalog wins. `/routine`, `/issue`, `/skill` and
// `/credential` mean one thing across every workspace, and a routine
// that happens to be slugged `issue` must not change what a user's
// muscle memory does — least of all silently, and least of all for
// other people in the same workspace.
//
// The loser is dropped from the palette and nothing about it is said on
// the wire: the collision is the workspace author's to fix by renaming
// the routine, and an API field describing "your routine was shadowed"
// would leak the platform's id list to every caller for no gain. The
// handler logs it for the operator instead.
func slashRoutineCollidesWithStatic(slug string) bool {
	_, taken := staticSlashCommandIDs[slug]
	return taken
}

// routineSlashCommands returns the per-routine entries for a workspace.
//
// One query: Store.List pulls the active routines with their definition
// JSON already on the row, and the opt-in check reads that column. There
// is no per-routine follow-up read, which is the difference between a
// palette open costing one statement and costing one per routine in the
// workspace.
//
// Only runnable routines are listed. A `proposed` routine is waiting
// for a human to approve it and a `disabled` one has been pulled by an
// admin; the run endpoint refuses both, so offering either in a palette
// would only be a button that always fails.
//
// The filter is pipeline.StatusRunnable — the run gate's own predicate,
// called rather than restated as a `status = 'active'` clause in the
// query. Today the two agree exactly (v128 made the column NOT NULL
// with a CHECK over the three states, so 'active' is the only runnable
// one). Sharing the function is what keeps them agreeing when a fourth
// state is added: whoever teaches the gate to run it gets the palette
// for free, instead of shipping a palette that hides a routine the
// endpoint would have accepted.
//
// A store error yields no routine entries and a logged warning — the
// static catalog still returns, because a member losing `/credential`
// because the pipelines table hiccuped is a worse failure than a
// palette that is briefly missing its routines.
func (h *SlashCommandsHandler) routineSlashCommands(ctx context.Context, workspaceID string) []slashCommand {
	if h.pipelines == nil || workspaceID == "" {
		return nil
	}
	rows, err := h.pipelines.List(ctx, pipeline.ListFilters{
		WorkspaceID: workspaceID,
		OrderBy:     pipeline.OrderByPopularity,
	})
	if err != nil {
		if h.r != nil && h.r.logger != nil {
			h.r.logger.Warn("slash catalog: list routines failed",
				"workspace_id", workspaceID, "error", err.Error())
		}
		return nil
	}
	var out []slashCommand
	truncated := 0
	for _, p := range rows {
		if !pipeline.StatusRunnable(p.Status) {
			continue
		}
		cmd, ok := slashCommandForRoutine(p)
		if !ok {
			continue
		}
		if slashRoutineCollidesWithStatic(p.Slug) {
			if h.r != nil && h.r.logger != nil {
				h.r.logger.Warn("slash catalog: routine slug shadows a platform command, dropped from the palette",
					"workspace_id", workspaceID, "slug", p.Slug)
			}
			continue
		}
		if len(out) >= maxSlashRoutineCommands {
			truncated++
			continue
		}
		out = append(out, cmd)
	}
	if truncated > 0 && h.r != nil && h.r.logger != nil {
		h.r.logger.Warn("slash catalog: routine palette truncated",
			"workspace_id", workspaceID, "limit", maxSlashRoutineCommands, "dropped", truncated)
	}
	return out
}
