package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/fileserver"
	"github.com/crewship-ai/crewship/internal/journal"
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
	verb := "wrote"
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
	s := itoa(whole) + "." + itoa(frac)
	return strings.TrimRight(s, "0")
}
