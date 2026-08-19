package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// systemPromptCap limits how many routines we list in a single
// [AVAILABLE ROUTINES] block. We sort by invocation_count DESC so
// the most-used routines surface even when the workspace has 200+;
// the rest stay listable via the API but aren't dragged into every
// agent's system prompt.
//
// 30 entries × ~150 chars/entry = ~4.5 KB system-prompt overhead per
// run — small enough to not balloon Anthropic prompt-cache key
// invalidation, large enough that a moderately busy workspace shows
// most useful routines.
const systemPromptCap = 30

// routinesPromptCharBudget bounds the TOTAL size of the block. The
// entry cap alone doesn't: 30 entries with 200-char descriptions plus
// author/usage lines is ~12 KB (~3k tokens) injected into EVERY agent
// exec — measured live on dev2, it pushed the minimum cost of a
// trivial Haiku step past the seeded $0.01 cost-cap sentinel. When the
// budget is hit, remaining routines collapse into one "…N more" line;
// the full list stays one GET /pipelines away.
const routinesPromptCharBudget = 6000

// BuildSystemPromptBlock returns the [AVAILABLE ROUTINES] system-
// prompt block for the named workspace, or "" if no routines exist.
// Returning empty when zero routines means agents in fresh workspaces
// don't see an empty header — they don't even know routines exist
// until the first one lands, which keeps the prompt clean.
//
// Naming note: the user-facing term is "Routine" but the underlying
// HTTP paths stay /pipelines/* for backwards compatibility. The agent
// reads "routine" conceptually and uses the /pipelines/ API. Both
// terms refer to the same thing.
//
// Format mirrors [SKILLS AVAILABLE] in agent_config_resolver.go: a
// header line + bracketed body + closing line, with each entry as a
// kebab-cased fact bag the LLM can scan quickly.
//
// authorCrewName is supplied by the caller to render "authored by"
// labels when each routine's author crew is in the same workspace.
// Pass nil to render with raw IDs as a fallback (still functional,
// just less readable).
func BuildSystemPromptBlock(ctx context.Context, store *Store, workspaceID string, crewNameByID map[string]string) (string, error) {
	pipes, err := store.List(ctx, ListFilters{
		WorkspaceID: workspaceID,
		Limit:       systemPromptCap,
		OrderBy:     OrderByPopularity,
	})
	if err != nil {
		return "", fmt.Errorf("pipeline: build system prompt: %w", err)
	}
	if len(pipes) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("[AVAILABLE ROUTINES]\n")
	b.WriteString("Routines are saved, repeatable workspace recipes (declarative AI workflows). Invoke them instead of improvising repetitive work.\n\n")
	b.WriteString("To LIST available routines:\n")
	b.WriteString("  GET http://localhost:9119/pipelines\n\n")
	b.WriteString("To INVOKE a routine:\n")
	b.WriteString("  Call the run_routine tool — args: { slug, inputs }\n")
	b.WriteString("  It runs the saved routine and returns the run result/status. Do NOT curl the run endpoint — use the tool.\n")
	// The rule this block exists to state. Each listed routine now
	// carries its declared inputs, which is what makes asking possible
	// at all: before that an agent could not name the values a routine
	// wanted, so its only options were an empty inputs object or invented
	// key names. Running a routine spends money and touches the crew's
	// integrations, so guessing at what to run it WITH is the expensive
	// kind of wrong.
	b.WriteString("  BEFORE calling it: if the routine lists inputs below and you do not have a value\n")
	b.WriteString("  for one, ASK THE USER for it and wait for their answer. Do not invent values, do\n")
	b.WriteString("  not send an empty inputs object when the routine declares inputs, and do not\n")
	b.WriteString("  substitute a default that is not written below. Ask for every missing input in\n")
	b.WriteString("  ONE message, listing each by name with its meaning, and say which you will use\n")
	b.WriteString("  the stated default for unless told otherwise.\n")
	b.WriteString("  Send each value as its declared type: a number unquoted, a boolean as true/false.\n\n")
	b.WriteString("To DRY-RUN (preview without side effects):\n")
	b.WriteString("  POST http://localhost:9119/pipelines/{slug}/dry_run\n")
	b.WriteString("  body: { \"inputs\": {...} }\n\n")
	b.WriteString("To SAVE a new routine (when you discover a repetitive pattern):\n")
	b.WriteString("  Call the save_routine tool — args: { name, description, definition (the DSL object), sample_inputs }\n")
	b.WriteString("  It validates + saves in one call; on a DSL error it returns the message so you fix and retry.\n")
	b.WriteString("  Do NOT curl the save endpoint — use the tool.\n\n")
	b.WriteString("Currently registered routines in this workspace (top by usage):\n\n")

	shown := 0
	for _, p := range pipes {
		// Per-entry: slug, description, last status, used by N
		// crews, authored by. Extra fields are deliberately
		// minimal — the LLM mainly needs slug + description to
		// decide if a pipeline is the right fit; everything else
		// is signal-of-trustworthiness.
		var entry strings.Builder
		fmt.Fprintf(&entry, "- slug: %s\n", p.Slug)
		if p.Description != "" {
			fmt.Fprintf(&entry, "  description: %s\n", oneLine(p.Description))
		}
		if p.AuthorCrewID != "" {
			authorLabel := p.AuthorCrewID
			if name, ok := crewNameByID[p.AuthorCrewID]; ok && name != "" {
				authorLabel = name
			}
			fmt.Fprintf(&entry, "  authored by: %s\n", authorLabel)
		}
		if p.InvocationCount > 0 {
			status := "completed"
			if p.LastInvocationStatus != "" {
				status = strings.ToLower(p.LastInvocationStatus)
			}
			fmt.Fprintf(&entry, "  used: %d invocations · last status: %s\n", p.InvocationCount, status)
		} else {
			entry.WriteString("  used: not yet invoked\n")
		}
		entry.WriteString(promptInputLines(p.DefinitionJSON))
		entry.WriteString("\n")

		// Char budget: entries are already popularity-ordered, so
		// stopping here keeps the most-used routines and folds the
		// tail into one summary line.
		if b.Len()+entry.Len() > routinesPromptCharBudget {
			break
		}
		b.WriteString(entry.String())
		shown++
	}
	if rest := len(pipes) - shown; rest > 0 {
		fmt.Fprintf(&b, "…%d more routine(s) not shown (prompt budget) — GET http://localhost:9119/pipelines for the full list.\n\n", rest)
	}
	b.WriteString("[END AVAILABLE ROUTINES]")
	return b.String(), nil
}

