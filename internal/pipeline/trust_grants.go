package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// TrustGrantStore backs standing approval grants — the "you have waved
// this same gate through ten times, stop asking" surface.
//
// The design deliberately adds no new concept to the runtime. A grant is
// looked up at the one place a blocking approval is minted
// (SQLWaitpointStore.CreateApproval), and when it fires the waitpoint row
// is still written — approved, attributed to the granting operator,
// carrying the grant id in its decision_payload. So the run history, the
// backup bundle and the run-detail UI see an approval that happened, with
// a pointer to the standing decision that made it. Nothing downstream
// needs to learn a new state.
//
// What keeps this honest is the definition_hash in the lookup key: it is
// pipeline_runs.definition_hash (v114), the hash of the definition the
// run is actually executing. An operator who trusts a gate trusts a
// specific routine body; edit the body and the hash moves, no grant
// matches, and the gate blocks again. There is no invalidation job to
// forget to run.
type TrustGrantStore struct {
	db *sql.DB
	// now is injectable so expiry is testable without sleeping.
	now func() time.Time
}

// NewTrustGrantStore wraps a DB handle. The zero dependency set is
// deliberate: the store shares the pipeline DB and needs no executor,
// policy resolver or journal emitter, which is why turning the feature on
// requires no bootstrap wiring anywhere.
func NewTrustGrantStore(db *sql.DB) *TrustGrantStore {
	return &TrustGrantStore{db: db, now: time.Now}
}

// TrustGrant is one standing decision, as stored.
type TrustGrant struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspace_id"`
	PipelineID      string     `json:"pipeline_id"`
	StepID          string     `json:"step_id"`
	DefinitionHash  string     `json:"definition_hash"`
	GrantedByUserID string     `json:"granted_by_user_id"`
	GrantedAt       string     `json:"granted_at"`
	Reason          string     `json:"reason,omitempty"`
	PriorApprovals  int        `json:"prior_approvals"`
	MaxUses         *int       `json:"max_uses,omitempty"`
	Uses            int        `json:"uses"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	RevokedByUserID string     `json:"revoked_by_user_id,omitempty"`
	RevokeReason    string     `json:"revoke_reason,omitempty"`
}

// Live reports whether the grant would still fire, ignoring max_uses
// exhaustion (which Consume enforces atomically). Used by the API to
// label rows without duplicating the SQL predicate.
func (g TrustGrant) Live(now time.Time) bool {
	switch {
	case g.RevokedAt != nil:
		return false
	case g.ExpiresAt != nil && !g.ExpiresAt.After(now):
		return false
	case g.MaxUses != nil && g.Uses >= *g.MaxUses:
		return false
	}
	return true
}

// GrantInput is what an operator submits when they answer "yes, stop
// asking me" on an inbox card.
type GrantInput struct {
	WorkspaceID     string
	PipelineID      string
	StepID          string
	DefinitionHash  string
	GrantedByUserID string
	Reason          string
	// PriorApprovals records how many manual approvals earned the offer.
	// Stored for the audit trail — "they had seen this 12 times" is the
	// justification for the grant existing.
	PriorApprovals int
	// MaxUses / ExpiresAt are nil = unbounded. Both exist so a cautious
	// operator has something between "forever" and "never".
	MaxUses   *int
	ExpiresAt *time.Time
}

// ErrGrantExists reports a live grant already covering this exact gate
// and definition. Surfaced rather than swallowed so a double-click on
// "always allow" reads as a no-op instead of silently resetting the use
// counter of the existing grant.
var ErrGrantExists = errors.New("pipeline: a live trust grant already covers this gate and definition")

// Grant records a standing approval. Returns the new grant id.
func (s *TrustGrantStore) Grant(ctx context.Context, in GrantInput) (string, error) {
	switch {
	case in.WorkspaceID == "" || in.PipelineID == "" || in.StepID == "":
		return "", errors.New("pipeline: trust grant needs workspace, pipeline and step")
	case in.DefinitionHash == "":
		// Refusing an empty hash is a safety property, not validation
		// hygiene: a grant with no definition bound to it would match
		// every future edit of the routine.
		return "", errors.New("pipeline: trust grant needs the definition hash it is trusting")
	case in.GrantedByUserID == "":
		return "", errors.New("pipeline: trust grant needs an attributable granter")
	case in.MaxUses != nil && *in.MaxUses < 1:
		return "", errors.New("pipeline: trust grant max_uses must be at least 1")
	}

	id := "wtg_" + generateWaitpointToken()
	var expires any
	if in.ExpiresAt != nil {
		// tsformat, because Consume string-compares this column: the
		// stdlib nano layout truncates trailing fractional zeros, so a
		// whole-second expiry sorts after a sub-second instant within
		// that same second and the grant outlives its own expiry.
		expires = tsformat.Format(*in.ExpiresAt)
	}
	var maxUses any
	if in.MaxUses != nil {
		maxUses = *in.MaxUses
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO waitpoint_trust_grants (
    id, workspace_id, pipeline_id, step_id, definition_hash,
    granted_by_user_id, reason, prior_approvals, max_uses, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.WorkspaceID, in.PipelineID, in.StepID, in.DefinitionHash,
		in.GrantedByUserID, nullableStr(in.Reason), in.PriorApprovals, maxUses, expires,
	)
	if err != nil {
		// The partial UNIQUE index on live rows is the only constraint
		// that can collide here.
		if isUniqueViolation(err) {
			return "", ErrGrantExists
		}
		return "", fmt.Errorf("trust grants: insert: %w", err)
	}
	return id, nil
}

// TrustGrantUse is what firing a grant yields: enough to attribute the
// resulting approval to the human who authorised it, without a second
// read.
type TrustGrantUse struct {
	GrantID         string `json:"grant_id"`
	GrantedByUserID string `json:"granted_by_user_id"`
	PriorApprovals  int    `json:"prior_approvals"`
	// Uses is the count INCLUDING this one.
	Uses int `json:"uses"`
}

// Consume fires a grant for a gate that is about to block, incrementing
// its use counter. Returns ok=false when no live grant covers this exact
// (workspace, routine, step, definition) — in which case the caller mints
// a normal blocking approval.
//
// The whole decision is ONE statement. A read-then-update pair would let
// two concurrent runs of the same routine both observe uses=max-1 and
// both fire, turning max_uses into a suggestion; the correlated subquery
// keeps the check and the increment inside a single write, and RETURNING
// hands back the granter so the caller need not read the row again.
func (s *TrustGrantStore) Consume(ctx context.Context, workspaceID, pipelineID, stepID, definitionHash string) (TrustGrantUse, bool, error) {
	if workspaceID == "" || pipelineID == "" || stepID == "" || definitionHash == "" {
		// A run with no recorded definition hash (pre-v114 rows) cannot
		// prove which body it is executing, so it never auto-approves.
		return TrustGrantUse{}, false, nil
	}
	now := tsformat.Format(s.now()) // fixed width; compared against expires_at below

	var use TrustGrantUse
	err := s.db.QueryRowContext(ctx, `
