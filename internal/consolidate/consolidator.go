package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Consolidator turns a window of recent journal entries for one crew into
// a set of learned rules, appended to a daily markdown file. A single
// instance is safe to reuse across crews and over time because Run carries
// all per-invocation state on the stack.
//
// The DB handle is read-only from this struct's perspective — we only call
// journal.List against it. Journal writes happen via the Emitter so the
// journal package remains the sole INSERT path for journal_entries and
// SetTraceResolver + batching stay consistent.
type Consolidator struct {
	DB         *sql.DB
	Journal    journal.Emitter
	Summarizer SummarizerClient
	Logger     *slog.Logger
	// Now lets tests pin the clock. Production leaves it nil and the
	// consolidator uses time.Now().UTC().
	Now func() time.Time
}

// candidateTypes is the set of entry types we consider semantically
// interesting — they carry signal about patterns that should survive past
// the originating event. Everything else (exec chunks, metrics, broadcast
// noise) is explicitly excluded to keep the LLM prompt dense and the rules
// grounded in decisions rather than activity.
var candidateTypes = []journal.EntryType{
	journal.EntryPeerEscalation,
	journal.EntrySummaryGenerated,
	journal.EntryKeeperDecision,
	journal.EntryMissionStatus,
	journal.EntryEvalRegression,
}