// oneLine collapses any whitespace run in s to a single space and
// trims, so descriptions written with newlines render as one line in
// the system prompt without breaking the bracketed structure.
//
// Truncation walks back to a UTF-8 rune boundary before slicing so
// multi-byte characters at the cap boundary (CJK, emoji,
// diacritics) don't get corrupted into invalid UTF-8.
func oneLine(s string) string {
	fields := strings.Fields(s)
	out := strings.Join(fields, " ")
	const cap = 200
	if len(out) <= cap {
		return out
	}
	cut := cap
	for cut > 0 && cut > cap-4 && (out[cut]&0xc0) == 0x80 {
		cut--
	}
	return out[:cut] + "…"
}

// promptInputLines renders a routine's declared inputs for its entry in
// the [AVAILABLE ROUTINES] block, or "" when it declares none.
//
// This is what makes "ask the user first" an instruction an agent can
// actually follow. Before it, the block gave a slug, a description and a
// usage count, so an agent asked to run the monthly accounting pack knew
// the routine existed and nothing whatsoever about what it wanted — not
// that `obdobi` is a period in YYYY-MM, not that leaving it empty means
// last month, not that `ucetnictvi_root` has a default worth mentioning.
// Its only moves were an empty inputs object or invented key names, and
// a routine run costs money and touches the crew's integrations.
//
// Rendered shape, one line per input:
//
//	inputs (ask the user for any you do not have):
//	  - obdobi (string) — YYYY-MM; empty means the previous month
//	  - ucetnictvi_root (string, default "Unify - Účetnictví")
//	  - amount (number, REQUIRED)
//
// Kept terse on purpose: this lands in EVERY agent's system prompt for
// every run, and routinesPromptCharBudget already bounds the block. A
// routine with more inputs than promptInputCap says so and stops, rather
// than crowding out the routines below it.
func promptInputLines(definitionJSON string) string {
	var def struct {
		Inputs []InputSpec `json:"inputs"`
	}
	// A definition that no longer decodes costs this routine its input
	// list, not the block. The entry above still names the slug, which is
	// enough for the agent to look it up.
	if err := json.Unmarshal([]byte(definitionJSON), &def); err != nil || len(def.Inputs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("  inputs (ask the user for any you do not have):\n")
	shown := 0
	for _, in := range def.Inputs {
		if in.Name == "" {
			continue
		}
		if shown >= promptInputCap {
			fmt.Fprintf(&b, "    …%d more — GET http://localhost:9119/pipelines for the full spec\n", len(def.Inputs)-shown)
			break
		}
		typ := in.Type
		if typ == "" {
			typ = "string"
		}
		fmt.Fprintf(&b, "    - %s (%s", in.Name, typ)
		if in.Required {
			// Uppercase because it changes what the agent must do before
			// calling the tool, and it is the one word here that does.
			b.WriteString(", REQUIRED")
		} else if s := formatPromptDefault(in.Default); s != "" {
			fmt.Fprintf(&b, ", default %s", s)
		}
		b.WriteString(")")
		if in.Description != "" {
			fmt.Fprintf(&b, " — %s", oneLine(in.Description))
		}
		b.WriteString("\n")
		shown++
	}
	if shown == 0 {
		return ""
	}
	return b.String()
}

// promptInputCap bounds how many inputs one routine contributes. A
// routine with more than this many is unusual, and a single pathological
// one must not eat the budget that the other routines in the workspace
// need to be visible at all.
const promptInputCap = 12

// formatPromptDefault renders a declared default for the prompt line.
//
// Numbers keep their shape (42, not 42.0 — JSON hands every number over
// as a float64) so the agent repeats back to the user the value the
// routine will actually use. Strings are quoted so an empty or
// space-bearing default is visible as one; a default that renders empty
// contributes nothing, since "default " tells the agent less than
// silence does.
func formatPromptDefault(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if t == "" {
			return ""
		}
		return strconv.Quote(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
