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
	"github.com/crewship-ai/crewship/internal/memory"
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

	// Separate query for priority-marked entries across ALL types (not
	// just candidateTypes). An operator who pins an exec.command or
	// mission.comment still wants that pin snapshotted to pins.md and,
	// if priority=permanent, wants the threshold bypassed — but the
	// candidateTypes filter would drop those entries silently.
	pq := journal.Query{
		WorkspaceID: cfg.WorkspaceID,
		CrewID:      cfg.CrewID,
		Priorities:  []journal.Priority{journal.PriorityPin, journal.PriorityPermanent},
		Since:       now.Add(-cfg.Since),
		Limit:       500,
	}
	prioEntries, _, err := journal.List(ctx, c.DB, pq)
	if err != nil {
		logger.Warn("consolidate: priority scan failed", "err", err)
		prioEntries = nil
	}

	// Priority=permanent entries bypass the below-threshold skip. An
	// operator has explicitly said "remember this forever", so we
	// extract rules from the filtered set regardless of count — waiting
	// 6 hours for 10 entries before recording an explicit permanent
	// marker defeats the markers' purpose. Also extract pins into
	// pins.md so they surface prominently outside the rule stream.
	hasPermanent := false
	for _, e := range prioEntries {
		if e.Priority == journal.PriorityPermanent {
			hasPermanent = true
			break
		}
	}
	pinsWrote, pinsErr := snapshotPins(cfg, prioEntries)
	if pinsErr != nil && logger != nil {
		logger.Warn("consolidate: pins snapshot failed", "err", pinsErr)
	} else if pinsErr == nil && pinsWrote {
		// Only record a canonical-version row when snapshotPins
		// actually appended new content. The earlier check
		// (`len(prioEntries) > 0`) recorded a row whenever there
		// were any pinned entries in the window — including reruns
		// where every candidate ID was already in pins.md and
		// nothing was written. That produced a phantom version row
		// claiming "pins.md changed" when on-disk content was
		// byte-identical to the previous version.
		pinsPath := filepath.Join(cfg.OutputDir, "pins.md")
		c.recordCanonicalVersion(ctx, cfg, pinsPath, memory.TierPins, canonicalAuditPath(cfg.CrewID, "pins.md"))
	}

	if len(filtered) < cfg.MinEntries && !hasPermanent {
		logger.Debug("consolidate: skipping, below threshold",
			"crew_id", cfg.CrewID, "have", len(filtered), "want", cfg.MinEntries)
		return ConsolidationResult{Skipped: true, EntriesScanned: len(filtered)}, nil
	}

	// Heuristic Curator path: when no LLM Summarizer is wired (operator
	// hasn't set KEEPER_OLLAMA_URL + KEEPER_MODEL), we still want the
	// pin snapshot above to run, version rows to land, and the journal
	// to record an entry for this tick — but we can't extract new
	// learned rules without an LLM. Pre-fix the tick would nil-panic on
	// c.Summarizer.Summarize the moment a crew tripped the
	// MinEntries threshold; the Curator subsystem went dark globally
	// even though the pins snapshot was already producing useful state
	// on its own. (Issue #543.)
	if c.Summarizer == nil {
		logger.Debug("consolidate: no Summarizer; running pin-snapshot path only (set KEEPER_OLLAMA_URL + KEEPER_MODEL for full Curator)",
			"crew_id", cfg.CrewID)
		return ConsolidationResult{
			Skipped:        false,
			EntriesScanned: len(filtered),
			RulesAppended:  0,
		}, nil
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
	// Drop any rule whose normalised pattern already appeared in a
	// learned-*.md within the last 7 days. The LLM is liable to
	// re-propose the same pattern across consecutive 6h ticks while
	// the underlying entries are still in the window; the dedup
	// guards the on-disk corpus against re-appending those.
	rules = dedupAgainstPrior(rules, cfg.OutputDir, now, 7*24*time.Hour)
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

	// HITL path: stage the rules in .proposed/ and emit the
	// EntryMemoryConsolidationProposed marker instead of touching
	// the canonical learned-*.md. The operator approves via the
	// API; on approve the proposal body is merged into the canonical
	// file and the regular EntryMemoryConsolidated emit fires.
	if cfg.ProposalMode {
		return c.writeProposal(ctx, cfg, now, rules, len(filtered))
	}

	outPath, outContent, err := c.appendRules(cfg.OutputDir, now, rules)
	if err != nil {
		return ConsolidationResult{EntriesScanned: len(filtered)},
			fmt.Errorf("consolidate: write rules: %w", err)
	}
	// Pass the in-memory content captured during the appendRules
	// flock window. recordCanonicalVersion would otherwise re-read
	// from disk and risk capturing a sibling tick's changes against
	// THIS run's parent_sha.
	c.recordCanonicalVersionContent(ctx, cfg, outContent, memory.TierLearned, canonicalAuditPath(cfg.CrewID, filepath.Base(outPath)))

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

// snapshotPins walks the filtered entries for priority=pin and appends
// them to {outputDir}/pins.md — one entry per file, human-curated by
// the operator, surfaced prominently next to the auto-generated rule
// stream. Unlike learned-*.md the pins file accumulates forever: the
// point of a pin is "I want every future session to remember this".
// Duplicate IDs are skipped so rerunning consolidation on the same
// entries doesn't grow the file unboundedly.
//
// The wrote return reports whether pins.md was actually modified on
// this call. Callers (e.g. the canonical-version recorder) use this
// to skip an audit-version row when no write happened — the prior
// logic that recorded a version "if there were any pinned entries in
// the window" would falsely audit duplicate pin IDs as new versions.
func snapshotPins(cfg Config, entries []journal.Entry) (wrote bool, err error) {
	if cfg.OutputDir == "" {
		return false, nil
	}
	var pins []journal.Entry
	for _, e := range entries {
		if e.Priority == journal.PriorityPin {
			pins = append(pins, e)
		}
	}
	if len(pins) == 0 {
		return false, nil
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return false, fmt.Errorf("pins mkdir: %w", err)
	}
	path := filepath.Join(cfg.OutputDir, "pins.md")

	// Lock for the full read-then-append window so two consolidator
	// runs cannot both see the same `existing` set and double-append
	// the same pin IDs. Pre-fix pins.md occasionally accumulated dup
	// lines under concurrent ticks (the comment-marker dedup is
	// best-effort; flock is the durable fix).
	lk := memory.NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return false, fmt.Errorf("pins lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()

	// Pre-read existing pins to skip IDs already captured. pins.md is
	// an append-only human-curated reference — rewriting or deduping
	// the entire file would surprise operators who annotated entries
	// by hand.
	existing := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if i := strings.Index(line, "pin-id:"); i >= 0 {
				// Strip the HTML-comment close marker that we wrote when
				// the pin was appended (`<!-- pin-id:j_123 -->`). Without
				// this, the recorded ID ends up as "j_123 -->" and every
				// rerun re-appends the same pin because the lookup misses.
				rest := line[i+len("pin-id:"):]
				if j := strings.Index(rest, "-->"); j >= 0 {
					rest = rest[:j]
				}
				id := strings.TrimSpace(rest)
				if id != "" {
					existing[id] = true
				}
			}
		}
	}

	var block strings.Builder
	if _, err := os.Stat(path); err != nil {
		block.WriteString("# Pinned entries\n\n")
		block.WriteString("Entries explicitly pinned by operators (`priority=pin`). The consolidator\n")
		block.WriteString("appends them here so they stay visible outside the rule stream.\n\n")
	}
	appended := 0
	for _, e := range pins {
		if existing[e.ID] {
			continue
		}
		fmt.Fprintf(&block, "- **%s** (%s, %s) — %s\n", e.ID, e.Type,
			e.TS.UTC().Format("2006-01-02"), e.Summary)
		fmt.Fprintf(&block, "  <!-- pin-id:%s -->\n", e.ID)
		appended++
	}
	if appended == 0 {
		// Every candidate pin was already in the file — no write
		// happened, so caller must NOT record a canonical-version
		// row.
		return false, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("pins open: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(block.String()); err != nil {
		return false, fmt.Errorf("pins write: %w", err)
	}
	return true, nil
}

// appendRules writes the rendered rules to
// {outputDir}/learned-YYYY-MM-DD.md, creating the directory if missing.
// If the file already exists, the new block is appended after a divider
// so multiple runs on the same day accumulate rather than overwrite.
//
// Returns the path AND the full canonical content as it landed on disk
// — captured inside the same flock window as the write. The caller
// passes that captured content into recordCanonicalVersion instead of
// re-reading from disk, so the audit-trail blob always reflects the
// exact mutation that just happened. The previous re-read pattern was
// vulnerable to a sibling writer racing in between unlock and read,
// which would record a blob containing the sibling's changes against
// THIS run's parent_sha — a subtle audit-drift bug.
func (c *Consolidator) appendRules(outputDir string, now time.Time, rules []LearnedRule) (string, []byte, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir %s: %w", outputDir, err)
	}
	fname := fmt.Sprintf("learned-%s.md", now.Format("2006-01-02"))
	path := filepath.Join(outputDir, fname)

	// Serialise concurrent appends. Two consolidator goroutines (one
	// per crew, sharing an output dir if the operator misconfigures
	// them) could otherwise interleave block fragments on the same
	// file. flock is per-fd, blocking — the second caller waits.
	lk := memory.NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return "", nil, fmt.Errorf("lock %s: %w", path, err)
	}
	defer func() { _ = lk.Unlock() }()

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
		return "", nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(block.String()); err != nil {
		return "", nil, fmt.Errorf("write %s: %w", path, err)
	}
	// Read back the full file content WHILE STILL HOLDING THE LOCK so
	// the bytes the caller hands to recordCanonicalVersion match the
	// exact post-append state. Reading outside the lock would race
	// with the next consolidator tick's append.
	if err := f.Sync(); err != nil {
		// fsync failure surfaces as a wrapped error rather than
		// silent half-write; the lockfile + defer-unlock still run.
		return "", nil, fmt.Errorf("sync %s: %w", path, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("read back %s: %w", path, err)
	}
	return path, content, nil
}

