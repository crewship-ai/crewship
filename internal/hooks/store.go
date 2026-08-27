package hooks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
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
		tsformat.Format(h.CreatedAt),
		tsformat.Format(h.UpdatedAt),
	)
	if err != nil {
		return "", fmt.Errorf("hooks: insert: %w", err)
	}

	// Lint shell hooks after successful insert so OWNER authors get a
	// loud heads-up on the common $CREWSHIP_PAYLOAD-without-quotes
	// gotcha. Non-blocking — the row is already persisted and a real UI
	// path is expected to call LintShellCommand up front so the operator
	// sees warnings before committing. The log line is the durable
	// breadcrumb for the headless / API path.
	if h.HandlerKind == HandlerKindShell {
		if cmd, ok := h.HandlerConfig["command"].(string); ok {
			for _, w := range LintShellCommand(cmd) {
				slog.Default().Warn("hooks: shell command lint",
					"hook_id", h.ID,
					"workspace_id", h.WorkspaceID,
					"warning", w,
				)
			}
		}
	}
	return h.ID, nil
}

// validateForInsert is the shared guard used by Register and Update. Split
// out so tests can exercise the branches without a DB.
//
// The event check is the reason this is a chokepoint rather than a
// convenience: hooks_config CHECKs handler_kind but not event, so an
// unrecognised event name inserts cleanly and then never matches
// ListByEvent's predicate. The hook lists and toggles like any other and
// silently never fires. Rejecting it at the only write path turns a
// permanently-dead registration into an error the caller can act on.
func validateForInsert(h Hook, allowedShell bool) error {
	if err := validateEventForWrite(h.Event); err != nil {
		return err
	}
	return validateWorkspaceAndHandler(h, allowedShell)
}

// validateEventForWrite is the event half of validateForInsert, split out
// so Update can apply it only when the event is actually changing (see
// Update's comment) while Register keeps requiring it unconditionally.
func validateEventForWrite(e Event) error {
	if e == "" {
		return errors.New("hooks: event required")
	}
	return ValidateEvent(e)
}

