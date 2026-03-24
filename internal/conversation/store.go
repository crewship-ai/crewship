package conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

type Message struct {
	ID        string    `json:"id"`
	ChatID string    `json:"session_id"`
	Role      Role      `json:"role"`
	Content   string    `json:"content"`
	ToolName    string    `json:"tool_name,omitempty"`
	ToolSummary string    `json:"tool_summary,omitempty"`
	Metadata    any       `json:"metadata,omitempty"`
	Timestamp   time.Time `json:"ts"`
}

type Store struct {
	basePath string
	logger   *slog.Logger
	mu       sync.Mutex
	files    map[string]*os.File
}

func NewStore(basePath string, logger *slog.Logger) *Store {
	return &Store{
		basePath: basePath,
		logger:   logger,
		files:    make(map[string]*os.File),
	}
}

func (s *Store) Append(ctx context.Context, sessionID string, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := validateID(sessionID); err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	msg.ChatID = sessionID

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[sessionID]
	if !ok {
		dir := filepath.Join(s.basePath, "conversations")
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("create conversation dir: %w", err)
		}
		path := filepath.Join(dir, sessionID+".jsonl")
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
		if err != nil {
			return fmt.Errorf("open conversation file: %w", err)
		}
		s.files[sessionID] = f
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

func (s *Store) Read(ctx context.Context, sessionID string, offset, limit int) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := validateID(sessionID); err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}

	path := filepath.Join(s.basePath, "conversations", sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var messages []Message
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

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		messages = append(messages, msg)

		if limit > 0 && len(messages) >= limit {
			break
		}
	}

	return messages, scanner.Err()
}

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, f := range s.files {
		_ = f.Close()
		delete(s.files, key)
	}
}

func validateID(s string) error {
	if s == "" || strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return fmt.Errorf("invalid ID: %q", s)
	}
	return nil
}
