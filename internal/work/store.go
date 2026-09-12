package work

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Store owns the work ledger. Time and randomness are injected because §4
// requires the retry clock and the backoff jitter to be controllable in tests,
// and §11 forbids sleep-based races.
type Store struct {
	db            *sql.DB
	now           func() time.Time
	rnd           func(int64) int64
	idFn          func() string
	ingressLimits IngressLimits
}

// NewStore returns a Store over db with production time and randomness.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now, rnd: cryptoRandInt63n, idFn: newID}
}

// WithClock replaces the time source. Test-only in practice, but not gated on a
// build tag: a caller that needs a skewed clock in production is a caller with a
// bug to find, not a compile error to route around.
func (s *Store) WithClock(now func() time.Time) *Store {
	c := *s
	c.now = now
	return &c
}

// WithRand replaces the jitter source. rnd(n) must return a value in [0, n).
func (s *Store) WithRand(rnd func(int64) int64) *Store {
	c := *s
	c.rnd = rnd
	return &c
}

// WithIDs replaces the identifier generator, so a test can assert on stable ids.
func (s *Store) WithIDs(idFn func() string) *Store {
	c := *s
	c.idFn = idFn
	return &c
}

func cryptoRandInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		// A failing CSPRNG must not silently collapse the jitter to zero, which
		// is exactly the thundering herd the jitter exists to prevent. Half the
		// window is a poor spread but a bounded one.
		return n / 2
	}
	return v.Int64()
}

var idCounter atomic.Uint64

// newID mints a CUID in the same shape as internal/api's generateCUID, so ids
// minted here are indistinguishable from ids minted there. The layout is
// "c" + base36(unix millis) + 4 hex counter chars + 8 hex random chars.
func newID() string {
	ts := time.Now().UnixMilli()
	c := idCounter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b[0] = byte(c >> 24)
		b[1] = byte(c >> 16)
		b[2] = byte(ts >> 8)
		b[3] = byte(ts)
	}
	var sb strings.Builder
	sb.WriteByte('c')
	sb.WriteString(strconv.FormatInt(ts, 36))
	const hexdigits = "0123456789abcdef"
	tail := c % 65536
	sb.WriteByte(hexdigits[(tail>>12)&0xF])
	sb.WriteByte(hexdigits[(tail>>8)&0xF])
	sb.WriteByte(hexdigits[(tail>>4)&0xF])
	sb.WriteByte(hexdigits[tail&0xF])
	sb.WriteString(hex.EncodeToString(b))
	return sb.String()
}

// AcceptRequest is one unit of work being taken responsibility for. Everything
// needed to run it later must be here: after the commit, the producer's process
// may be gone.
type AcceptRequest struct {
	WorkspaceID string
	Source      Source
	SourceRef   string
	DomainKind  string
	DomainID    string

	AgentID   string
	CrewID    string
	SessionID string
	Class     Class

	AuthorizedByUserID string
	AuthorizedScope    string

	InputJSON      string
	InputSHA256    string
	TargetRevision string

	Priority   int
	EligibleAt time.Time // zero = immediately
	DeadlineAt time.Time // zero = never expires on its own

	ReplayOf     string
	ReplayReason string
}

func (r AcceptRequest) validate() error {
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return errors.New("work: accept requires a workspace")
	}
	if r.Source == "" {
		return errors.New("work: accept requires a source")
	}
	if !r.Class.valid() {
		return fmt.Errorf("work: invalid class %q", r.Class)
	}
	if r.ReplayReason != "" && r.ReplayOf == "" {
		return errors.New("work: replay reason without replay_of")
	}
	return nil
}

// Receipt is what a producer gets back and what an accepted webhook returns. It
// is an identifier, not a bearer capability: reading the work behind it still
// needs workspace-scoped authorization (§5).
type Receipt struct {
	WorkID     string
	DeliveryID string
	State      State
	Duplicate  bool
}

