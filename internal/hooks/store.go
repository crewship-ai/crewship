package hooks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Register inserts a new hook row. The caller passes allowedShell=true only
// if the user creating the hook has OWNER role; non-OWNER callers trying to
// register a shell hook get ErrShellHookNotAllowed back. The validation is
// duplicated here (vs. at the handler) so a misconfigured call path can't
// bypass the guard.
//
// On success returns the generated hook ID.
func Register(ctx context.Context, db *sql.DB, h Hook, allowedShell bool) (string, error) {
	if err := validateForInsert(h, allowedShell); err != nil {
		return "", err
	}
	if h.ID == "" {
		h.ID = newHookID()
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = h.CreatedAt
	}

	matcherJSON, err := json.Marshal(h.Matcher)
	if err != nil {
		return "", fmt.Errorf("hooks: marshal matcher: %w", err)
	}
	handlerCfg := h.HandlerConfig
	if handlerCfg == nil {
		handlerCfg = map[string]any{}
	}
	handlerJSON, err := json.Marshal(handlerCfg)
	if err != nil {
		return "", fmt.Errorf("hooks: marshal handler_config: %w", err)
	}

	_, err = db.ExecContext(ctx, `INSERT INTO hooks_config
		(id, workspace_id, crew_id, event, matcher, handler_kind, handler_config,
		 blocking, enabled, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID,
		h.WorkspaceID,
		nullableStr(h.CrewID),
		string(h.Event),
		string(matcherJSON),
		string(h.HandlerKind),
		string(handlerJSON),
		boolToInt(h.Blocking),
		boolToInt(h.Enabled),
		nullableStr(h.CreatedBy),
		h.CreatedAt.UTC().Format(time.RFC3339Nano),
		h.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return "", fmt.Errorf("hooks: insert: %w", err)
	}
	return h.ID, nil
}

// validateForInsert is the shared guard used by Register. Split out so tests
// can exercise the branches without a DB.
func validateForInsert(h Hook, allowedShell bool) error {
	if h.WorkspaceID == "" {
		return errors.New("hooks: workspace_id required")
	}
	if h.Event == "" {
		return errors.New("hooks: event required")
	}
	switch h.HandlerKind {
	case HandlerKindShell:
		if !allowedShell {
			return ErrShellHookNotAllowed
		}
		if _, ok := h.HandlerConfig["command"].(string); !ok {
			return errors.New("hooks: shell handler requires handler_config.command (string)")
		}
	case HandlerKindHTTP:
		if _, ok := h.HandlerConfig["url"].(string); !ok {
			return errors.New("hooks: http handler requires handler_config.url (string)")
		}
	case HandlerKindSubagent:
		// Agent selection is handler-specific; don't enforce shape here.
	default:
		return ErrUnknownHandlerKind
	}
	return nil
}

// Delete removes a hook row, scoped to the workspace so cross-tenant
// deletes are impossible from a buggy caller.
func Delete(ctx context.Context, db *sql.DB, workspaceID, id string) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM hooks_config WHERE workspace_id = ? AND id = ?`,
		workspaceID, id)
	if err != nil {
		return fmt.Errorf("hooks: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetEnabled flips the enabled flag. workspaceID is load-bearing for
// tenant isolation — without it in the WHERE predicate, a caller who
// learned another workspace's hook ID could toggle it cross-tenant.
// Callers MUST pass the workspace resolved from their auth context.
// Returns sql.ErrNoRows when the id doesn't exist within the workspace,
// so a cross-tenant ID surfaces identically to a missing one (no
// existence leak).
func SetEnabled(ctx context.Context, db *sql.DB, workspaceID, id string, enabled bool) error {
	res, err := db.ExecContext(ctx,
		`UPDATE hooks_config SET enabled = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
		boolToInt(enabled), time.Now().UTC().Format(time.RFC3339Nano), id, workspaceID)
	if err != nil {
		return fmt.Errorf("hooks: set enabled: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("hooks: rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func Enable(ctx context.Context, db *sql.DB, workspaceID, id string) error {
	return SetEnabled(ctx, db, workspaceID, id, true)
}

func Disable(ctx context.Context, db *sql.DB, workspaceID, id string) error {
	return SetEnabled(ctx, db, workspaceID, id, false)
}

// ListByEvent loads every enabled hook for (workspaceID, event) whose crew
// scope is compatible with crewID. A hook with crew_id = NULL is
// workspace-wide and fires for every crew; a hook with crew_id = X fires
// only when crewID == X. Results are ordered by created_at + id so the
// dispatch order is deterministic across runs.
//
// Passing an empty crewID returns only the workspace-wide hooks, which is
// what call sites that are not bound to a crew (workspace-level events)
// want.
func ListByEvent(ctx context.Context, db *sql.DB, workspaceID, crewID string, event Event) ([]Hook, error) {
	if workspaceID == "" {
		return nil, errors.New("hooks: ListByEvent requires workspace_id")
	}
	var (
		query string
		args  []any
	)
	if crewID == "" {
		query = `SELECT id, workspace_id, crew_id, event, matcher, handler_kind, handler_config,
			blocking, enabled, created_by, created_at, updated_at
			FROM hooks_config
			WHERE workspace_id = ? AND event = ? AND enabled = 1 AND crew_id IS NULL
			ORDER BY created_at ASC, id ASC`
		args = []any{workspaceID, string(event)}
	} else {
		query = `SELECT id, workspace_id, crew_id, event, matcher, handler_kind, handler_config,
			blocking, enabled, created_by, created_at, updated_at
			FROM hooks_config
			WHERE workspace_id = ? AND event = ? AND enabled = 1
			  AND (crew_id IS NULL OR crew_id = ?)
			ORDER BY created_at ASC, id ASC`
		args = []any{workspaceID, string(event), crewID}
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hooks: list by event: %w", err)
	}
	defer rows.Close()

	out := make([]Hook, 0, 8)
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Get fetches a single hook scoped to workspaceID. Returns (nil, nil) when
// the row does not exist so callers can distinguish "missing" from "error".
func Get(ctx context.Context, db *sql.DB, workspaceID, id string) (*Hook, error) {
	row := db.QueryRowContext(ctx, `SELECT id, workspace_id, crew_id, event, matcher,
		handler_kind, handler_config, blocking, enabled, created_by, created_at, updated_at
		FROM hooks_config WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	h, err := scanHook(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &h, nil
}

// rowScanner lets scanHook work over both *sql.Row and *sql.Rows without
// duplicating the projection list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanHook(r rowScanner) (Hook, error) {
	var (
		h                                                Hook
		crewID, createdBy                                sql.NullString
		matcherStr, handlerCfgStr, kind, createdAt, upd  string
		blockingInt, enabledInt                          int
		eventStr                                         string
	)
	if err := r.Scan(
		&h.ID,
		&h.WorkspaceID,
		&crewID,
		&eventStr,
		&matcherStr,
		&kind,
		&handlerCfgStr,
		&blockingInt,
		&enabledInt,
		&createdBy,
		&createdAt,
		&upd,
	); err != nil {
		return Hook{}, err
	}
	h.CrewID = crewID.String
	h.CreatedBy = createdBy.String
	h.Event = Event(eventStr)
	h.HandlerKind = HandlerKind(kind)
	h.Blocking = blockingInt != 0
	h.Enabled = enabledInt != 0

	if matcherStr != "" && matcherStr != "{}" {
		if err := json.Unmarshal([]byte(matcherStr), &h.Matcher); err != nil {
			return Hook{}, fmt.Errorf("hooks: unmarshal matcher: %w", err)
		}
	}
	if handlerCfgStr != "" && handlerCfgStr != "{}" {
		h.HandlerConfig = map[string]any{}
		if err := json.Unmarshal([]byte(handlerCfgStr), &h.HandlerConfig); err != nil {
			return Hook{}, fmt.Errorf("hooks: unmarshal handler_config: %w", err)
		}
	} else {
		h.HandlerConfig = map[string]any{}
	}

	if t, err := parseTS(createdAt); err == nil {
		h.CreatedAt = t
	}
	if t, err := parseTS(upd); err == nil {
		h.UpdatedAt = t
	}
	return h, nil
}

// parseTS accepts both the RFC3339Nano form we write and the shorter
// datetime('now') SQLite produces, matching the pattern journal.parseTS
// uses.
func parseTS(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("hooks: unparseable timestamp %q", s)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newHookID generates a random identifier prefixed with "hk_". 64 bits of
// entropy is plenty at this scale; we only need uniqueness within a
// workspace, not global collision resistance.
func newHookID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "hk_" + hex.EncodeToString(b[:])
}
