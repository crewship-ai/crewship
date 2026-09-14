package server

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/fileserver"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// fileEmitTimeout caps the per-event journal write so a slow DB write
// doesn't block the fsnotify goroutine — file events arrive in bursts
// (e.g. an agent untarring a node_modules directory) and back-pressure
// here would compound into a stalled watcher.
const fileEmitTimeout = 2 * time.Second

// emitFileWrittenEntry persists a file.written journal entry for one
// fsnotify event. Called from the file-watcher handler in server.go on
// every Create / Modify / Delete inside a crew output directory.
//
// Failures are logged at debug — the WS broadcast already covered the
// live UI; the journal entry is the auditable replay layer and a single
// missed row is not worth surfacing to the operator.
func emitFileWrittenEntry(j *journal.Writer, crewID string, ev fileserver.FileEvent, logger *slog.Logger) {
	if j == nil || crewID == "" {
		return
	}

	// Map fsnotify operation strings to a stable verb the UI can group on.
	// Delete is included so Crow's Nest can show a strikethrough; the
	// existing FilesystemPanel only renders writes today but the data is
	// captured so a future tab doesn't need a back-fill.
	var verb string
	switch ev.Event {
	case "file_created":
		verb = "created"
	case "file_modified":
		verb = "wrote"
	case "file_deleted":
		verb = "deleted"
	default:
		// Unknown op (rare; toFileEvent returns nil for ones we don't
		// translate). Skip silently rather than emit a malformed row.
		return
	}

	// crewID maps to the workspace via the crews table; the watcher
	// callback only knows the crew. Resolve later via the journal API
	// (handlers join on crew_id) — the entry stays scoped to the crew
	// without needing a workspace round-trip on the hot path.
	//
	// agent slug is parsed by the watcher from the relative path's first
	// segment (extractAgentSlug) so mission-driven runs that write to
	// /output/<agent>/ get attribution without a DB lookup.
	//
	// E0: the SECOND segment is the run id, because a run's working directory
	// is now /output/<agent>/<runID> (orchestrator.agentRunOutputDir). Parsing
	// it here rather than in the watcher is deliberate — internal/fileserver
	// knows nothing about runs, and the attribution question is the journal's.
	// Without it two concurrent runs of one agent produce an indistinguishable
	// stream of file.written rows, which is the artifact half of "two run
	// streams must never merge on agent slug alone".
	runID := runIDFromEventPath(ev.Path)
	summary := summarizeFileEvent(verb, ev.Path, ev.Size)

	ctx, cancel := context.WithTimeout(context.Background(), fileEmitTimeout)
	defer cancel()

	entry := journal.Entry{
		CrewID:    crewID,
		Type:      journal.EntryFileWritten,
		Severity:  journal.SeverityInfo,
		ActorType: journal.ActorAgent,
		ActorID:   ev.Agent, // slug; the timeline UI shortens to first 8
		Summary:   summary,
		Payload: map[string]any{
			"path":  ev.Path,
			"agent": ev.Agent,
			"size":  ev.Size,
			"op":    ev.Event,
		},
		Refs: map[string]any{"crew_id": crewID},
	}
	// TraceID is the journal's run_id column, so an artifact written by a run
	// joins that run's timeline. Left empty — not faked — for a write this
	// cannot attribute: a file dropped straight into /output/<agent>/, a
	// pre-E0 path, or anything the server itself wrote (chat attachments land
	// under /output/<agent>/attachments/..., whose second segment is
	// "attachments" and is correctly rejected as a run id).
	if runID != "" {
		entry.TraceID = runID
		entry.Payload["run_id"] = runID
	}

	// WorkspaceID is required by Validate but we don't have it here. The
	// crew-scoped query Crow's Nest uses (`crew_id=` filter) doesn't need
	// it for retrieval, but the writer enforces it so we resolve once via
	// the journal package's helper.
	wsID, err := journal.LookupWorkspaceForCrew(ctx, j.DB(), crewID)
	if err != nil || wsID == "" {
		// Crew not found / DB hiccup. Drop the entry rather than surfacing
		// a hot-path error — the WS broadcast already reached the live UI
		// and the next event will retry.
		if logger != nil {
			logger.Debug("file.written emit: workspace lookup failed", "crew_id", crewID, "err", err)
		}
		return
	}
	entry.WorkspaceID = wsID

	if _, err := j.Emit(ctx, entry); err != nil {
		if logger != nil {
			logger.Debug("file.written emit failed", "crew_id", crewID, "path", ev.Path, "err", err)
		}
	}
}

