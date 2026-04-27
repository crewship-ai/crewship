// Package consolidate contains the background workers that turn the raw
// append-only journal into two longer-lived derived artefacts:
//
//  1. Memory consolidation — reads recent episodic entries (peer escalations,
//     generated summaries, denied keeper decisions, terminal mission status
//     changes, eval regressions) for a given crew, asks an LLM to extract
//     stable "when X happens, do Y" rules and appends them to a daily
//     learned-YYYY-MM-DD.md file under the crew's shared memory directory.
//     This is the episodic-to-semantic bridge: it upgrades raw events into
//     generalised lessons that future crew members can load on boot.
//
//  2. Journal compaction — rolls up high-volume low-signal entries older
//     than N days (exec output chunks, container metrics, port open/close
//     events, llm.call records) into per-day summary entries, then deletes
//     the originals. High-value entries (escalations, summaries, keeper
//     decisions, mission state, eval.*) are never compacted.
//
// Both workers are DB-only and filesystem-only (for the markdown output).
// Neither owns migrations or schema; they read via journal.List and write
// via journal.Emit, so the journal package stays the sole writer of the
// journal_entries table.
package consolidate

import (
	"context"
	"time"
)

// Config parameterises a single consolidation run. A runner (see
// runner.go) fills this in per-crew each tick.
//
// MinEntries guards against calling the LLM for a crew that barely did
// anything — there's nothing stable to learn from three events. The
// default is 10, which matches the minimum sample size we want before
// taking any generalisation seriously.
//
// OutputDir is the directory the learned-*.md file should be written into.
// Production wiring points this at the crew's shared memory topics dir
// (typically /crew/shared/.memory/topics/). Tests pass t.TempDir() so the
// writes land in a throw-away location.
type Config struct {
	WorkspaceID string
	CrewID      string
	Since       time.Duration // look-back window; default 6h if zero
	MinEntries  int           // skip LLM if fewer than this many candidate entries; default 10
	LLMModel    string        // model identifier passed to the summarizer; informational
	OutputDir   string        // where learned-YYYY-MM-DD.md is written
}

// LearnedRule is one extracted "pattern -> action" lesson. Evidence is the
// list of journal entry IDs that led the LLM to propose the rule — stored
// so an operator reviewing the markdown can click through to the source
// events that justified it.
//
// Confidence is a 0..1 self-reported score from the LLM. We don't trust it
// in any hard gating sense, we just render it so the operator can triage
// the noisier rules first.
type LearnedRule struct {
	Pattern    string   `json:"pattern"`
	Action     string   `json:"action"`
	Evidence   []string `json:"evidence"`
	Confidence float64  `json:"confidence"`
}

// ConsolidationResult reports what a single Run produced. Skipped is true
// when the MinEntries threshold was not met; in that case RulesAppended
// and OutputPath are zero values and no file is written.
type ConsolidationResult struct {
	Skipped        bool
	EntriesScanned int
	RulesAppended  int
	OutputPath     string
	JournalEntryID string // id of the memory.consolidated entry that was emitted
}

// CompactResult reports what a single compaction pass deleted and rolled
// up. BytesFreed is a best-effort SUM(length(payload)+length(summary))
// over deleted rows — it's not exact DB reclaim but is representative of
// the cost of the removed entries.
type CompactResult struct {
	EntriesDeleted  int64
	EntriesArchived int64
	BucketsCreated  int64
	BytesFreed      int64
}

// SummarizerClient is the minimum surface the consolidator needs from an
// LLM backend. Production wiring supplies an Ollama-backed implementation;
// tests supply a stub that returns pre-canned JSON. Keeping the interface
// this small means the consolidate package has no opinion about model
// hosting, streaming, retries, or auth — those concerns belong to the
// concrete client.
type SummarizerClient interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}
