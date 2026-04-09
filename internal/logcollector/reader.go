package logcollector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Reader reads structured log entries from per-agent JSONL files.
type Reader struct {
	basePath string
}

// NewReader creates a Reader that reads agent logs from the given base path.
func NewReader(basePath string) *Reader {
	return &Reader{basePath: basePath}
}

// ReadAgentLogs returns log entries for the given crew and agent, with optional
// pagination via offset and limit (0 means no limit).
func (r *Reader) ReadAgentLogs(crewID, agentID string, offset, limit int) ([]LogEntry, error) {
	if err := validatePathSegment(crewID); err != nil {
		return nil, fmt.Errorf("invalid crew ID: %w", err)
	}
	if err := validatePathSegment(agentID); err != nil {
		return nil, fmt.Errorf("invalid agent ID: %w", err)
	}
	path := filepath.Join(r.basePath, "crews", crewID, "agents", agentID, "current.jsonl")
	return readJSONL(path, offset, limit)
}

func validatePathSegment(s string) error {
	if s == "" || strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return fmt.Errorf("invalid path segment: %q", s)
	}
	return nil
}

// ReadSessionMessages reads all log entries for a specific session from a JSONL file.
func (r *Reader) ReadSessionMessages(basePath, sessionID string) ([]LogEntry, error) {
	path := filepath.Join(basePath, sessionID+".jsonl")
	return readJSONL(path, 0, 0)
}

func readJSONL(path string, offset, limit int) ([]LogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if offset > 0 && lineNum <= offset {
			continue
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)

		if limit > 0 && len(entries) >= limit {
			break
		}
	}

	return entries, scanner.Err()
}