// validateWorkspaceAndHandler is the non-event half of validateForInsert:
// workspace_id presence and the handler-kind shape/gate checks. These run
// on every write regardless of whether the event is changing — a shell
// hook edited to drop its command, or an http hook edited to drop its url,
// is just as dead as a bad event.
func validateWorkspaceAndHandler(h Hook, allowedShell bool) error {
	if h.WorkspaceID == "" {
		return errors.New("hooks: workspace_id required")
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

// Update rewrites the mutable columns of an existing hook. The caller
// supplies the FULL desired state (an already-merged Hook, not a patch) —
// merging partial input against the current row is the HTTP layer's job,
// so this function does not re-derive the desired column values from the
// row. It does read one column first (event — see below), and the UPDATE
// carries that value as a WHERE predicate so the read and the write cannot
// disagree.
//
// Three things are deliberately NOT taken from h:
//
//   - id / workspace_id come from the arguments and pin the WHERE clause,
//     so a body that names another workspace cannot move a row across
//     tenants;
//   - created_at / created_by are never rewritten — provenance of who
//     first registered a hook survives every later edit.
//
// allowedShell carries the same meaning as in Register and is checked by
// the same guard: converting an http hook into a shell hook is exactly
// the escalation the OWNER gate exists to stop, so Update must not be a
// way around it.
//
// Returns sql.ErrNoRows when no row in workspaceID has that id, matching
// SetEnabled / Delete so a cross-tenant id is indistinguishable from a
// missing one.
func Update(ctx context.Context, db *sql.DB, workspaceID string, h Hook, allowedShell bool) error {
	if h.ID == "" {
		return errors.New("hooks: update: id required")
	}
	if workspaceID == "" {
		return errors.New("hooks: update: workspace_id required")
	}
	// Validate against the workspace we are actually writing to, not the
	// one the caller put in the struct.
	h.WorkspaceID = workspaceID

	// h is the FULL merged struct (see the doc comment above), so
	// h.Event is populated even when the caller's request never
	// mentioned "event" — it just carries whatever the row already had.
	// A pre-existing row can carry an event that used to be valid and
	// no longer is (pre_tool_call, removed from AllEvents the same
	// change that added this comment): that row must stay editable and
	// disable-able for every OTHER field, the same way SetEnabled and
	// Delete don't care what event a row has. Re-running
	// validateForInsert's event check unconditionally — as if every
	// Update were a fresh Register — would instead turn "PATCH blocking
	// on a legacy hook" into a permanent "unknown event" error with no
	// way out short of deleting the row.
	//
	// So: look up the event the row actually has right now and only
	// validate h.Event when the caller is changing it. This intentionally
	// does NOT trust a caller-supplied "did you touch event" flag (the
	// HTTP layer already validates an explicit change in the request
	// body, but a future call site could get that wrong) — it re-derives
	// "changing" from the database, so the guard holds regardless of who
	// calls Update.
	currentEvent, err := currentEventFor(ctx, db, workspaceID, h.ID)
	if err != nil {
		return fmt.Errorf("hooks: update: look up current event: %w", err)
	}
	if currentEvent == "" {
		return sql.ErrNoRows
	}
	if h.Event != currentEvent {
		if err := validateEventForWrite(h.Event); err != nil {
			return err
		}
	}
	if err := validateWorkspaceAndHandler(h, allowedShell); err != nil {
		return err
	}

	// Test seam for the TOCTOU the `AND event = ?` predicate below closes.
	// Always nil in production; a test sets it to mutate the row in the
	// window between the read above and the UPDATE, so the race can be
	// asserted deterministically instead of being raced for.
	if updateEventRaceHookForTest != nil {
		updateEventRaceHookForTest()
	}

	matcherJSON, err := json.Marshal(h.Matcher)
	if err != nil {
		return fmt.Errorf("hooks: marshal matcher: %w", err)
	}
	handlerCfg := h.HandlerConfig
	if handlerCfg == nil {
		handlerCfg = map[string]any{}
	}
	handlerJSON, err := json.Marshal(handlerCfg)
	if err != nil {
		return fmt.Errorf("hooks: marshal handler_config: %w", err)
	}

	res, err := db.ExecContext(ctx, `UPDATE hooks_config SET
		crew_id = ?, event = ?, matcher = ?, handler_kind = ?, handler_config = ?,
		blocking = ?, enabled = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND event = ?`,
		nullableStr(h.CrewID),
		string(h.Event),
		string(matcherJSON),
		string(h.HandlerKind),
		string(handlerJSON),
		boolToInt(h.Blocking),
		boolToInt(h.Enabled),
		tsformat.Format(time.Now()),
		h.ID,
		workspaceID,
		// The event we validated against, not the one we are writing:
		// this pins the UPDATE to the exact row state the check above
		// was made on. Without it, a concurrent write that moves a
		// legacy pre_tool_call row onto a valid event could be undone
		// by this (now stale) update, putting the retired event back
		// without ever passing validateEventForWrite.
		string(currentEvent),
	)
	if err != nil {
		return fmt.Errorf("hooks: update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("hooks: rows affected: %w", err)
	}
	if n == 0 {
		// Either the row is gone or its event changed under us. Both
		// mean "the row you computed this patch from no longer exists
		// in that state"; both are reported as ErrNoRows so the caller
		// (and the 404 the HTTP layer maps it to) keeps one meaning,
		// and a racing client re-reads and retries rather than having
		// its stale write silently win.
		return sql.ErrNoRows
	}

	// Same lint breadcrumb Register leaves — an edit that introduces the
	// unquoted-$CREWSHIP_PAYLOAD gotcha deserves the same warning as a
	// fresh registration, otherwise operators can launder a bad command
	// through PATCH.
	if h.HandlerKind == HandlerKindShell {
		if cmd, ok := h.HandlerConfig["command"].(string); ok {
			for _, w := range LintShellCommand(cmd) {
				slog.Default().Warn("hooks: shell command lint",
					"hook_id", h.ID,
					"workspace_id", workspaceID,
					"warning", w,
				)
			}
		}
	}
	return nil
}

// updateEventRaceHookForTest is nil in production. Update calls it (when
// set) between reading the row's current event and issuing the guarded
// UPDATE, which is the only way to land another writer inside that window
// deterministically. Kept in the non-test file because the seam has to sit
// in the function it guards.
var updateEventRaceHookForTest func()

// currentEventFor returns the event a row currently has, scoped to
// workspaceID so a cross-tenant id reads back as "not found" rather than
// leaking another tenant's event value. Returns ("", nil) — not an error —
// when no such row exists in that workspace; Update treats that the same
// way SetEnabled/Delete treat a missing row (sql.ErrNoRows).
func currentEventFor(ctx context.Context, db *sql.DB, workspaceID, id string) (Event, error) {
	var event string
	err := db.QueryRowContext(ctx,
		`SELECT event FROM hooks_config WHERE id = ? AND workspace_id = ?`,
		id, workspaceID).Scan(&event)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return Event(event), nil
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
		boolToInt(enabled), tsformat.Format(time.Now()), id, workspaceID)
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
		h                                               Hook
		crewID, createdBy                               sql.NullString
		matcherStr, handlerCfgStr, kind, createdAt, upd string
		blockingInt, enabledInt                         int
		eventStr                                        string
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

// parseTS accepts both the fixed-width tsformat.Layout we now write, the
// RFC3339Nano form written before that (time.RFC3339Nano parses both), and the
// shorter
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
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "hk_" + hex.EncodeToString(b[:])
}
