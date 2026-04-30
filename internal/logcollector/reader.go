package logcollector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
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

// validatePathSegment guards path inputs that get joined into the on-disk
// log layout. Beyond the obvious "/", "\", and ".." traversal blocks, it
// rejects null bytes (which truncate paths in some FS layers and break
// downstream tooling parsing the path), control / whitespace characters
// (which produce surprising filenames), and oversize inputs.
func validatePathSegment(s string) error {
	if s == "" {
		return fmt.Errorf("invalid path segment: empty")
	}
	if len(s) > 256 {
		return fmt.Errorf("invalid path segment: too long (%d bytes)", len(s))
	}
	// Reject invalid UTF-8 explicitly. Without this, byte sequences like
	// 0xFF 0xFE decode to the replacement rune U+FFFD, which is both
	// printable and non-space — so the per-rune scan below would let them
	// through despite the sequence being a malformed encoding.
	if !utf8.ValidString(s) {
		return fmt.Errorf("invalid path segment: invalid UTF-8")
	}
	if strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return fmt.Errorf("invalid path segment: %q", s)
	}
	for i, r := range s {
		// IsPrint covers ASCII control + DEL + most Unicode controls;
		// IsSpace catches whitespace not classified as control. Together
		// they leave only the safe printable + non-space character set.
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid path segment %q: disallowed character at %d (U+%04X)", s, i, r)
		}
	}
	return nil
}

// ReadSessionMessages reads all log entries for a specific session from a JSONL file.
// sessionID is validated as a single path segment (no separators, no ".."),
// matching ReadAgentLogs, to prevent path traversal out of basePath.
func (r *Reader) ReadSessionMessages(basePath, sessionID string) ([]LogEntry, error) {
	if err := validatePathSegment(sessionID); err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}
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
