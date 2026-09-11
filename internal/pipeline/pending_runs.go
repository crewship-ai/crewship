package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PendingRun is a deferred trigger parked in pending_runs (v122) — the
// backing for delay / ttl / debounce / priority. The dispatcher fires
// due rows (FireAt <= now), highest Priority first, and expires rows
// past ExpiresAt.
type PendingRun struct {
	PinnedVersion *int // nil is the legacy live-at-dispatch policy
	ID            string
	WorkspaceID   string
	PipelineID    string
	PipelineSlug  string
	InputsJSON    string
	TagsJSON      string
	MetadataJSON  string
	TierOverride  string
	Priority      int
	DebounceKey   string
	FireAt        time.Time
	ExpiresAt     *time.Time
	DebounceMaxAt *time.Time
	// InvokingUserID is the workspace user who enqueued this deferred run,
	// threaded through to the fired run so a notify step can resolve
	// `to: trigger` to a real recipient (issue #842 Phase 1). Empty for
	// service/token triggers → `to: trigger` falls back to a workspace notice.
	InvokingUserID string
	// TriggeredVia / TriggeredByID are what actually started this deferred
	// run. Empty means "did not say" — effectivePendingTrigger applies the
	// dispatcher's documented default — which is a different fact from
	// claiming a schedule.
	TriggeredVia  TriggeredVia
	TriggeredByID string
	// ChainDepth is how many composed hops led here. Threaded so a cycle
	// that leaves the process through the journal and comes back still
	// spends from the same budget runCallPipelineStep spends from.
	//
	// It is the depth this run IS at, already priced by whoever enqueued the
	// row (Registry.Flush for an automation). The dispatcher passes it through
	// rather than adding one: two places incrementing is how a cap silently
	// becomes half its stated size.
	ChainDepth int
	// ChainOrigin is the run or journal entry that started the chain this run
	// belongs to. Empty means "did not say", and the executor then roots the
	// chain at the fired run itself — which is right for a scheduled run and
	// wrong for a composed one, so a composed producer must set it.
	//
	// Depth is the safety property; this is the legibility one. Without it a
	// legitimate eight-hop chain reads as eight unrelated runs, which is both
	// unreadable and the shape a loop would prefer to present.
	ChainOrigin string
}

// effectivePendingTrigger resolves what a fired deferred run should claim.
//
// One function so the default lives in one place: the dispatcher used to
// inline it, which is why an attributed producer had nowhere to put its
// answer even once it had one.
func effectivePendingTrigger(pr PendingRun) (TriggeredVia, string) {
	if pr.TriggeredVia != "" {
		return pr.TriggeredVia, pr.TriggeredByID
	}
	return TriggeredViaSchedule, pr.ID
}

// PendingRunStore is the DB access layer for deferred dispatch.
type pendingRunDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type PendingRunStore struct{ db pendingRunDB }

// NewPendingRunStoreTx lets admission checks and enqueue share one transaction.
func NewPendingRunStoreTx(tx *sql.Tx) *PendingRunStore { return &PendingRunStore{db: tx} }

// NewPendingRunStore wraps a DB handle.
func NewPendingRunStore(db *sql.DB) *PendingRunStore {
	return &PendingRunStore{db: db}
}

// CoalesceAdmit judges a trigger that is about to coalesce into an existing
// pending row. pin is the version the row WILL carry afterwards — the row's
// own pin, or this trigger's when the row has none. Returning an error
// refuses the coalesce and leaves the row exactly as it was; the error is
// returned to the caller unwrapped so it can be classified.
//
// The store cannot judge inputs itself (it does not know the DSL), but it is
// the only place that knows which pin wins, so the two halves of the
// decision meet here rather than in a caller that read the row a moment
// earlier. Opponent round 2 on #2501: keeping the first pin and adopting the
// last inputs are each right alone and together stored a row the pinned
// recipe could not run.
type CoalesceAdmit func(ctx context.Context, pin *int) error

// EnqueueResult is what Enqueue actually stored — the receipt is written
// from this, never from what the request hoped for.
type EnqueueResult struct {
	ID            string
	Coalesced     bool
	PinnedVersion *int // the pin the row carries after this call
	FireAt        time.Time
}

// errPendingRowMoved: the row found by the lookup was claimed, cancelled or
// re-pinned before the merge landed. Enqueue starts over.
var errPendingRowMoved = errors.New("pending_runs: row moved during coalesce")