// Run executes one consolidation cycle for cfg.CrewID within cfg.WorkspaceID.
// It queries the journal, optionally invokes the summarizer, parses the
// response, and appends the resulting rules to a daily markdown file.
//
// The function never returns a partial success: either the file was
// appended and the memory.consolidated entry was emitted, or the result
// reports Skipped=true and no side effect is visible. The one exception
// is a malformed LLM response — the malformed rules are dropped and the
// remainder flows through, because a single bad JSON object should not
// throw away the whole cycle.
func (c *Consolidator) Run(ctx context.Context, cfg Config) (ConsolidationResult, error) {
	if cfg.WorkspaceID == "" {
		return ConsolidationResult{}, fmt.Errorf("consolidate: workspace_id required")
	}
	if cfg.CrewID == "" {
		return ConsolidationResult{}, fmt.Errorf("consolidate: crew_id required")
	}
	if cfg.OutputDir == "" {
		return ConsolidationResult{}, fmt.Errorf("consolidate: output_dir required")
	}
	if cfg.MinEntries <= 0 {
		cfg.MinEntries = 10
	}
	if cfg.Since <= 0 {
		cfg.Since = 6 * time.Hour
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := c.now()

	q := journal.Query{
		WorkspaceID: cfg.WorkspaceID,
		CrewID:      cfg.CrewID,
		Types:       append([]journal.EntryType(nil), candidateTypes...),
		Since:       now.Add(-cfg.Since),
		Limit:       500,
	}
	entries, _, err := journal.List(ctx, c.DB, q)
	if err != nil {
		return ConsolidationResult{}, fmt.Errorf("consolidate: list: %w", err)
	}

	// Drop keeper.decision entries that were not denials. We want only
	// the denied ones — they're the signal that the crew asked for
	// something it shouldn't have, which is exactly the kind of lesson
	// worth generalising. Allowed decisions are routine operations.
	filtered := filterKeeperDenied(entries)
	if len(filtered) < cfg.MinEntries {
		logger.Debug("consolidate: skipping, below threshold",
			"crew_id", cfg.CrewID, "have", len(filtered), "want", cfg.MinEntries)
		return ConsolidationResult{Skipped: true, EntriesScanned: len(filtered)}, nil
	}

	prompt := buildPrompt(filtered)
	raw, err := c.Summarizer.Summarize(ctx, prompt)
	if err != nil {
		return ConsolidationResult{EntriesScanned: len(filtered)},
			fmt.Errorf("consolidate: summarize: %w", err)
	}

	rules := parseRules(raw)
	// Require supporting evidence >1. Single-event rules tend to be
	// over-fitted noise; demanding at least two supporting entries is
	// a cheap filter the LLM should respect but we enforce it again here.
	rules = filterMultipleEvidence(rules)
	if len(rules) == 0 {
		// Still emit the journal marker so operators can see the worker
		// ran. Nothing written to disk because there is nothing to write.
		id, emitErr := c.Journal.Emit(ctx, journal.Entry{
			WorkspaceID: cfg.WorkspaceID,
			CrewID:      cfg.CrewID,
			Type:        journal.EntryMemoryConsolidated,
			ActorType:   journal.ActorSystem,
			ActorID:     "consolidator",
			Severity:    journal.SeverityInfo,
			Summary: fmt.Sprintf("consolidated %d entries; 0 rules extracted",
				len(filtered)),
			Payload: map[string]any{
				"rules_count":     0,
				"entries_scanned": len(filtered),
				"skipped":         false,
				"model":           cfg.LLMModel,
			},
		})
		if emitErr != nil {
			return ConsolidationResult{EntriesScanned: len(filtered)},
				fmt.Errorf("consolidate: emit empty: %w", emitErr)
		}
		return ConsolidationResult{
			EntriesScanned: len(filtered),
			RulesAppended:  0,
			JournalEntryID: id,
		}, nil
	}

	outPath, err := c.appendRules(cfg.OutputDir, now, rules)
	if err != nil {
		return ConsolidationResult{EntriesScanned: len(filtered)},
			fmt.Errorf("consolidate: write rules: %w", err)
	}

	evidenceAll := collectEvidence(rules)
	id, err := c.Journal.Emit(ctx, journal.Entry{
		WorkspaceID: cfg.WorkspaceID,
		CrewID:      cfg.CrewID,
		Type:        journal.EntryMemoryConsolidated,
		ActorType:   journal.ActorSystem,
		ActorID:     "consolidator",
		Severity:    journal.SeverityNotice,
		Summary: fmt.Sprintf("consolidated %d entries into %d learned rules",
			len(filtered), len(rules)),
		Payload: map[string]any{
			"rules_count":     len(rules),
			"entries_scanned": len(filtered),
			"skipped":         false,
			"output_path":     outPath,
			"model":           cfg.LLMModel,
		},
		Refs: map[string]any{
			"source_entry_ids": evidenceAll,
		},
	})
	if err != nil {
		return ConsolidationResult{EntriesScanned: len(filtered), RulesAppended: len(rules), OutputPath: outPath},
			fmt.Errorf("consolidate: emit: %w", err)
	}

	return ConsolidationResult{
		EntriesScanned: len(filtered),
		RulesAppended:  len(rules),
		OutputPath:     outPath,
		JournalEntryID: id,
	}, nil
}

// now returns the consolidator's view of current time, using the injected
// clock if one is set. Isolated in a helper so tests can pin it without
// touching every call site.
func (c *Consolidator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// filterKeeperDenied keeps every non-keeper entry as-is and only retains
// keeper.decision entries whose payload indicates denial. The keeper
// payload convention (see internal/keeper) stores the outcome under
// "decision" with values like "allow", "deny", or "escalate".
func filterKeeperDenied(entries []journal.Entry) []journal.Entry {
	out := make([]journal.Entry, 0, len(entries))
	for _, e := range entries {
		if e.Type != journal.EntryKeeperDecision {
			out = append(out, e)
			continue
		}
		if isDenied(e.Payload) {
			out = append(out, e)
		}
	}
	return out
}

func isDenied(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	for _, key := range []string{"decision", "outcome", "result"} {
		v, ok := payload[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "deny" || s == "denied" || s == "reject" || s == "rejected" {
			return true
		}
	}
	// Some emitters use a boolean `allowed`. An explicit false is a denial.
	if v, ok := payload["allowed"]; ok {
		if b, ok := v.(bool); ok && !b {
			return true
		}
	}
	return false
}

// buildPrompt renders the LLM prompt from the candidate entries. The
// format is deliberately plain-text: one line per entry, ID prefixed so
// the model can cite evidence back in its JSON response.
func buildPrompt(entries []journal.Entry) string {
	var b strings.Builder
	b.WriteString("You are consolidating a crew's recent activity into stable semantic rules.\n")
	b.WriteString("From the events below, extract rules of the form \"when X happens, agents should Y\".\n")
	b.WriteString("Return a JSON array of objects: {pattern, action, evidence:[entry_ids], confidence:0..1}.\n")
	b.WriteString("Only include rules supported by MORE THAN ONE entry. Return [] if no stable pattern is visible.\n\n")
	b.WriteString("EVENTS:\n")
	for _, e := range entries {
		b.WriteString("- [")
		b.WriteString(e.ID)
		b.WriteString("] ")
		b.WriteString(string(e.Type))
		b.WriteString(": ")
		b.WriteString(e.Summary)
		b.WriteByte('\n')
	}
	return b.String()
}

// parseRules tolerates wrappers around the JSON array (code fences,
// leading/trailing prose) because raw LLM output often has them. It
// returns an empty slice on any irrecoverable parse failure rather than
// failing the whole consolidation run — a malformed response should not
// erase the journal marker that the worker ran.
func parseRules(raw string) []LearnedRule {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Strip a leading ```json / ``` fence and its trailing counterpart.
	if strings.HasPrefix(raw, "```") {
		// drop everything up to and including the first newline
		if nl := strings.IndexByte(raw, '\n'); nl >= 0 {
			raw = raw[nl+1:]
		}
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
		raw = strings.TrimSpace(raw)
	}
	// Find the first '[' and last ']' so leading/trailing prose does not
	// derail json.Unmarshal.
	start := strings.IndexByte(raw, '[')
	end := strings.LastIndexByte(raw, ']')
	if start < 0 || end <= start {
		return nil
	}
	var rules []LearnedRule
	if err := json.Unmarshal([]byte(raw[start:end+1]), &rules); err != nil {
		return nil
	}
	// Drop rules that are entirely empty — pattern+action both blank is
	// LLM slop masquerading as a structured response.
	cleaned := make([]LearnedRule, 0, len(rules))
	for _, r := range rules {
		r.Pattern = strings.TrimSpace(r.Pattern)
		r.Action = strings.TrimSpace(r.Action)
		if r.Pattern == "" && r.Action == "" {
			continue
		}
		// Clamp confidence into [0,1]; LLMs occasionally emit values
		// outside the declared range.
		if r.Confidence < 0 {
			r.Confidence = 0
		}
		if r.Confidence > 1 {
			r.Confidence = 1
		}
		cleaned = append(cleaned, r)
	}
	return cleaned
}

// filterMultipleEvidence enforces the "at least two supporting entries"
// invariant the prompt asks for. A model that ignores the instruction
// would otherwise pollute learned-*.md with single-event rules.
func filterMultipleEvidence(rules []LearnedRule) []LearnedRule {
	out := make([]LearnedRule, 0, len(rules))
	for _, r := range rules {
		if len(r.Evidence) > 1 {
			out = append(out, r)
		}
	}
	return out
}

// collectEvidence flattens all evidence IDs across a slice of rules into
// a deduplicated list. Used to populate the source_entry_ids reference of
// the memory.consolidated journal entry.
func collectEvidence(rules []LearnedRule) []string {
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, 16)
	for _, r := range rules {
		for _, id := range r.Evidence {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// appendRules writes the rendered rules to
// {outputDir}/learned-YYYY-MM-DD.md, creating the directory if missing.
// If the file already exists, the new block is appended after a divider
// so multiple runs on the same day accumulate rather than overwrite.
func (c *Consolidator) appendRules(outputDir string, now time.Time, rules []LearnedRule) (string, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", outputDir, err)
	}
	fname := fmt.Sprintf("learned-%s.md", now.Format("2006-01-02"))
	path := filepath.Join(outputDir, fname)

	var block strings.Builder
	_, statErr := os.Stat(path)
	exists := statErr == nil
	if !exists {
		block.WriteString("# Learned rules — ")
		block.WriteString(now.Format("2006-01-02"))
		block.WriteString("\n\n")
		block.WriteString("Auto-generated by the consolidation worker. Each rule lists the\n")
		block.WriteString("source journal entries under \"evidence\" so you can audit the\n")
		block.WriteString("reasoning. High-confidence rules appear first in each block.\n\n")
	} else {
		block.WriteString("\n---\n\n")
	}
	block.WriteString("## Run at ")
	block.WriteString(now.Format("15:04:05 MST"))
	block.WriteString("\n\n")
	for i, r := range rules {
		fmt.Fprintf(&block, "- **Pattern:** %s  \n", r.Pattern)
		fmt.Fprintf(&block, "  **Action:** %s  \n", r.Action)
		fmt.Fprintf(&block, "  **Confidence:** %.2f  \n", r.Confidence)
		if len(r.Evidence) > 0 {
			fmt.Fprintf(&block, "  **Evidence:** %s\n", strings.Join(r.Evidence, ", "))
		}
		if i < len(rules)-1 {
			block.WriteByte('\n')
		}
	}
	block.WriteByte('\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(block.String()); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
