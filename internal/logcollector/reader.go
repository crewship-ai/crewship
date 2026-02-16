package logcollector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Reader struct {
	basePath string
}

func NewReader(basePath string) *Reader {
	return &Reader{basePath: basePath}
}

func (r *Reader) ReadAgentLogs(teamID, agentID string, offset, limit int) ([]LogEntry, error) {
	path := filepath.Join(r.basePath, "teams", teamID, "agents", agentID, "current.jsonl")
	return readJSONL(path, offset, limit)
}

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