UPDATE waitpoint_trust_grants
   SET uses = uses + 1
 WHERE id = (
       SELECT id FROM waitpoint_trust_grants
        WHERE workspace_id = ? AND pipeline_id = ? AND step_id = ? AND definition_hash = ?
          AND revoked_at IS NULL
          AND (expires_at IS NULL OR expires_at > ?)
          AND (max_uses  IS NULL OR uses < max_uses)
        LIMIT 1)
RETURNING id, granted_by_user_id, prior_approvals, uses`,
		workspaceID, pipelineID, stepID, definitionHash, now,
	).Scan(&use.GrantID, &use.GrantedByUserID, &use.PriorApprovals, &use.Uses)
	if errors.Is(err, sql.ErrNoRows) {
		return TrustGrantUse{}, false, nil
	}
	if err != nil {
		return TrustGrantUse{}, false, fmt.Errorf("trust grants: consume: %w", err)
	}
	return use, true, nil
}

// Revoke retires a grant. Rows are kept, not deleted, so the audit can
// answer who trusted this gate and who took it back. Returns false when
// the id matched nothing live for this routine in this workspace.
//
// pipelineID is part of the predicate rather than trusted from the
// caller's context: grant ids are workspace-unique, so without it a
// request naming routine A could retire a grant belonging to routine B.
// Same reason byUserID is mandatory — a revocation with no actor leaves
// the audit unable to answer the second half of the question it exists
// to answer.
func (s *TrustGrantStore) Revoke(ctx context.Context, workspaceID, pipelineID, grantID, byUserID, reason string) (bool, error) {
	if byUserID == "" {
		return false, errors.New("pipeline: revoking a trust grant needs an attributable actor")
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE waitpoint_trust_grants
   SET revoked_at = ?, revoked_by_user_id = ?, revoke_reason = ?
 WHERE id = ? AND workspace_id = ? AND pipeline_id = ? AND revoked_at IS NULL`,
		tsformat.Format(s.now()), byUserID, nullableStr(reason), grantID, workspaceID, pipelineID)
	if err != nil {
		return false, fmt.Errorf("trust grants: revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("trust grants: revoke rows: %w", err)
	}
	return n > 0, nil
}