// AcceptTx records the work item inside the CALLER's transaction, so the domain
// row and the queued work commit together or not at all (I1). It deliberately
// takes a *sql.Tx rather than opening its own: an acceptance path that commits
// the work separately from the thing it is work for has a window where a crash
// loses one of them, which is finding W3 in the audit.
//
// Nothing outside the database happens here. No container is warmed, no
// goroutine is started; that is finding W2, and it is why the agent webhook can
// blow a provider's response deadline today.
func (s *Store) AcceptTx(ctx context.Context, tx *sql.Tx, req AcceptRequest) (Receipt, error) {
	if err := req.validate(); err != nil {
		return Receipt{}, err
	}
	now := s.now().UTC()
	eligible := req.EligibleAt
	if eligible.IsZero() {
		eligible = now
	}
	id := s.idFn()

	var deadline any
	if !req.DeadlineAt.IsZero() {
		deadline = tsformat.Format(req.DeadlineAt)
	}
	var replayOf any
	if req.ReplayOf != "" {
		replayOf = req.ReplayOf
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_items (
			id, workspace_id, source, source_ref, domain_kind, domain_id,
			agent_id, crew_id, session_id, class,
			authorized_by_user_id, authorized_scope,
			input_json, input_sha256, target_revision,
			state, generation, attempts, priority, eligible_at, deadline_at,
			replay_of, replay_reason, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,?,?,?,?,?,?,?)`,
		id, req.WorkspaceID, string(req.Source), req.SourceRef, req.DomainKind, req.DomainID,
		req.AgentID, req.CrewID, req.SessionID, string(req.Class),
		req.AuthorizedByUserID, req.AuthorizedScope,
		defaultJSON(req.InputJSON), req.InputSHA256, req.TargetRevision,
		string(StateQueued), req.Priority, tsformat.Format(eligible), deadline,
		replayOf, req.ReplayReason, tsformat.Format(now), tsformat.Format(now),
	); err != nil {
		return Receipt{}, fmt.Errorf("work: insert work item: %w", err)
	}

	if err := appendEventTx(ctx, tx, id, "", StateQueued, "", 0, "accepted", now); err != nil {
		return Receipt{}, err
	}
	return Receipt{WorkID: id, State: StateQueued}, nil
}

func defaultJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

// appendEventTx writes the next event for a work item. The sequence is derived
// inside the same transaction that changes the state, so authoritative state
// and its event are always committed together (§3).
func appendEventTx(ctx context.Context, tx *sql.Tx, workID, from string, to State, runID string, generation int64, reason string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO work_events (work_id, seq, at, from_state, to_state, run_id, generation, reason)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM work_events WHERE work_id = ?), ?, ?, ?, ?, ?, ?)`,
		workID, workID, tsformat.Format(at), from, string(to), runID, generation, reason)
	if err != nil {
		return fmt.Errorf("work: append event: %w", err)
	}
	return nil
}

// Item is a work item as stored.
type Item struct {
	ID          string
	WorkspaceID string
	Source      Source
	SourceRef   string
	DomainKind  string
	DomainID    string
	AgentID     string
	CrewID      string
	SessionID   string
	Class       Class

	AuthorizedByUserID string
	AuthorizedScope    string

	InputJSON      string
	InputSHA256    string
	TargetRevision string

	State       State
	StateReason string
	Generation  int64
	Attempts    int

	Priority   int
	EligibleAt time.Time
	DeadlineAt *time.Time
	ReplayOf   string

	CreatedAt  time.Time
	UpdatedAt  time.Time
	TerminalAt *time.Time
}

const itemColumns = `id, workspace_id, source, source_ref, domain_kind, domain_id,
	agent_id, crew_id, session_id, class, authorized_by_user_id, authorized_scope,
	input_json, input_sha256, target_revision, state, state_reason, generation, attempts,
	priority, eligible_at, deadline_at, replay_of, created_at, updated_at, terminal_at`

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	var source, class, state string
	var deadline, terminal, replayOf sql.NullString
	var eligible, created, updated string
	if err := row.Scan(
		&it.ID, &it.WorkspaceID, &source, &it.SourceRef, &it.DomainKind, &it.DomainID,
		&it.AgentID, &it.CrewID, &it.SessionID, &class, &it.AuthorizedByUserID, &it.AuthorizedScope,
		&it.InputJSON, &it.InputSHA256, &it.TargetRevision, &state, &it.StateReason, &it.Generation, &it.Attempts,
		&it.Priority, &eligible, &deadline, &replayOf, &created, &updated, &terminal,
	); err != nil {
		return nil, err
	}
	it.Source, it.Class, it.State = Source(source), Class(class), State(state)
	it.ReplayOf = replayOf.String
	it.EligibleAt = parseTime(eligible)
	it.CreatedAt = parseTime(created)
	it.UpdatedAt = parseTime(updated)
	if deadline.Valid {
		t := parseTime(deadline.String)
		it.DeadlineAt = &t
	}
	if terminal.Valid {
		t := parseTime(terminal.String)
		it.TerminalAt = &t
	}
	return &it, nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Get returns one work item.
func (s *Store) Get(ctx context.Context, workID string) (*Item, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM work_items WHERE id = ?`, workID)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

// WithIngressLimits configures acceptance capacity; zero values use defaults.
func (s *Store) WithIngressLimits(limits IngressLimits) *Store {
	c := *s
	c.ingressLimits = limits
	return &c
}
