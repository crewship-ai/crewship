package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Sentinel errors returned by Store. Wrap with %w if you need to add
// context; callers can errors.Is against these.
var (
	ErrNotFound          = errors.New("pipeline: not found")
	ErrSlugConflict      = errors.New("pipeline: slug already exists in workspace")
	ErrTestRunGateFailed = errors.New("pipeline: save requires a fresh, passing test_run")
	// ErrRoutineNotActive is the governance airbag enforced INSIDE the
	// executor: a real run (ModeRun) of a routine that is not 'active'
	// (proposed → awaiting approval, disabled → admin airbag) is refused.
	// Enforcing it at the executor — not only at the HTTP handlers — closes
	// the cron / webhook / batch / deferred-dispatch paths that call
	// executor.Run directly and would otherwise bypass the status gate.
	ErrRoutineNotActive = errors.New("pipeline: routine is not active")
)

// testRunFreshness is how recently a test_run must have passed for the
// save endpoint to accept it. The save handler enforces this; the
// store records the timestamp so any later read can show "tested 4
// minutes ago" or warn that the test is stale before re-edit.
const testRunFreshness = 5 * time.Minute

// Store wraps the pipelines table with the operations the executor,
// sidecar handlers, and main API need. All methods take a context so
// long-running queries (e.g. a workspace with thousands of pipelines)
// can be cancelled when the request is.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store backed by the given DB handle. The handle
// must already be open and migrated to v78 or later.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Save persists a new pipeline OR upserts an existing one (matched by
// workspace_id + slug). The test-run gate is enforced here: if
// in.LastTestRunPassed is false, the save is rejected. The handler is
// responsible for calling test_run first and threading the result in.
//
// Returns the saved Pipeline (with generated id and timestamps) so
// the caller can echo the canonical representation back to the agent.
func (s *Store) Save(ctx context.Context, in SaveInput) (*Pipeline, error) {
	if in.WorkspaceID == "" {
		return nil, errors.New("pipeline: workspace_id required")
	}
	if in.Slug == "" {
		return nil, errors.New("pipeline: slug required")
	}
	if in.DefinitionJSON == "" {
		return nil, errors.New("pipeline: definition_json required")
	}
	if !in.LastTestRunPassed {
		return nil, ErrTestRunGateFailed
	}
	// Freshness check uses time.Since (server clock) so a caller
	// cannot mint a passing gate by claiming a stale timestamp.
	// Future timestamps are also rejected — without this check, a
	// claim of last_test_run_at = "year 9999" would always look
	// fresh because Since returns negative durations. We allow a
	// small clock-skew tolerance (1 minute) so distributed callers
	// with mildly drifting clocks don't get spurious failures.
	if in.LastTestRunAt == nil {
		return nil, ErrTestRunGateFailed
	}
	since := time.Since(*in.LastTestRunAt)
	if since > testRunFreshness || since < -1*time.Minute {
		return nil, ErrTestRunGateFailed
	}
	if in.Author.Via == "" {
		in.Author.Via = AuthoredViaAgent
	}
	if in.DSLVersion == "" {
		in.DSLVersion = "1.0"
	}
	// Governance status: empty means "live". The save handlers set
	// 'proposed' for risky routines so they land in the maker-checker queue.
	status := in.Status
	if status == "" {
		status = "active"
	}

	hash := definitionHash(in.DefinitionJSON)
	now := time.Now().UTC()

	// Look up existing row by (workspace_id, slug). If present,
	// update in place (preserving id, created_at, invocation_count
	// — those should never reset on edit). If absent, insert new.
	existingID, existingCreatedAt, err := s.findIDBySlug(ctx, in.WorkspaceID, in.Slug)
	switch {
	case errors.Is(err, ErrNotFound):
		// fall through to insert
	case err != nil:
		return nil, fmt.Errorf("pipeline: lookup existing slug: %w", err)
	default:
		// update path — wraps the in-place UPDATE + the
		// pipeline_versions insert in a single transaction so the
		// head pointer and the immutable history row land
		// atomically. Without this, a crash between the two writes
		// would leave the head pointing at a version row that
		// doesn't exist (or vice versa).
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("pipeline: begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		// Disable-airbag invariant: a routine an OWNER/ADMIN explicitly
		// 'disabled' must stay disabled across an edit. statusForRisk only
		// ever yields 'active'/'proposed', so without this a plain re-save
		// (user OR agent re-author) would silently revive a killed routine,
		// bypassing the OWNER/ADMIN-only enable gate. Re-enable is the
		// deliberate path (SetStatus via the enable handler), not a save.
		var existingStatus string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(status, 'active') FROM pipelines WHERE id = ?`, existingID,
		).Scan(&existingStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("pipeline: read existing status: %w", err)
		}
		if existingStatus == "disabled" {
			status = "disabled"
		}

		_, err = tx.ExecContext(ctx, `
UPDATE pipelines SET
    name = ?, description = ?, dsl_version = ?, definition_json = ?, definition_hash = ?,
    author_crew_id = ?, author_agent_id = ?, author_user_id = ?, author_chat_id = ?, author_run_id = ?,
    authored_via = ?, imported_from_url = ?,
    last_test_run_at = ?, last_test_run_passed = 1,
    execution_tier_json = ?,
    status = ?,
    updated_at = ?,
    deleted_at = NULL
WHERE id = ?`,
			in.Name, nullStr(in.Description), in.DSLVersion, in.DefinitionJSON, hash,
			nullStr(in.Author.CrewID), nullStr(in.Author.AgentID), nullStr(in.Author.UserID),
			nullStr(in.Author.ChatID), nullStr(in.Author.RunID),
			string(in.Author.Via), nullStr(in.Author.ImportedURL),
			in.LastTestRunAt.UTC().Format(time.RFC3339Nano),
			nullStr(in.ExecutionTierJSON),
			status,
			now.Format(time.RFC3339Nano),
			existingID,
		)
		if err != nil {
			return nil, fmt.Errorf("pipeline: update: %w", err)
		}

		// Append a new version row (or no-op if hash matches).
		// SaveVersion handles dedup against existing versions, so
		// re-saving identical bytes is idempotent.
		if err := s.saveVersionTx(ctx, tx, existingID, in, hash, now); err != nil {
			return nil, fmt.Errorf("pipeline: save version (update): %w", err)
		}

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("pipeline: commit: %w", err)
		}
		return s.GetByID(ctx, existingID)
	}

	id := generatePipelineID()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("pipeline: begin tx (insert): %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
INSERT INTO pipelines (
    id, workspace_id, slug, name, description, dsl_version,
    definition_json, definition_hash,
    ephemeral, workspace_visible, invocation_count,
    author_crew_id, author_agent_id, author_user_id, author_chat_id, author_run_id,
    authored_via, imported_from_url,
    last_test_run_at, last_test_run_passed,
    execution_tier_json,
    status,
    created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?,
    ?, ?,
    0, 1, 0,
    ?, ?, ?, ?, ?,
    ?, ?,
    ?, 1,
    ?,
    ?,
    ?, ?
)`,
		id, in.WorkspaceID, in.Slug, in.Name, nullStr(in.Description), in.DSLVersion,
		in.DefinitionJSON, hash,
		nullStr(in.Author.CrewID), nullStr(in.Author.AgentID), nullStr(in.Author.UserID),
		nullStr(in.Author.ChatID), nullStr(in.Author.RunID),
		string(in.Author.Via), nullStr(in.Author.ImportedURL),
		in.LastTestRunAt.UTC().Format(time.RFC3339Nano),
		nullStr(in.ExecutionTierJSON),
		status,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		// SQLite UNIQUE constraint surfaces as a "constraint failed"
		// error string; map it to ErrSlugConflict so callers can
		// distinguish "duplicate slug" from generic DB failures and
		// return a proper 409 response.
		if isUniqueViolation(err) {
			return nil, ErrSlugConflict
		}
		return nil, fmt.Errorf("pipeline: insert: %w", err)
	}

	// Append v1 to pipeline_versions in the same transaction so a
	// new pipeline always has a version row from the start.
	if err := s.saveVersionTx(ctx, tx, id, in, hash, now); err != nil {
		return nil, fmt.Errorf("pipeline: save version (insert): %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("pipeline: commit (insert): %w", err)
	}
	_ = existingCreatedAt // silence unused (kept for future audit log surface)

	return s.GetByID(ctx, id)
}

// saveVersionTx is the in-transaction variant of SaveVersion used by
// Save's atomic dual-write. Falls through silently when the
// pipeline_versions table is missing — Save was working fine pre-v79
// and we don't want to break tests that use the older minimal
// schema. Production builds always have v79 applied.
func (s *Store) saveVersionTx(ctx context.Context, tx *sql.Tx, pipelineID string, in SaveInput, hash string, now time.Time) error {
	// Detect dedup: if a row already exists with this hash, we
	// don't need to insert (re-saving identical content is a no-op
	// at the version level too). Hand off to a SELECT inside the
	// tx for atomicity.
	var existing int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM pipeline_versions WHERE pipeline_id = ? AND definition_hash = ? LIMIT 1`,
		pipelineID, hash,
	).Scan(&existing); err == nil {
		// Already have this version — no-op.
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		// Likely "no such table: pipeline_versions" on pre-v79
		// test schemas. Surface that as a soft skip so older
		// tests using the minimal schema continue to pass.
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return fmt.Errorf("hash lookup: %w", err)
	}

	// Compute next version number inside the tx.
	var head int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM pipeline_versions WHERE pipeline_id = ?`,
		pipelineID,
	).Scan(&head); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return fmt.Errorf("max version: %w", err)
	}
	newVersion := head + 1
	parentVal := sql.NullInt64{}
	if head > 0 {
		parentVal = sql.NullInt64{Int64: int64(head), Valid: true}
	}

	authorType := "agent"
	authorID := in.Author.AgentID
	if in.Author.Via == AuthoredViaUser {
		authorType = "user"
		authorID = in.Author.UserID
	} else if in.Author.Via == AuthoredViaImported {
		authorType = "imported"
		authorID = in.Author.ImportedURL
	}
	if authorID == "" {
		authorID = "unknown"
	}

	_, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_versions (
    id, pipeline_id, version, definition_json, definition_hash,
    author_type, author_id, parent_version, change_summary, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		generateVersionID(), pipelineID, newVersion, in.DefinitionJSON, hash,
		authorType, authorID, parentVal,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return fmt.Errorf("insert version: %w", err)
	}
	// Bump pipelines.head_version too.
	if _, err := tx.ExecContext(ctx,
		`UPDATE pipelines SET head_version = ? WHERE id = ?`, newVersion, pipelineID,
	); err != nil {
		return fmt.Errorf("update head: %w", err)
	}
	return nil
}

