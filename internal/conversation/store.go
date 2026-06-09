package conversation

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Role identifies the sender of a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message represents a single message in a chat conversation, including
// role, content, and optional tool call metadata.
type Message struct {
	ID          string    `json:"id"`
	ChatID      string    `json:"session_id"`
	AgentID     string    `json:"agent_id,omitempty"`
	Role        Role      `json:"role"`
	Content     string    `json:"content"`
	ToolName    string    `json:"tool_name,omitempty"`
	ToolSummary string    `json:"tool_summary,omitempty"`
	Metadata    any       `json:"metadata,omitempty"`
	Timestamp   time.Time `json:"ts"`
}

// SearchHit is a single conversation_messages row returned by Search,
// ranked by FTS5 BM25 (best match first). It carries the source session
// and timestamp so a caller can follow up by reading the full session
// JSONL via Read.
type SearchHit struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id"`
	Role        Role      `json:"role"`
	Content     string    `json:"content"`
	ToolSummary string    `json:"tool_summary,omitempty"`
	Timestamp   time.Time `json:"ts"`
}

// Store persists chat conversation messages as JSONL files, one file per
// session. When a *sql.DB is wired (Option WithDB), Append also dual-writes
// a row into conversation_messages so Search can do cross-session keyword
// recall. The JSONL files remain the durable source of truth; the DB row is
// a searchable mirror that can be rebuilt from JSONL if ever needed.
type Store struct {
	basePath string
	logger   *slog.Logger
	mu       sync.Mutex
	files    map[string]*os.File
	db       *sql.DB
}

// Option configures a Store at construction time. Variadic so the common
// JSONL-only callers (tests, code paths without a DB) keep the
// NewStore(basePath, logger) signature unchanged.
type Option func(*Store)

// WithDB enables the searchable conversation_messages mirror. When set,
// Append dual-writes each message to the DB and Search becomes usable.
// When unset (nil db), the Store is JSONL-only and Search returns an
// error explaining the mirror is not configured.
func WithDB(db *sql.DB) Option {
	return func(s *Store) { s.db = db }
}

// NewStore creates a conversation Store that writes session files under
// basePath. Pass WithDB to also enable the searchable DB mirror.
func NewStore(basePath string, logger *slog.Logger, opts ...Option) *Store {
	s := &Store{
		basePath: basePath,
		logger:   logger,
		files:    make(map[string]*os.File),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Append writes a message to the session's JSONL file, creating it if needed.
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

	// Dual-write the searchable mirror. The JSONL above is the durable
	// source of truth — if the mirror write fails we log and continue
	// rather than failing the whole Append, so a transient DB hiccup
	// never loses a chat turn. A row missing from the mirror only means
	// that one turn is not keyword-searchable; the JSONL still has it.
	if s.db != nil {
		if err := s.appendMirror(ctx, sessionID, msg); err != nil {
			s.logger.Warn("conversation search mirror write failed",
				"error", err, "session_id", sessionID, "message_id", msg.ID)
		}
	}
	return nil
}

// appendMirror inserts the searchable row. Triggers (migration v111) keep
// conversation_messages_fts in sync, so this is a single INSERT.
func (s *Store) appendMirror(ctx context.Context, sessionID string, msg Message) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversation_messages (id, session_id, agent_id, role, content, tool_summary, ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, sessionID, msg.AgentID, string(msg.Role), msg.Content, msg.ToolSummary,
		msg.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
	)
	if err != nil {
		return fmt.Errorf("insert mirror row: %w", err)
	}
	return nil
}

// fts5Phrase wraps the user's free-text search in FTS5 phrase quotes so
// operators like NEAR / OR / * inside the input don't change the query's
// meaning. Internal double quotes are doubled to escape them per the FTS5
// grammar. Returns empty when the input is whitespace. Copied from the
// journal package's helper of the same name (internal/journal/queries.go)
// so the two search surfaces neutralise operators identically.
func fts5Phrase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	escaped := strings.ReplaceAll(s, `"`, `""`)
	return `"` + escaped + `"`
}

// Search runs a BM25 keyword search over an agent's conversation messages
// and returns up to limit hits, best match first. The agent_id filter is
// ALWAYS applied so an agent can never see another agent's history — it is
// the isolation boundary, not an optional narrowing. The query is wrapped
// via fts5Phrase so FTS5 operators in user input are treated as literal
// text. A whitespace-only or empty query, or an unconfigured mirror, is an
// error rather than an unbounded scan.
func (s *Store) Search(ctx context.Context, agentID, query string, limit int) ([]SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.db == nil {
		return nil, fmt.Errorf("conversation search mirror not configured")
	}
	if strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	phrase := fts5Phrase(query)
	if phrase == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// JOIN the external-content FTS shadow on rowid and rank by bm25().
	// agent_id lives only on the base table, so the bare reference stays
	// unambiguous. ORDER BY bm25(fts) ASC puts the best (lowest) score
	// first.
	rows, err := s.db.QueryContext(ctx, `
		SELECT cm.id, cm.session_id, cm.agent_id, cm.role, cm.content, cm.tool_summary, cm.ts
		FROM conversation_messages cm
		JOIN conversation_messages_fts fts ON fts.rowid = cm.rowid
		WHERE cm.agent_id = ? AND conversation_messages_fts MATCH ?
		ORDER BY bm25(conversation_messages_fts) ASC, cm.ts DESC
		LIMIT ?`,
		agentID, phrase, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	out := make([]SearchHit, 0, limit)
	for rows.Next() {
		var (
			h     SearchHit
			role  string
			tsStr string
		)
		if err := rows.Scan(&h.ID, &h.SessionID, &h.AgentID, &role, &h.Content, &h.ToolSummary, &tsStr); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		h.Role = Role(role)
		if t, perr := time.Parse("2006-01-02T15:04:05.000Z", tsStr); perr == nil {
			h.Timestamp = t
		} else if t, perr := time.Parse(time.RFC3339Nano, tsStr); perr == nil {
			h.Timestamp = t
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Read returns messages from a session's JSONL file with optional pagination.
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

// Close closes all open session file handles.
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
