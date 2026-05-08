package pipeline

import (
	"context"
	"fmt"
	"strings"
)

// systemPromptCap limits how many pipelines we list in a single
// [AVAILABLE PIPELINES] block. We sort by invocation_count DESC so
// the most-used pipelines surface even when the workspace has 200+;
// the rest stay listable via the API but aren't dragged into every
// agent's system prompt.
//
// 30 entries × ~150 chars/entry = ~4.5 KB system-prompt overhead per
// run — small enough to not balloon Anthropic prompt-cache key
// invalidation, large enough that a moderately busy workspace shows
// most useful pipelines.
const systemPromptCap = 30

// BuildSystemPromptBlock returns the [AVAILABLE PIPELINES] system-
// prompt block for the named workspace, or "" if no pipelines exist.
// Returning empty when zero pipelines means agents in fresh workspaces
// don't see an empty header — they don't even know pipelines exist
// until the first one lands, which keeps the prompt clean.
//
// Format mirrors [SKILLS AVAILABLE] in agent_config_resolver.go: a
// header line + bracketed body + closing line, with each entry as a
// kebab-cased fact bag the LLM can scan quickly.
//
// authorCrewName is supplied by the caller to render "authored by"
// labels when each pipeline's author crew is in the same workspace.
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
	b.WriteString("[AVAILABLE PIPELINES]\n")
	b.WriteString("You can invoke saved workspace pipelines instead of improvising repetitive work.\n\n")
	b.WriteString("To LIST available pipelines:\n")
	b.WriteString("  GET http://localhost:9119/pipelines\n\n")
	b.WriteString("To INVOKE a pipeline:\n")
	b.WriteString("  POST http://localhost:9119/pipelines/{slug}/run\n")
	b.WriteString("  body: { \"inputs\": {...} }\n\n")
	b.WriteString("To DRY-RUN (preview without side effects):\n")
	b.WriteString("  POST http://localhost:9119/pipelines/{slug}/dry_run\n")
	b.WriteString("  body: { \"inputs\": {...} }\n\n")
	b.WriteString("To SAVE a new pipeline (when you discover a repetitive pattern):\n")
	b.WriteString("  POST http://localhost:9119/pipelines/save\n")
	b.WriteString("  body: { \"name\": \"...\", \"description\": \"...\", \"definition\": {...DSL...}, \"sample_inputs\": {...} }\n")
	b.WriteString("  Note: save runs test_run first; if test fails, fix and retry.\n\n")
	b.WriteString("Currently registered pipelines in this workspace (top by usage):\n\n")

	for _, p := range pipes {
		// Per-entry: slug, description, last status, used by N
		// crews, authored by. Extra fields are deliberately
		// minimal — the LLM mainly needs slug + description to
		// decide if a pipeline is the right fit; everything else
		// is signal-of-trustworthiness.
		fmt.Fprintf(&b, "- slug: %s\n", p.Slug)
		if p.Description != "" {
			fmt.Fprintf(&b, "  description: %s\n", oneLine(p.Description))
		}
		if p.AuthorCrewID != "" {
			authorLabel := p.AuthorCrewID
			if name, ok := crewNameByID[p.AuthorCrewID]; ok && name != "" {
				authorLabel = name
			}
			fmt.Fprintf(&b, "  authored by: %s\n", authorLabel)
		}
		if p.InvocationCount > 0 {
			status := "completed"
			if p.LastInvocationStatus != "" {
				status = strings.ToLower(p.LastInvocationStatus)
			}
			fmt.Fprintf(&b, "  used: %d invocations · last status: %s\n", p.InvocationCount, status)
		} else {
			b.WriteString("  used: not yet invoked\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("[END AVAILABLE PIPELINES]")
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