// summarizeFileEvent builds a one-line summary like
// "filip wrote /reports/q4.csv (12.4 KB)". Kept stable — the journal
// timeline + Crow's Nest activity feed both render this verbatim.
func summarizeFileEvent(verb, path string, size int64) string {
	// Path may include the agent slug as the first segment; the activity
	// feed is per-crew so leaving it in keeps attribution visible without
	// a separate column.
	return verb + " " + trimPath(path) + " " + formatSizeBytes(size)
}

func trimPath(p string) string {
	const max = 80
	if len(p) <= max {
		return p
	}
	// Keep the tail (filename) — the prefix is usually agent dirs the
	// reader can infer from context.
	return "…" + p[len(p)-max+1:]
}

func formatSizeBytes(n int64) string {
	if n <= 0 {
		return ""
	}
	switch {
	case n < 1024:
		return "(" + itoa(n) + " B)"
	case n < 1024*1024:
		return "(" + ftoa(float64(n)/1024) + " KB)"
	default:
		return "(" + ftoa(float64(n)/(1024*1024)) + " MB)"
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(f float64) string {
	// One-decimal formatter — matches the dashboard's CPU/RAM rounding.
	whole := int64(f)
	frac := int64((f - float64(whole)) * 10)
	if frac < 0 {
		frac = -frac
	}
	if frac == 0 {
		// Drop the trailing ".0" entirely so 1024 → "1 KB" not "1. KB"
		// (the previous TrimRight stopped at "0" and left a dangling ".").
		return itoa(whole)
	}
	return itoa(whole) + "." + itoa(frac)
}

// runIDFromEventPath pulls the run id out of a watcher path of the shape
// <agentSlug>/runs/<runID>/... — the layout an agent run's working directory
// now has (/output/<agent>/runs/<runID>, orchestrator.agentRunOutputDir).
//
// The literal "runs" segment is what makes this exact rather than a guess.
// Matching <agentSlug>/<runID>/... instead would ask "does this segment look
// like a run id?", and orchestrator.ValidRunID answers yes for "attachments" —
// so every chat attachment (/output/<agent>/attachments/<chatID>/<id>/<file>,
// internal/api/proxy_attachments.go) would have been filed under a run called
// "attachments". ValidRunID is a SAFETY check for shell interpolation, not a
// recogniser, and using it as one is how that bug would have got in.
//
// Returns "" for anything else — a file written straight into the agent's
// shared tree, a pre-E0 path, a bare filename at the crew root — and the
// caller then records no run rather than a wrong one.
//
// It deliberately does NOT check that the run exists: this runs on the
// fsnotify hot path, where a DB lookup per file event is exactly the
// back-pressure fileEmitTimeout exists to avoid. A syntactically valid id that
// names no run costs one unjoined journal row; a lookup would cost every
// burst-write an extra query.
func runIDFromEventPath(relPath string) string {
	cleaned := strings.TrimPrefix(path.Clean(strings.ReplaceAll(relPath, "\\", "/")), "./")
	parts := strings.Split(cleaned, "/")
	// Need at least <agent>/runs/<runID>/<something>.
	if len(parts) < 4 || parts[1] != orchestrator.ContainerRunOutputSegment {
		return ""
	}
	if !orchestrator.ValidRunID(parts[2]) {
		return ""
	}
	return parts[2]
}