// errNoPendingRow: nothing to coalesce into; Enqueue inserts.
var errNoPendingRow = errors.New("pending_runs: no pending row")

// Enqueue parks a deferred trigger. When DebounceKey is set and a
// pending row already exists for (pipeline_id, debounce_key), the
// existing row is COALESCED: its fire_at is pushed to the new FireAt
// (capped at the original debounce_max_at), inputs/tags/metadata are
// replaced, and the existing id is returned. Otherwise a fresh row is
// inserted. Returns (id, coalesced, error). Callers that need to judge
// the coalesce, or the stored pin, use EnqueueChecked.
func (s *PendingRunStore) Enqueue(ctx context.Context, pr PendingRun) (string, bool, error) {
	res, err := s.EnqueueChecked(ctx, pr, nil)
	return res.ID, res.Coalesced, err
}

// EnqueueChecked is Enqueue with an admission hook for the coalesce path
// and a result that reports the stored state. The merge is a compare-and-
// set: it lands only on a row that is still pending and still carries the
// pin admit was asked about, so a concurrent claim or a rival trigger that
// filled the pin first sends this one back to the lookup instead of
// storing a pair nobody judged.
func (s *PendingRunStore) EnqueueChecked(ctx context.Context, pr PendingRun, admit CoalesceAdmit) (EnqueueResult, error) {
	if pr.ID == "" {
		return EnqueueResult{}, errors.New("pending_runs: id required")
	}
	const attempts = 4
	for i := 0; i < attempts; i++ {
		if pr.DebounceKey != "" {
			res, err := s.coalesceDebounce(ctx, pr, admit)
			switch {
			case err == nil:
				return res, nil
			case errors.Is(err, errNoPendingRow):
				// fall through to the insert
			case errors.Is(err, errPendingRowMoved):
				continue
			default:
				return EnqueueResult{}, err
			}
		}

		_, err := s.db.ExecContext(ctx, `
INSERT INTO pending_runs (
    id, workspace_id, pipeline_id, pipeline_slug, inputs_json, tags_json, metadata_json,
    tier_override, priority, debounce_key, fire_at, expires_at, debounce_max_at,
    invoking_user_id, triggered_via, triggered_by_id, chain_depth, chain_origin, pinned_version, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', datetime('now','subsec'), datetime('now','subsec'))`,
			pr.ID, pr.WorkspaceID, pr.PipelineID, pr.PipelineSlug,
			orJSON(pr.InputsJSON, "{}"), orJSON(pr.TagsJSON, "[]"), orJSON(pr.MetadataJSON, "{}"),
			nullableStr(pr.TierOverride), pr.Priority, nullableStr(pr.DebounceKey),
			pr.FireAt.UTC().Format(time.RFC3339Nano), nullableTime(pr.ExpiresAt), nullableTime(pr.DebounceMaxAt),
			nullableStr(pr.InvokingUserID),
			nullableStr(string(pr.TriggeredVia)), nullableStr(pr.TriggeredByID), pr.ChainDepth,
			nullableStr(pr.ChainOrigin), pr.PinnedVersion)
		if err != nil {
			// Debounce race: a concurrent trigger with the same key inserted
			// first, so the partial-unique index rejects this one. Both
			// requests looked before either INSERTed — go round again and
			// coalesce into the row that now exists instead of surfacing a 500.
			if pr.DebounceKey != "" && isUniqueViolation(err) {
				continue
			}
			return EnqueueResult{}, fmt.Errorf("pending_runs: insert: %w", err)
		}
		return EnqueueResult{ID: pr.ID, PinnedVersion: pr.PinnedVersion, FireAt: pr.FireAt}, nil
	}
	return EnqueueResult{}, fmt.Errorf("pending_runs: debounce row for %s/%s kept moving after %d attempts", pr.PipelineID, pr.DebounceKey, attempts)
}

