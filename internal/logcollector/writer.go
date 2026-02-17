package logcollector

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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

type Writer struct {
	basePath string
	logger   *slog.Logger
	mu       sync.Mutex
	files    map[string]*os.File
}

func NewWriter(basePath string, logger *slog.Logger) *Writer {
	return &Writer{
		basePath: basePath,
		logger:   logger,
		files:    make(map[string]*os.File),
	}
}

func validateID(s string) error {
	if s == "" || strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return fmt.Errorf("invalid ID: %q", s)
	}
	return nil
}

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

	key := crewID + "/" + agentID
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

func (w *Writer) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.files {
		_ = f.Sync()
	}
}

func (w *Writer) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, f := range w.files {
		_ = f.Close()
		delete(w.files, key)
	}
}