// GetByID returns the pipeline with the given id, or ErrNotFound if
// no row matches or the row is soft-deleted.
func (s *Store) GetByID(ctx context.Context, id string) (*Pipeline, error) {
	return s.scanOne(ctx, `
SELECT `+pipelineColumns+` FROM pipelines
WHERE id = ? AND deleted_at IS NULL`, id)
}

// GetBySlug returns the workspace-scoped pipeline with the given slug,
// or ErrNotFound. Used by /pipelines/{slug}/run.
func (s *Store) GetBySlug(ctx context.Context, workspaceID, slug string) (*Pipeline, error) {
	return s.scanOne(ctx, `
SELECT `+pipelineColumns+` FROM pipelines
WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, workspaceID, slug)
}

// List returns pipelines matching the filter. Zero limit means "no
// limit" but we cap at 500 to keep the [AVAILABLE PIPELINES] block
// from exploding system prompts.
func (s *Store) List(ctx context.Context, f ListFilters) ([]*Pipeline, error) {
	if f.WorkspaceID == "" {
		return nil, errors.New("pipeline: workspace_id required for list")
	}

	var (
		conds = []string{"workspace_id = ?", "deleted_at IS NULL"}
		args  = []any{f.WorkspaceID}
	)
	if !f.IncludeEphemeral {
		conds = append(conds, "ephemeral = 0")
	}
	if !f.IncludeHidden {
		conds = append(conds, "workspace_visible = 1")
	}
	if f.AuthorCrewID != "" {
		conds = append(conds, "author_crew_id = ?")
		args = append(args, f.AuthorCrewID)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}

	var orderBy string
	switch f.OrderBy {
	case OrderByRecent:
		orderBy = "ORDER BY COALESCE(last_invoked_at, created_at) DESC, name ASC"
	case OrderByName:
		orderBy = "ORDER BY name ASC"
	default:
		orderBy = "ORDER BY invocation_count DESC, name ASC"
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	q := fmt.Sprintf(`
SELECT %s FROM pipelines
WHERE %s
%s
LIMIT %d`, pipelineColumns, strings.Join(conds, " AND "), orderBy, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pipeline: list: %w", err)
	}
	defer rows.Close()

	var out []*Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("pipeline: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SoftDelete marks the pipeline as deleted without removing the row.
// Existing pipeline_runs in journal_entries remain valid (they
// reference id), and the row is excluded from List / GetBySlug /
// GetByID via the deleted_at IS NULL guard.
func (s *Store) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE pipelines SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id)
	if err != nil {
		return fmt.Errorf("pipeline: soft delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus flips a pipeline's governance status (active | proposed |
// disabled) by id. Used by the approve/enable (→active), reject (handled
// via SoftDelete), and disable (→disabled) governance endpoints. Returns
// ErrNotFound when no live row matches so callers can map to 404.
func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE pipelines SET status = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		status, now, id)
	if err != nil {
		return fmt.Errorf("pipeline: set status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordInvocation increments invocation_count and updates
// last_invoked_at + last_invocation_status. Called by the executor
// after a run completes (success or failure). Best-effort: if the
// row is soft-deleted mid-run, we silently no-op — the run already
// happened and journal_entries hold the truth.
func (s *Store) RecordInvocation(ctx context.Context, id string, status string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE pipelines
SET invocation_count = invocation_count + 1,
    last_invoked_at = ?,
    last_invocation_status = ?,
    updated_at = ?
WHERE id = ? AND deleted_at IS NULL`, now, status, now, id)
	if err != nil {
		return fmt.Errorf("pipeline: record invocation: %w", err)
	}
	return nil
}

