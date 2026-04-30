package logcollector

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogEntry represents a single structured log event from an agent run,
// including the event type, content, and optional tool/token metadata.
type LogEntry struct {
	Timestamp time.Time `json:"ts"`
	Level     string    `json:"level"`
	Agent     string    `json:"agent"`
	Event     string    `json:"event"`
	Content   string    `json:"content,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Tokens    int       `json:"tokens,omitempty"`
	Metadata  any       `json:"metadata,omitempty"`
}

// fileKey identifies an open per-agent log file without a per-call string
// concatenation: struct equality is zero-alloc when used as a map key.
type fileKey struct {
	crewID  string
	agentID string
}

// Writer appends structured log entries to per-agent JSONL files organized
// under basePath/crews/{crewID}/agents/{agentID}/current.jsonl.
type Writer struct {
	basePath string
	logger   *slog.Logger
	mu       sync.Mutex
	files    map[fileKey]*os.File
}

// NewWriter creates a Writer that stores agent logs under the given base path.
func NewWriter(basePath string, logger *slog.Logger) *Writer {
	return &Writer{
		basePath: basePath,
		logger:   logger,
		files:    make(map[fileKey]*os.File),
	}
}

func validateID(s string) error {
	// Delegates to the shared path-segment validator so writer and reader
	// reject the same set of dangerous inputs (null bytes, control chars,
	// whitespace, traversal segments).
	return validatePathSegment(s)
}

// Append writes a log entry to the JSONL file for the given crew and agent,
// creating the file and directory structure if needed.
func (w *Writer) Append(crewID, agentID string, entry LogEntry) error {
	if err := validateID(crewID); err != nil {
		return fmt.Errorf("invalid crew ID: %w", err)
	}
	if err := validateID(agentID); err != nil {
		return fmt.Errorf("invalid agent ID: %w", err)
	}

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.Level == "" {
		entry.Level = "info"
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	key := fileKey{crewID: crewID, agentID: agentID}
	f, ok := w.files[key]
	if !ok {
		dir := filepath.Join(w.basePath, "crews", crewID, "agents", agentID)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
		path := filepath.Join(dir, "current.jsonl")
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		w.files[key] = f
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write log entry: %w", err)
	}
	return nil
}

// Flush syncs all open log files to disk.
func (w *Writer) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.files {
		_ = f.Sync()
	}
}

// Close closes all open log file handles.
func (w *Writer) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, f := range w.files {
		_ = f.Close()
		delete(w.files, key)
	}
}