// RevokeForPipeline retires every live grant on a routine. Called when a
// routine is disabled or its risk profile is re-reviewed — the operator
// is being asked to look at the routine again, so standing trust in its
// gates should not outlive that review.
func (s *TrustGrantStore) RevokeForPipeline(ctx context.Context, workspaceID, pipelineID, byUserID, reason string) (int, error) {
	if byUserID == "" {
		return 0, errors.New("pipeline: revoking trust grants needs an attributable actor")
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE waitpoint_trust_grants
   SET revoked_at = ?, revoked_by_user_id = ?, revoke_reason = ?
 WHERE workspace_id = ? AND pipeline_id = ? AND revoked_at IS NULL`,
		tsformat.Format(s.now()), byUserID, nullableStr(reason), workspaceID, pipelineID)
	if err != nil {
		return 0, fmt.Errorf("trust grants: revoke for pipeline: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("trust grants: revoke for pipeline rows: %w", err)
	}
	return int(n), nil
}

// List returns grants for a workspace, newest first. A blank pipelineID
// lists the whole workspace. Revoked rows are included so the console can
// show history; callers filter with TrustGrant.Live.
func (s *TrustGrantStore) List(ctx context.Context, workspaceID, pipelineID string) ([]TrustGrant, error) {
	q := `
SELECT id, workspace_id, pipeline_id, step_id, definition_hash,
       granted_by_user_id, granted_at, COALESCE(reason,''), prior_approvals,
       max_uses, uses, expires_at, revoked_at,
       COALESCE(revoked_by_user_id,''), COALESCE(revoke_reason,'')
  FROM waitpoint_trust_grants
 WHERE workspace_id = ?`
	args := []any{workspaceID}
	if pipelineID != "" {
		q += ` AND pipeline_id = ?`
		args = append(args, pipelineID)
	}
	q += ` ORDER BY granted_at DESC, id DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("trust grants: list: %w", err)
	}
	defer rows.Close()

	var out []TrustGrant
	for rows.Next() {
		var g TrustGrant
		var maxUses sql.NullInt64
		var expiresAt, revokedAt sql.NullString
		if err := rows.Scan(&g.ID, &g.WorkspaceID, &g.PipelineID, &g.StepID, &g.DefinitionHash,
			&g.GrantedByUserID, &g.GrantedAt, &g.Reason, &g.PriorApprovals,
			&maxUses, &g.Uses, &expiresAt, &revokedAt, &g.RevokedByUserID, &g.RevokeReason); err != nil {
			return nil, fmt.Errorf("trust grants: scan: %w", err)
		}
		if maxUses.Valid {
			n := int(maxUses.Int64)
			g.MaxUses = &n
		}
		g.ExpiresAt = parseNullableTime(expiresAt)
		g.RevokedAt = parseNullableTime(revokedAt)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trust grants: rows: %w", err)
	}
	return out, nil
}

// PriorApprovals counts how many times a human has approved this exact
// gate on this exact definition. It drives the offer: below the
// threshold the operator is not asked whether to stop being asked.
//
// Only 'approved' counts. A denial is the opposite of a vote of
// confidence, and a timeout means nobody looked — treating either as
// evidence of trust would let a gate nobody watches promote itself.
//
// By the same rule the count excludes the approvals a GRANT wrote. A
// firing grant deliberately writes real `approved` rows so the run
// history stays honest, but they are not evidence that a human looked.
// Counting them would close a loop: grant fires fifty times, operator
// revokes it, and the next card re-offers the shortcut on the strength
// of fifty approvals the grant manufactured — a deliberate revocation
// answered with a stronger nag.
//
// json_valid guards the extraction because the manual path stores the
// approver's free-text comment in decision_payload, and json_extract
// errors on input that is not JSON.
func (s *TrustGrantStore) PriorApprovals(ctx context.Context, workspaceID, pipelineID, stepID, definitionHash string) (int, error) {
	if definitionHash == "" {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM pipeline_waitpoints w
  JOIN pipeline_runs r ON r.id = w.pipeline_run_id
 WHERE w.workspace_id = ? AND w.step_id = ? AND w.status = 'approved'
   AND r.pipeline_id = ? AND r.definition_hash = ?
   AND NOT (json_valid(COALESCE(w.decision_payload, ''))
            AND COALESCE(json_extract(w.decision_payload, '$.auto_approved'), 0) = 1)`,
		workspaceID, stepID, pipelineID, definitionHash).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("trust grants: prior approvals: %w", err)
	}
	return n, nil
}

// parseNullableTime tolerates both the RFC 3339 family (tsformat's
// fixed-width output parses as RFC3339Nano unchanged) and SQLite's
// datetime() shape, which is what the granted_at DEFAULT writes.
// Returns nil when the column is NULL or unparseable — an unreadable
// timestamp must not read as "not revoked".
func parseNullableTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s.String); err == nil {
			u := t.UTC()
			return &u
		}
	}
	// Unparseable but present: return a zero time so callers treat the
	// grant as revoked/expired rather than live. Failing closed.
	zero := time.Time{}
	return &zero
}
