package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// ProgressWriter appends structured JSONL events to a mission's progress file.
// This implements the "external state" pattern (Ralph Loop inspired):
// agents read progress.jsonl to understand what happened in previous iterations.
type ProgressWriter struct {
	mu sync.Mutex
}

// ProgressEvent represents a single event in the mission progress log.
type ProgressEvent struct {
	Timestamp time.Time `json:"ts"`
	Type      string    `json:"type"`
	MissionID string    `json:"mission_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	AgentSlug string    `json:"agent,omitempty"`
	Title     string    `json:"title,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Error     string    `json:"error,omitempty"`
	Tokens    int       `json:"tokens,omitempty"`
	Cost      string    `json:"cost,omitempty"`
	Duration  int64     `json:"duration_ms,omitempty"`
}

// NewProgressWriter creates a ProgressWriter for recording mission task events.
func NewProgressWriter() *ProgressWriter {
	return &ProgressWriter{}
}

// WriteEvent appends a progress event to the JSONL file for a mission.
// Path: /output/{crewSlug}/mission-{traceID}/progress.jsonl
// The base path is the crew's output bind mount inside the container,
// but for persistence we write to the host-mapped path.
func (pw *ProgressWriter) WriteEvent(traceID, crewSlug string, event ProgressEvent) {
	if traceID == "" || crewSlug == "" {
		return
	}
	// V-07: Prevent path traversal via crewSlug/traceID
	if strings.ContainsAny(crewSlug, "/\\") || strings.Contains(crewSlug, "..") {
		return
	}
	if strings.ContainsAny(traceID, "/\\") || strings.Contains(traceID, "..") {
		return
	}

	event.Timestamp = time.Now().UTC()

	pw.mu.Lock()
	defer pw.mu.Unlock()

	dir := filepath.Join("data", "crews", crewSlug, "missions", traceID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return // best-effort, don't fail the mission
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	line := append(data, '\n')

	// Read-modify-write through the durable helper rather than O_APPEND
	// (#1999, same shape as #1807). The old form was
	// os.OpenFile(O_APPEND|O_CREATE) followed by a bare, un-synced
	// f.Write: O_CREATE and the first write are two syscalls, so between
	// them progress.jsonl exists at zero bytes and any concurrent reader
	// observes an EMPTY mission history. That reader is right here in
	// this file — ReadProgress feeds BuildProgressContext, whose output
	// is injected into the agent's system prompt, so a reader landing in
	// that window doesn't get a stale log, it gets "nothing has happened
	// in this mission yet" and the agent redoes completed work.
	//
	// The cost objection that kept appends in place elsewhere does not
	// apply: progress.jsonl holds one line per mission task event
	// (tens, not thousands), the whole write is already serialised by
	// pw.mu, and WriteEvent was re-opening the file per event anyway.
	// WriteFileDurable gives the reader all-or-nothing content plus the
	// fsync the bare f.Write never had.
	path := filepath.Join(dir, "progress.jsonl")
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return // best-effort, don't fail the mission
	}
	// Best-effort by contract (WriteEvent returns nothing), but the
	// durable helper leaves the previous content untouched on failure
	// rather than half-appending.
	_ = memory.WriteFileDurable(path, append(prev, line...), 0644)
}

// ReadProgress reads the progress JSONL file for a mission.
// Returns all events in chronological order.
func (pw *ProgressWriter) ReadProgress(traceID, crewSlug string) ([]ProgressEvent, error) {
	// V-07: Prevent path traversal
	if traceID == "" || crewSlug == "" {
		return nil, nil
	}
	if strings.ContainsAny(crewSlug, "/\\") || strings.Contains(crewSlug, "..") {
		return nil, fmt.Errorf("invalid crew slug")
	}
	if strings.ContainsAny(traceID, "/\\") || strings.Contains(traceID, "..") {
		return nil, fmt.Errorf("invalid trace ID")
	}
	path := filepath.Join("data", "crews", crewSlug, "missions", traceID, "progress.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read progress: %w", err)
	}

	var events []ProgressEvent
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var ev ProgressEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, nil
}

// BuildProgressContext formats progress events into a text block
// suitable for injection into an agent's system prompt.
func (pw *ProgressWriter) BuildProgressContext(traceID, crewSlug string) string {
	events, err := pw.ReadProgress(traceID, crewSlug)
	if err != nil || len(events) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[MISSION PROGRESS - previous events in this mission]\n")
	for _, ev := range events {
		ts := ev.Timestamp.Format("15:04:05")
		switch ev.Type {
		case "task_completed", "task_COMPLETED":
			fmt.Fprintf(&b, "[%s] Task '%s' by @%s: COMPLETED — %s\n", ts, ev.Title, ev.AgentSlug, ev.Summary)
		case "task_FAILED", "task_failed":
			fmt.Fprintf(&b, "[%s] Task '%s' by @%s: FAILED — %s\n", ts, ev.Title, ev.AgentSlug, ev.Error)
		case "task_started":
			fmt.Fprintf(&b, "[%s] Task '%s' assigned to @%s\n", ts, ev.Title, ev.AgentSlug)
		case "mission_started":
			fmt.Fprintf(&b, "[%s] Mission started\n", ts)
		case "mission_REVIEW", "mission_review":
			fmt.Fprintf(&b, "[%s] All tasks completed — mission in review\n", ts)
		default:
			fmt.Fprintf(&b, "[%s] %s\n", ts, ev.Type)
		}
	}
	b.WriteString("[END MISSION PROGRESS]\n")
	return b.String()
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