// coalesceDebounce merges pr into the pending row for (pipeline,
// debounce_key). errNoPendingRow when there is none; errPendingRowMoved
// when the row changed under the merge.
func (s *PendingRunStore) coalesceDebounce(ctx context.Context, pr PendingRun, admit CoalesceAdmit) (EnqueueResult, error) {
	var existingID string
	var maxAt sql.NullString
	var existingPin *int
	err := s.db.QueryRowContext(ctx, `
SELECT id, COALESCE(debounce_max_at,''), pinned_version FROM pending_runs
WHERE pipeline_id = ? AND debounce_key = ? AND status = 'pending'`,
		pr.PipelineID, pr.DebounceKey).Scan(&existingID, &maxAt, &existingPin)
	if errors.Is(err, sql.ErrNoRows) {
		return EnqueueResult{}, errNoPendingRow
	}
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("pending_runs: coalesce lookup: %w", err)
	}
	fireAt := pr.FireAt
	if maxAt.String != "" {
		if cap, perr := time.Parse(time.RFC3339Nano, maxAt.String); perr == nil && fireAt.After(cap) {
			fireAt = cap
		}
	}
	// pinned_version stays with the FIRST trigger (#2500). A debounce window
	// turns a burst into one logical trigger, and that trigger was accepted
	// against whatever was published when the window opened; a later trigger
	// in the same window must not silently move it onto a recipe that was
	// published in between. An existing pin is kept and only an empty one is
	// filled, so a legacy unpinned row can still acquire a pin from the
	// first pinned trigger that coalesces into it. Found by the opponent
	// review of PR #2501 — the first probe re-used a stale routine object and
	// could not have failed.
	effectivePin := existingPin
	if effectivePin == nil {
		effectivePin = pr.PinnedVersion
	}
	// The payload this merge would store is judged against the pin it would
	// store, before anything is written.
	if admit != nil {
		if err := admit(ctx, effectivePin); err != nil {
			return EnqueueResult{}, err
		}
	}
	// Coalescing adopts the LATEST trigger's payload (inputs/tags/metadata),
	// so it must also adopt its invoking user — otherwise a run that fires
	// with user B's inputs would notify user A (the original enqueuer) on a
	// `to: trigger` step. Attribution follows the payload it belongs to.
	//
	// triggered_via / triggered_by_id are attribution in the same sense and
	// move for the same reason. debounce_key is caller-supplied on the
	// deferred-run endpoint, so an automation's row and a user's defer can
	// meet on one row in either order; leaving the byline behind meant the
	// FIRST producer got credit for the LAST one's payload. Both directions
	// are wrong and only one of them is quiet: a user's inputs firing under a
	// rule's name is a forged audit trail, and a rule's run reading as a cron
	// is the exact confusion the columns were added to end.
	// The chain position does NOT follow the payload — it follows the DEEPEST
	// claimant. Attribution moves because a forged byline is the harm there;
	// the budget must not, because under-charging is the harm here. A row that
	// took the later trigger's depth would hand a composed cycle its allowance
	// back every time a hop met a shallower pending row: an automation at
	// depth 6 coalescing into a user's depth-0 defer would fire at 0 and buy
	// eight more hops. Charged too much is survivable; charged too little is
	// the unbounded loop this cap exists to stop.
	//
	// chain_origin travels with the depth that won, so a chain that keeps its
	// budget also keeps its root. Found by the runtime harness, which produced
	// ten status changes against a cap of nine and two distinct origins.
	//
	// The WHERE clause is the compare-and-set: the row must still be pending
	// (a dispatcher's claim is `status = 'fired'`) and must still carry the
	// pin admit was asked about, or the judged pair and the stored pair would
	// differ. Zero rows means start over.
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs
SET inputs_json = ?, tags_json = ?, metadata_json = ?, tier_override = ?,
    priority = ?, fire_at = ?, expires_at = ?, invoking_user_id = ?,
    triggered_via = ?, triggered_by_id = ?,
    pinned_version = ?,
    chain_origin = CASE WHEN ? > COALESCE(chain_depth,0) THEN ? ELSE chain_origin END,
    chain_depth  = MAX(COALESCE(chain_depth,0), ?),
    updated_at = datetime('now','subsec')
