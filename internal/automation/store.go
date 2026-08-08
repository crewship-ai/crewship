package automation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Store is the DB access layer for the `automations` table.
//
// Every method is workspace-scoped. That is not decoration: an automation
// names a routine and fires it with data lifted out of a journal entry, so a
// rule that could be read or edited across a workspace boundary would be a
// cross-tenant execution primitive.
type Store struct {
	db *sql.DB
}

// NewStore wraps a DB handle.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// deleted_at is selected even though Get and List filter it out, because
// GetIncludingDeleted does not — and a struct whose DeletedAt is always nil
// would make "is this a tombstone" unanswerable for the one caller that asks.
const automationColumns = `id, workspace_id, name, enabled, event_type, matcher_json,
	action_kind, action_config_json, debounce_seconds, max_per_hour,
	COALESCE(created_by, ''), created_at, updated_at, deleted_at`

// Create inserts a validated automation and returns the stored row.
func (s *Store) Create(ctx context.Context, a Automation) (Automation, error) {
	if err := a.Validate(); err != nil {
		return Automation{}, err
	}
	if a.ID == "" {
		a.ID = newAutomationID()
	}
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now

	matcher, err := json.Marshal(a.Matcher)
	if err != nil {
		return Automation{}, fmt.Errorf("automation: marshal matcher: %w", err)
	}
	action, err := json.Marshal(a.Action)
	if err != nil {
		return Automation{}, fmt.Errorf("automation: marshal action: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO automations (
    id, workspace_id, name, enabled, event_type, matcher_json,
    action_kind, action_config_json, debounce_seconds, max_per_hour,
    created_by, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.WorkspaceID, a.Name, boolInt(a.Enabled), a.EventType, string(matcher),
		a.ActionKind, string(action), a.DebounceSeconds, a.MaxPerHour,
		nullStr(a.CreatedBy), tsformat.Format(now), tsformat.Format(now)); err != nil {
		return Automation{}, fmt.Errorf("automation: insert: %w", err)
	}
	return a, nil
}

// Get returns one automation inside workspaceID. A row that belongs to
// another workspace, or that was soft-deleted, is ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (Automation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+automationColumns+` FROM automations
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, workspaceID)
	a, err := scanAutomation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Automation{}, ErrNotFound
	}
	return a, err
}

// GetIncludingDeleted returns one automation inside workspaceID whether or not
// it was soft-deleted. The workspace fence still applies; only the liveness
// predicate is lifted.
//
// This exists so the store keeps owning BOTH definitions of "visible" rather
// than a caller hand-rolling the second one. Two questions are being asked of
// this table and they want different answers:
//
//   - "what is wired right now" — every operational surface, and Get. A deleted
//     rule is gone: it cannot fire, be edited, or be listed.
//   - "how did this happen" — internal/chain. pipeline_runs keeps
//     triggered_via='automation' forever, so after a delete the run still
//     records that a rule started it while the rule stops resolving. Answering
//     the historical question with the operational predicate draws a run with
//     no origin, and a reader takes that to mean nobody started it.
//
// Callers answering the first question must use Get. Reaching for this one to
// dodge a not-found is how a deleted rule gets presented as live wiring; the
// caller is responsible for marking what it renders (chain spells the status
// "deleted", distinct from "disabled").
func (s *Store) GetIncludingDeleted(ctx context.Context, workspaceID, id string) (Automation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+automationColumns+` FROM automations
		 WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	a, err := scanAutomation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Automation{}, ErrNotFound
	}
	return a, err
}

// List returns every live automation in a workspace, newest first.
func (s *Store) List(ctx context.Context, workspaceID string) ([]Automation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+automationColumns+` FROM automations
		 WHERE workspace_id = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("automation: list: %w", err)
	}
	defer rows.Close()
	out := []Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Patch is a sparse update. A nil field is "leave alone", which is what
// makes `automation enable` a one-field write rather than a read-modify-write
// that can clobber a concurrent edit to the matcher.
type Patch struct {
	Name            *string
	Enabled         *bool
	EventType       *string
	Matcher         *Matcher
	Action          *Action
	DebounceSeconds *int
	MaxPerHour      *int
}

// Update applies a sparse patch and returns the stored row.
func (s *Store) Update(ctx context.Context, workspaceID, id string, p Patch) (Automation, error) {
	cur, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return Automation{}, err
	}
	if p.Name != nil {
		cur.Name = *p.Name
	}
	if p.Enabled != nil {
		cur.Enabled = *p.Enabled
	}
	if p.EventType != nil {
		cur.EventType = *p.EventType
	}
	if p.Matcher != nil {
		cur.Matcher = *p.Matcher
	}
	if p.Action != nil {
		cur.Action = *p.Action
	}
	if p.DebounceSeconds != nil {
		cur.DebounceSeconds = *p.DebounceSeconds
	}
	if p.MaxPerHour != nil {
		cur.MaxPerHour = *p.MaxPerHour
	}
	if err := cur.Validate(); err != nil {
		return Automation{}, err
	}
	matcher, err := json.Marshal(cur.Matcher)
	if err != nil {
		return Automation{}, fmt.Errorf("automation: marshal matcher: %w", err)
	}
	action, err := json.Marshal(cur.Action)
	if err != nil {
		return Automation{}, fmt.Errorf("automation: marshal action: %w", err)
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE automations
SET name = ?, enabled = ?, event_type = ?, matcher_json = ?,
    action_kind = ?, action_config_json = ?, debounce_seconds = ?, max_per_hour = ?,
    updated_at = ?
WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		cur.Name, boolInt(cur.Enabled), cur.EventType, string(matcher),
		cur.ActionKind, string(action), cur.DebounceSeconds, cur.MaxPerHour,
		tsformat.Format(now), id, workspaceID)
	if err != nil {
		return Automation{}, fmt.Errorf("automation: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Automation{}, ErrNotFound
	}
	cur.UpdatedAt = now
	return cur, nil
}

