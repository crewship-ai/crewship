package providerpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

var ErrNotFound = errors.New("provider pool not found")

// Pool is a named set, not a permission grant. A binding layer must separately
// authorize an agent before asking this store to select a paying account.
type Pool struct {
	ID          string
	WorkspaceID string
	Name        string
	CreatedBy   string
	Policy      Policy
	Members     []Member
}

type Member struct {
	CredentialID string
	Priority     int
}

type Store struct{ db *sql.DB }

// NewStore uses the application's database.Open handle: foreign keys, WAL,
// busy timeout and immediate write transactions are part of its contract.
// Read-only snapshots explicitly request a deferred/read-only transaction.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Create is atomic, including member validation. Role authorization and audit
// belong to the calling API layer; tenant checks also live here and in SQL.
func (s *Store) Create(ctx context.Context, pool Pool) error {
	if pool.ID == "" || pool.WorkspaceID == "" || pool.CreatedBy == "" ||
		strings.TrimSpace(pool.Name) == "" || utf8.RuneCountInString(pool.Name) > 200 ||
		!providerlogin.IsProvider(pool.Policy.Provider) || !providerlogin.ValidMode(pool.Policy.Mode) ||
		len(pool.Members) == 0 || len(pool.Members) > 100 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_login_pools
		(id, workspace_id, name, provider, mode, allow_cross_owner, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, pool.ID, pool.WorkspaceID, strings.TrimSpace(pool.Name),
		providerlogin.Canonical(pool.Policy.Provider), pool.Policy.Mode, pool.Policy.AllowCrossOwner, pool.CreatedBy)
	if err != nil {
		return fmt.Errorf("create provider pool: %w", err)
	}
	for _, member := range pool.Members {
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_login_pool_members (pool_id, credential_id, priority)
			VALUES (?, ?, ?)`, pool.ID, member.CredentialID, member.Priority)
		if err != nil {
			return fmt.Errorf("add provider pool member: %w", err)
		}
	}
	candidates, err := loadCandidates(ctx, tx, pool.WorkspaceID, pool.ID)
	if err != nil {
		return err
	}
	// An unavailable account may be configured before re-login; malformed or
	// mixed-owner membership must still fail. Select validates the entire set.
	_, err = Select(pool.Policy, candidates, time.Now())
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return err
	}
	return tx.Commit()
}

// Choose reserves a round-robin turn. Its first statement takes SQLite's write
// lock, avoiding the read-snapshot upgrade race between simultaneous starts.
// Refresh/network calls must happen BEFORE this transaction. Failure rolls
// back both the sequence and selection; a metadata read never calls Choose.
func (s *Store) Choose(ctx context.Context, workspaceID, poolID string, now time.Time) (Candidate, error) {
	if workspaceID == "" || poolID == "" || now.IsZero() {
		return Candidate{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Candidate{}, err
	}
	defer tx.Rollback()
	var policy Policy
	var seq int64
	err = tx.QueryRowContext(ctx, `UPDATE provider_login_pools SET selection_seq = selection_seq + 1
		WHERE id = ? AND workspace_id = ? AND selection_seq < ?
		RETURNING provider, mode, allow_cross_owner, selection_seq`, poolID, workspaceID, int64(math.MaxInt64)).
		Scan(&policy.Provider, &policy.Mode, &policy.AllowCrossOwner, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, ErrNotFound
	}
	if err != nil {
		return Candidate{}, err
	}
	candidates, err := loadCandidates(ctx, tx, workspaceID, poolID)
	if err != nil {
		return Candidate{}, err
	}
	chosen, err := Select(policy, candidates, now)
	if err != nil {
		return Candidate{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE provider_login_pool_members SET last_selected_seq = ?
		WHERE pool_id = ? AND credential_id = ?`, seq, poolID, chosen.ID)
	if err != nil {
		return Candidate{}, err
	}
	if err = tx.Commit(); err != nil {
		return Candidate{}, err
	}
	chosen.LastSelected = seq
	return chosen, nil
}

// Snapshot reads eligibility metadata without consuming a round-robin turn.
// It never exposes stored values. Callers must not label it a successful
// upstream connection test or an account already selected for a running job.
func (s *Store) Snapshot(ctx context.Context, workspaceID, poolID string) (Policy, []Candidate, error) {
	if workspaceID == "" || poolID == "" {
		return Policy{}, nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Policy{}, nil, err
	}
	defer tx.Rollback()
	var policy Policy
	err = tx.QueryRowContext(ctx, `SELECT provider, mode, allow_cross_owner FROM provider_login_pools
		WHERE id = ? AND workspace_id = ?`, poolID, workspaceID).
		Scan(&policy.Provider, &policy.Mode, &policy.AllowCrossOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return Policy{}, nil, ErrNotFound
	}
	if err != nil {
		return Policy{}, nil, err
	}
	candidates, err := loadCandidates(ctx, tx, workspaceID, poolID)
	if err != nil {
		return Policy{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return Policy{}, nil, err
	}
	return policy, candidates, nil
}

func loadCandidates(ctx context.Context, tx *sql.Tx, workspaceID, poolID string) ([]Candidate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.id, COALESCE(c.created_by, ''), c.provider,
		COALESCE(mode.value, ''), m.priority, m.last_selected_seq,
		(c.status = 'ACTIVE' AND c.deleted_at IS NULL),
		(COALESCE(r.status, '') = 'needs_relogin' OR a.blocked_reason IS NOT NULL),
		COALESCE(NULLIF(exp.value, ''), c.token_expires_at, ''), COALESCE(a.cooldown_until, ''), c.type,
		c.workspace_id
		FROM provider_login_pool_members m
		JOIN provider_login_pools p ON p.id = m.pool_id
		JOIN credentials c ON c.id = m.credential_id
		LEFT JOIN credential_fields mode ON mode.credential_id = c.id AND mode.key = 'mode' AND mode.is_secret = 0
		LEFT JOIN credential_fields exp ON exp.credential_id = c.id AND exp.key = 'expires_at' AND exp.is_secret = 0
		LEFT JOIN provider_login_refresh r ON r.credential_id = c.id
		LEFT JOIN provider_login_availability a ON a.credential_id = c.id
		WHERE p.id = ? AND p.workspace_id = ? ORDER BY m.credential_id`, poolID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		var expires, cooldown, kind, credentialWorkspace string
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.Provider, &c.Mode, &c.Priority, &c.LastSelected,
			&c.Active, &c.Blocked, &expires, &cooldown, &kind, &credentialWorkspace); err != nil {
			return nil, err
		}
		// Legacy subscription blobs can hide expiry inside encrypted auth.json.
		// Require import as PROVIDER_LOGIN before pooling them: this metadata-
		// only store must not interpret an unknown legacy expiry as valid.
		if credentialWorkspace != workspaceID || (kind != "PROVIDER_LOGIN" && kind != "API_KEY") {
			return nil, ErrInvalid
		}
		if kind == "API_KEY" {
			c.Mode = providerlogin.ModeAPIKey
		}
		if expires != "" {
			c.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
			if err != nil {
				return nil, ErrInvalid
			}
		}
		if cooldown != "" {
			c.CooldownUntil, err = time.Parse(time.RFC3339Nano, cooldown)
			if err != nil {
				return nil, ErrInvalid
			}
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}