WHERE id = ? AND status = 'pending' AND pinned_version IS ?`,
		orJSON(pr.InputsJSON, "{}"), orJSON(pr.TagsJSON, "[]"), orJSON(pr.MetadataJSON, "{}"),
		nullableStr(pr.TierOverride), pr.Priority, fireAt.UTC().Format(time.RFC3339Nano),
		nullableTime(pr.ExpiresAt), nullableStr(pr.InvokingUserID),
		nullableStr(string(pr.TriggeredVia)), nullableStr(pr.TriggeredByID), effectivePin,
		pr.ChainDepth, nullableStr(pr.ChainOrigin), pr.ChainDepth,
		existingID, existingPin)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("pending_runs: coalesce: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return EnqueueResult{}, errPendingRowMoved
	}
	return EnqueueResult{ID: existingID, Coalesced: true, PinnedVersion: effectivePin, FireAt: fireAt}, nil
}

// ClaimDue atomically takes the current row, not a stale DueRuns snapshot.
// Coalescing may replace its payload or postpone it after it was listed.
// RETURNING binds the executed inputs, pin, attribution and occurrence to
// the winning claim; later coalesces must create a new pending row.
func (s *PendingRunStore) ClaimDue(ctx context.Context, id string, now time.Time) (*PendingRun, error) {
	var pr PendingRun
	var fireAt string
	at := now.UTC().Format(time.RFC3339Nano)
	err := s.db.QueryRowContext(ctx, `
UPDATE pending_runs
SET status='fired', fired_run_id='', updated_at=datetime('now','subsec')
WHERE id=? AND status='pending' AND fire_at<=?
  AND (expires_at IS NULL OR expires_at>?)