// scanOne runs a SELECT-one query and decodes the result. Wrapped
// here so GetByID and GetBySlug share the row-decoding path.
func (s *Store) scanOne(ctx context.Context, q string, args ...any) (*Pipeline, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pipeline: query: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	p, err := scanPipeline(rows)
	if err != nil {
		return nil, fmt.Errorf("pipeline: scan: %w", err)
	}
	return p, nil
}

// findIDBySlug returns just the id + created_at for a workspace+slug
// pair. Cheap lookup used by the upsert path so we don't materialise
// the whole row twice.
//
// It intentionally does NOT filter on `deleted_at IS NULL`: the
// (workspace_id, slug) UNIQUE index counts soft-deleted rows, so a
// tombstone keeps the slug occupied at the index level. If the lookup
// skipped tombstones, the upsert would fall through to INSERT and trip
// the constraint with ErrSlugConflict — which made a slug un-recreatable
// after deletion and broke `seed --nuke` re-seeds (nuke soft-deletes
// every routine, so the next seed 409'd on every slug). Finding the
// tombstone routes Save down the UPDATE path instead, which clears
// deleted_at (resurrect) and appends a fresh version — making save an
// idempotent upsert-by-slug.
func (s *Store) findIDBySlug(ctx context.Context, workspaceID, slug string) (string, time.Time, error) {
	var (
		id        string
		createdAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, created_at FROM pipelines WHERE workspace_id = ? AND slug = ?`,
		workspaceID, slug,
	).Scan(&id, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	t, _ := time.Parse(time.RFC3339Nano, createdAt)
	return id, t, nil
}

// pipelineColumns is the canonical SELECT list. Keeping it as a const
// keeps every query aligned with the scanPipeline column ordering —
// any drift here surfaces as a clear panic in tests rather than silent
// off-by-one decoding.
const pipelineColumns = `
    id, workspace_id, slug, name, COALESCE(description, ''), dsl_version,
    definition_json, definition_hash,
    ephemeral, workspace_visible, invocation_count,
    last_invoked_at, COALESCE(last_invocation_status, ''),
    COALESCE(author_crew_id, ''), COALESCE(author_agent_id, ''),
    COALESCE(author_user_id, ''), COALESCE(author_chat_id, ''),
    COALESCE(author_run_id, ''),
    authored_via, COALESCE(imported_from_url, ''),
    last_test_run_at, last_test_run_passed,
    COALESCE(execution_tier_json, ''),
    COALESCE(status, 'active'),
    created_at, updated_at, deleted_at`

// rowScanner narrows the rows interface to just what we need so
// scanPipeline can be called from both *sql.Rows (List) and the
// single-row paths (GetByID/GetBySlug). Either of those returns a
// *sql.Rows that satisfies this interface.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanPipeline decodes one row in the order defined by pipelineColumns.
// Time fields are parsed via parseTimePtr — SQLite returns RFC3339Nano
// strings for our datetime('now','subsec') defaults, but tests can
// surprise us with bare RFC3339 too, so the helper accepts both.
func scanPipeline(rs rowScanner) (*Pipeline, error) {
	var (
		p                 Pipeline
		lastInvoked       sql.NullString
		lastTestRunAt     sql.NullString
		lastTestRunPassed int
		ephemeral, vis    int
		createdAt         string
		updatedAt         string
		deletedAt         sql.NullString
		authoredVia       string
	)
	err := rs.Scan(
		&p.ID, &p.WorkspaceID, &p.Slug, &p.Name, &p.Description, &p.DSLVersion,
		&p.DefinitionJSON, &p.DefinitionHash,
		&ephemeral, &vis, &p.InvocationCount,
		&lastInvoked, &p.LastInvocationStatus,
		&p.AuthorCrewID, &p.AuthorAgentID,
		&p.AuthorUserID, &p.AuthorChatID,
		&p.AuthorRunID,
		&authoredVia, &p.ImportedFrom,
		&lastTestRunAt, &lastTestRunPassed,
		&p.ExecutionTierJSON,
		&p.Status,
		&createdAt, &updatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	p.Ephemeral = ephemeral != 0
	p.WorkspaceVisible = vis != 0
	p.LastTestRunPassed = lastTestRunPassed != 0
	p.AuthoredVia = AuthoredVia(authoredVia)
	p.LastInvokedAt = parseTimePtr(lastInvoked.String)
	p.LastTestRunAt = parseTimePtr(lastTestRunAt.String)
	p.CreatedAt = parseTimeOrZero(createdAt)
	p.UpdatedAt = parseTimeOrZero(updatedAt)
	if deletedAt.Valid {
		t := parseTimeOrZero(deletedAt.String)
		p.DeletedAt = &t
	}
	return &p, nil
}

// definitionHash returns sha256 of the raw DSL JSON bytes as lowercase
// hex. Used for cheap dedup and for marketplace integrity checks. We
// hash the string as the caller passed it — re-marshalling would
// produce stable output but break integrity for documents that
// preserve significant whitespace or key ordering (some schemas do).
func definitionHash(definitionJSON string) string {
	return DefinitionHash([]byte(definitionJSON))
}

// DefinitionHash is the exported single-source-of-truth implementation
// of the routine definition hash. The HMAC save_token wiring in
// internal/api uses this to bind a token to a specific DSL version;
// historically internal/api had its own copy and the two were free
// to drift (different pre-processing, different encoding) which would
// silently break save_token verification. Keep one impl, share both
// callers.
func DefinitionHash(definitionJSON []byte) string {
	sum := sha256.Sum256(definitionJSON)
	return hex.EncodeToString(sum[:])
}

// generatePipelineID mints a new "pln_"-prefixed CUID. Format mirrors
// internal/api/cuid.generateCUID exactly so log lines and journal
// entries from agents and pipelines look consistent. The package-local
// copy avoids importing internal/api (which imports half the world).
//
// Layout: "pln_c" + base36(unix-millis) + 4-hex counter + 8-hex random.
func generatePipelineID() string {
	ts := time.Now().UnixMilli()
	c := pipelineCounter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b[0] = byte(c >> 24)
		b[1] = byte(c >> 16)
		b[2] = byte(ts >> 8)
		b[3] = byte(ts)
	}
	var buf [40]byte
	out := append(buf[:0], 'p', 'l', 'n', '_', 'c')
	out = strconv.AppendInt(out, ts, 36)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	out = append(out,
		hexdigits[(tail>>12)&0xf],
		hexdigits[(tail>>8)&0xf],
		hexdigits[(tail>>4)&0xf],
		hexdigits[tail&0xf],
	)
	out = append(out, hex.EncodeToString(b)...)
	return string(out)
}

var pipelineCounter atomic.Uint64

// nullStr converts an empty Go string to an SQL NULL via sql.NullString
// so the column stores NULL (and the partial indexes behave as
// expected) rather than an empty-string row that would still match the
// index predicate. Non-empty strings round-trip unchanged.
func nullStr(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

// parseTimePtr returns a pointer to the parsed time, or nil if the
// input is empty / unparseable. Used for the nullable timestamp
// columns where "no value" carries semantic weight (last_invoked_at
// nil = "never invoked", not "invoked at zero time").
func parseTimePtr(s string) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	return nil
}

// parseTimeOrZero is the non-nullable cousin of parseTimePtr — used
// for columns we know have a NOT NULL DEFAULT (created_at, updated_at).
// On parse failure we return zero time rather than panicking; tests
// that care will assert on the zero check.
func parseTimeOrZero(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// isUniqueViolation matches the SQLite error string for UNIQUE
// constraint failures across both modernc.org/sqlite and mattn's
// pure-Go alternative. Both surface the same "UNIQUE constraint
// failed:" prefix; we don't need to type-assert for finer detail.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