// recordCanonicalVersion reads the canonical file at canonicalPath and
// inserts a memory_versions row + content-addressed blob for it. Used
// after appendRules and snapshotPins so every successful canonical
// write contributes to the EU AI Act Art. 14 audit trail. Best-effort:
// failures log a warning but never abort the consolidation pipeline —
// the canonical file is on disk and operators see it whether or not
// the audit row landed. Versioning is gated by (c.DB != nil &&
// cfg.BlobRoot != ""); either zero-value silently disables versioning
// without affecting the rest of the flow.
func (c *Consolidator) recordCanonicalVersion(ctx context.Context, cfg Config, canonicalPath string, tier memory.Tier, auditPath string) {
	if c.DB == nil || cfg.BlobRoot == "" {
		return
	}
	content, err := os.ReadFile(canonicalPath)
	if err != nil {
		c.logger().Warn("version record: read canonical failed",
			"err", err, "path", canonicalPath)
		return
	}
	c.recordCanonicalVersionContent(ctx, cfg, content, tier, auditPath)
}

// recordCanonicalVersionContent is the read-disk-skipping sibling of
// recordCanonicalVersion. Callers that already hold the canonical
// file's lock should pass the post-mutation bytes directly — re-
// reading from disk would let a sibling tick race in between unlock
// and read, recording someone else's content under THIS tick's
// parent_sha. Same gating + same best-effort semantics as the
// path-based variant.
func (c *Consolidator) recordCanonicalVersionContent(ctx context.Context, cfg Config, content []byte, tier memory.Tier, auditPath string) {
	if c.DB == nil || cfg.BlobRoot == "" {
		return
	}
	parent, _ := memory.LatestVersionSha(ctx, c.DB, cfg.WorkspaceID, auditPath)
	if _, err := memory.RecordVersion(ctx, c.DB, memory.VersionRecord{
		WorkspaceID: cfg.WorkspaceID,
		Path:        auditPath,
		Tier:        tier,
		Content:     content,
		WrittenBy:   "consolidator",
		ParentSha:   parent,
		BlobRoot:    cfg.BlobRoot,
	}); err != nil {
		c.logger().Warn("version record: insert failed",
			"err", err, "audit_path", auditPath, "workspace_id", cfg.WorkspaceID)
	}
}

// logger returns the Consolidator's logger with a slog.Default fallback
// so the recordCanonicalVersion helper doesn't have to guard every
// call site. The existing Run() path also uses slog.Default when
// c.Logger is nil; this keeps the pattern consistent.
func (c *Consolidator) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// canonicalAuditPath produces the (workspace_id, path) pair the audit
// log keys on. crew:{crewID}/{filename} disambiguates per-crew files
// at the same name (e.g. both crew A and crew B have a
// learned-2026-05-17.md). Keeping the prefix human-readable so the
// `crewship memory log <path>` CLI can show a useful listing without
// joining through the crews table.
func canonicalAuditPath(crewID, filename string) string {
	if crewID == "" {
		return filename
	}
	return "crew:" + crewID + "/" + filename
}