// Delete soft-deletes an automation. Soft rather than hard because a rule
// that fired runs is part of why those runs exist: hard-deleting it turns
// every run it caused into an orphan whose trigger cannot be explained.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations SET deleted_at = ?, updated_at = ?
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		tsformat.Format(time.Now().UTC()), tsformat.Format(time.Now().UTC()), id, workspaceID)
	if err != nil {
		return fmt.Errorf("automation: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListActive returns every enabled, live automation across every workspace,
// with its target routine already resolved to a pipeline id.
//
// This is the ONLY query the trigger path performs, and it runs off the write
// path — once at boot, once per write, and once a minute. Resolving the slug
// here rather than in Observer is what lets the observer be a pure in-memory
// function: by the time an event arrives, the answer to "which pipeline is
// that" is already in the struct.
//
// An automation whose routine_slug does not resolve is skipped and reported
// by the caller. Enqueuing against a pipeline id we could not find would park
// a row the dispatcher can never fire.
func (s *Store) ListActive(ctx context.Context) ([]Resolved, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.id, a.workspace_id, a.name, a.enabled, a.event_type, a.matcher_json,
       a.action_kind, a.action_config_json, a.debounce_seconds, a.max_per_hour,
       COALESCE(a.created_by, ''), a.created_at, a.updated_at,
       COALESCE(p.id, ''), COALESCE(p.slug, '')
FROM automations a
LEFT JOIN pipelines p
       ON p.workspace_id = a.workspace_id
      AND p.slug = json_extract(a.action_config_json, '$.routine_slug')
      AND p.deleted_at IS NULL
WHERE a.deleted_at IS NULL AND a.enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("automation: list active: %w", err)
	}
	defer rows.Close()
	out := []Resolved{}
	for rows.Next() {
		var (
			r          Resolved
			enabled    int
			matcherRaw string
			actionRaw  string
			createdAt  string
			updatedAt  string
		)
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Name, &enabled, &r.EventType, &matcherRaw,
			&r.ActionKind, &actionRaw, &r.DebounceSeconds, &r.MaxPerHour,
			&r.CreatedBy, &createdAt, &updatedAt, &r.PipelineID, &r.PipelineSlug); err != nil {
			return nil, fmt.Errorf("automation: scan active: %w", err)
		}
		r.Enabled = enabled != 0
		decodeJSON(matcherRaw, &r.Matcher)
		decodeJSON(actionRaw, &r.Action)
		r.CreatedAt = parseTS(createdAt)
		r.UpdatedAt = parseTS(updatedAt)
		if r.PipelineID == "" {
			// Unresolvable target. Skipping here beats enqueuing a run the
			// dispatcher can never fire — the row stays in the table and
			// starts working the moment the routine exists.
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAutomation(sc rowScanner) (Automation, error) {
	var (
		a          Automation
		enabled    int
		matcherRaw string
		actionRaw  string
		createdAt  string
		updatedAt  string
		deletedAt  sql.NullString
	)
	if err := sc.Scan(&a.ID, &a.WorkspaceID, &a.Name, &enabled, &a.EventType, &matcherRaw,
		&a.ActionKind, &actionRaw, &a.DebounceSeconds, &a.MaxPerHour,
		&a.CreatedBy, &createdAt, &updatedAt, &deletedAt); err != nil {
		return Automation{}, err
	}
	a.Enabled = enabled != 0
	decodeJSON(matcherRaw, &a.Matcher)
	decodeJSON(actionRaw, &a.Action)
	a.CreatedAt = parseTS(createdAt)
	a.UpdatedAt = parseTS(updatedAt)
	// Nil rather than a zero Time for a live row: a caller testing DeletedAt !=
	// nil must not be told every rule is a tombstone, and an unparseable stamp
	// still means deleted — the row is only written by Delete.
	if deletedAt.Valid && deletedAt.String != "" {
		t := parseTS(deletedAt.String)
		a.DeletedAt = &t
	}
	return a, nil
}

// decodeJSON is best-effort on purpose. A stored matcher that will not
// decode leaves the zero value — "match everything of this type" — which is
// the same behaviour as the documented empty matcher, rather than making the
// whole refresh fail and taking every OTHER workspace's automations down
// with it.
func decodeJSON(raw string, into any) {
	if raw == "" {
		return
	}
	_ = json.Unmarshal([]byte(raw), into)
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil { // tsformat:allow: read-only parse of a tsformat.Format value, never compared in SQL
		return t
	}
	// Rows written by a `datetime('now','subsec')` default carry the SQLite
	// space-separated form; accept it so a hand-inserted or restored row is
	// not reported with a zero timestamp.
	if t, err := time.Parse("2006-01-02 15:04:05.999999999", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newAutomationID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "aut_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano))) // tsformat:allow: entropy fallback for an opaque id, never compared in SQL
	}
	return "aut_" + hex.EncodeToString(b)
}