RETURNING id, workspace_id, pipeline_id, pipeline_slug, inputs_json, tags_json, metadata_json,
    COALESCE(tier_override,''), priority, COALESCE(invoking_user_id,''),
    COALESCE(triggered_via,''), COALESCE(triggered_by_id,''), COALESCE(chain_depth,0),
    COALESCE(chain_origin,''), pinned_version, fire_at`, id, at, at).Scan(
		&pr.ID, &pr.WorkspaceID, &pr.PipelineID, &pr.PipelineSlug,
		&pr.InputsJSON, &pr.TagsJSON, &pr.MetadataJSON, &pr.TierOverride, &pr.Priority,
		&pr.InvokingUserID, &pr.TriggeredVia, &pr.TriggeredByID, &pr.ChainDepth,
		&pr.ChainOrigin, &pr.PinnedVersion, &fireAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pending_runs: claim current row: %w", err)
	}
	pr.FireAt, err = time.Parse(time.RFC3339Nano, fireAt) // tsformat:allow: parsing a persisted timestamp
	if err != nil {
		return nil, fmt.Errorf("pending_runs: parse claimed fire_at: %w", err)
	}
	return &pr, nil
}

// ExpireDue marks pending rows past their ttl as expired. Returns the
// count. Run before DueRuns so an expired-but-due row never fires.
func (s *PendingRunStore) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs SET status = 'expired', updated_at = datetime('now','subsec')
WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= ?`,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DueRuns returns pending rows whose fire_at has arrived, highest
// priority first (FIFO within a priority). Caller fires each, then
// MarkFired/MarkFailed.
func (s *PendingRunStore) DueRuns(ctx context.Context, now time.Time, limit int) ([]PendingRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workspace_id, pipeline_id, pipeline_slug, inputs_json, tags_json, metadata_json,
       COALESCE(tier_override,''), priority, COALESCE(invoking_user_id,''),
       COALESCE(triggered_via,''), COALESCE(triggered_by_id,''), COALESCE(chain_depth,0),
       COALESCE(chain_origin,''), pinned_version, fire_at
FROM pending_runs
WHERE status = 'pending' AND fire_at <= ?
ORDER BY priority DESC, created_at ASC
LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRun
	for rows.Next() {
		var pr PendingRun
		var fireAt string
		if err := rows.Scan(&pr.ID, &pr.WorkspaceID, &pr.PipelineID, &pr.PipelineSlug,
			&pr.InputsJSON, &pr.TagsJSON, &pr.MetadataJSON, &pr.TierOverride, &pr.Priority,
			&pr.InvokingUserID, &pr.TriggeredVia, &pr.TriggeredByID, &pr.ChainDepth,
			&pr.ChainOrigin, &pr.PinnedVersion, &fireAt); err != nil {
			return nil, err
		}
		pr.FireAt, err = time.Parse(time.RFC3339Nano, fireAt) // tsformat:allow: parsing a persisted timestamp, not writing or comparing SQL text
		if err != nil {
			return nil, fmt.Errorf("pending_runs: parse fire_at for %s: %w", pr.ID, err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// MarkFired records that a pending row dispatched into run runID. Scoped
// to status='pending' so a concurrent dispatcher can't double-fire.
func (s *PendingRunStore) MarkFired(ctx context.Context, id, runID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs SET status = 'fired', fired_run_id = ?, updated_at = datetime('now','subsec')
WHERE id = ? AND status = 'pending'`, runID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetFiredRunID backfills the dispatched run id after a claim (which
// stamps status='fired' with an empty run id). No status guard — the
// row is already ours post-claim.
func (s *PendingRunStore) SetFiredRunID(ctx context.Context, id, runID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pending_runs SET fired_run_id = ?, updated_at = datetime('now','subsec') WHERE id = ?`,
		runID, id)
	return err
}

// Cancel removes a pending row before it fires.
func (s *PendingRunStore) Cancel(ctx context.Context, workspaceID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs SET status = 'cancelled', updated_at = datetime('now','subsec')
WHERE id = ? AND workspace_id = ? AND status = 'pending'`, id, workspaceID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListPending returns a workspace's not-yet-fired deferred runs.
func (s *PendingRunStore) ListPending(ctx context.Context, workspaceID string, limit int) ([]PendingRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workspace_id, pipeline_id, pipeline_slug, COALESCE(debounce_key,''), priority, fire_at, pinned_version, inputs_json
FROM pending_runs WHERE workspace_id = ? AND status = 'pending'
ORDER BY fire_at ASC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRun
	for rows.Next() {
		var pr PendingRun
		var fireAt string
		if err := rows.Scan(&pr.ID, &pr.WorkspaceID, &pr.PipelineID, &pr.PipelineSlug,
			&pr.DebounceKey, &pr.Priority, &fireAt, &pr.PinnedVersion, &pr.InputsJSON); err != nil {
			return nil, err
		}
		pr.FireAt, _ = time.Parse(time.RFC3339Nano, fireAt)
		out = append(out, pr)
	}
	return out, rows.Err()
}

// orJSON returns v, or fallback when v is empty — keeps the JSON columns
// non-NULL without the caller pre-filling defaults.
func orJSON(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ChainPos is where a run already sits in a composed chain: how much budget has
// been spent, and which chain it belongs to.
//
// The two travel together because they are read together, and answering one
// without the other is what shipped first — the cap held while every hop
// re-rooted, so a bounded chain was still unreadable.
//
// Declared in this package because this package owns both columns and the
// single GuardChainDepth that spends against them; internal/automation aliases
// it rather than declaring a second.
type ChainPos struct {
	// Depth is how many composed hops led to that run.
	Depth int
	// Origin is the run or entry that started its chain. Empty means the run IS
	// the root, in which case a child's origin is that run's own id.
	Origin string
}

// RunChainReader answers where a run already sat in a composed chain. It
// satisfies automation.ChainSource, so the registry can price a hop without
// importing a database handle or knowing the columns' names.
//
// Lives here rather than in internal/automation because the row is this
// package's: a second query for pipeline_runs.chain_depth somewhere else is
// a second answer to "how deep are we", which is the failure the single
// GuardChainDepth exists to prevent.
type RunChainReader struct{ db *sql.DB }

func NewRunChainReader(db *sql.DB) *RunChainReader { return &RunChainReader{db: db} }

// ChainOf returns the run's depth and the root of its chain. The false return
// means "no such run in this workspace", which the caller reads as a
// human-caused root — an unknown run must not be treated as an ERROR, or a
// journal entry from any other producer would refuse every rule.
//
// An empty origin on a row that EXISTS is not missing data: it means that run
// is itself the root, and the caller names it as the child's origin.
func (r *RunChainReader) ChainOf(ctx context.Context, workspaceID, runID string) (ChainPos, bool, error) {
	if r == nil || r.db == nil || workspaceID == "" || runID == "" {
		return ChainPos{}, false, nil
	}
	var (
		depth  sql.NullInt64
		origin sql.NullString
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT chain_depth, chain_origin FROM pipeline_runs WHERE id = ? AND workspace_id = ?`,
		runID, workspaceID).Scan(&depth, &origin)
	if errors.Is(err, sql.ErrNoRows) {
		return ChainPos{}, false, nil
	}
	if err != nil {
		return ChainPos{}, false, fmt.Errorf("pending_runs: chain position of %q: %w", runID, err)
	}
	return ChainPos{Depth: int(depth.Int64), Origin: origin.String}, true, nil
}
