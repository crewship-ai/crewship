package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/diskusage"
)

// Ingress capacity, from §6.
//
// Limits is about EXECUTION slots — how many things may run at once. This file
// is about the other cap in §6, which is about how much accepted-but-unfinished
// work a workspace may be holding at all. The two are unrelated numbers with
// unrelated failure modes: exceeding the first means a slow queue, exceeding
// the second means a database growing without bound because a broken sender
// keeps posting and nothing ever drains.
//
// # Over the limit the answer is 429, and nothing is taken
//
// Not a 202 followed by a quiet drop, and not a 500. §6 is explicit: `429` with
// `Retry-After: 5`, and nothing new accepted. Each limit has its own error so a
// handler can say which one, because "we are full" without a reason is a
// support ticket rather than a fix.
//
// # A duplicate is never refused for fullness
//
// This is the case that is easy to get backwards, and getting it backwards is
// worse than having no limit at all. A sender that did not hear our 202 retries
// the SAME delivery. If fullness refuses that retry, we have told the sender to
// give up on work we are already holding — the sender eventually stops, the
// work runs, and nobody upstream believes it did. So the duplicate check comes
// FIRST, before any counting, and a delivery we already have is admitted no
// matter how full we are. It creates nothing new; it returns the receipt that
// already exists.
//
// Accepted work is likewise never evicted to make room. Under pressure the
// answer is always to refuse the new arrival, never to drop something we
// already promised to do.

// Ingress errors. Each names the limit it hit, so a handler can answer 429 with
// a reason rather than a generic refusal.
var (
	// ErrEndpointFull means this endpoint already holds its maximum of
	// non-terminal work.
	ErrEndpointFull = errors.New("work: endpoint ingress capacity full")
	// ErrWorkspaceFull means the workspace already holds its maximum of
	// non-terminal work across all endpoints.
	ErrWorkspaceFull = errors.New("work: workspace ingress capacity full")
	// ErrWorkspaceBytesFull means the workspace's non-terminal raw input would
	// exceed its shared byte budget.
	ErrWorkspaceBytesFull = errors.New("work: workspace raw input capacity full")
	// ErrDiskPressure means the volume behind the database is too short of space
	// to take on new work. §6: refuse new acceptance — and never delete a
	// non-terminal payload to make room.
	ErrDiskPressure = errors.New("work: insufficient disk for new acceptance")
)

// IngressRetryAfter is §6's `Retry-After` for a capacity refusal.
const IngressRetryAfter = 5 * time.Second

// IsIngressFull reports whether err is one of the capacity refusals — the set a
// handler answers with 429 and Retry-After.
//
// ErrDiskPressure is deliberately in this set. From the sender's side it is the
// same fact: we are not taking new work right now, come back. The distinction
// belongs in our logs, not in a different status code that would invite a
// sender to give up.
func IsIngressFull(err error) bool {
	return errors.Is(err, ErrEndpointFull) ||
		errors.Is(err, ErrWorkspaceFull) ||
		errors.Is(err, ErrWorkspaceBytesFull) ||
		errors.Is(err, ErrDiskPressure)
}

// §6's default ingress profile. Higher limits must be configured explicitly.
const (
	// DefaultEndpointNonTerminal is 2 000 non-terminal work items per endpoint.
	DefaultEndpointNonTerminal = 2000
	// DefaultWorkspaceNonTerminal is 10 000 non-terminal work items per
	// workspace.
	DefaultWorkspaceNonTerminal = 10000
	// DefaultWorkspaceRawBytes is the shared 256 MiB of raw input across a
	// workspace's non-terminal work.
	DefaultWorkspaceRawBytes int64 = 256 << 20
	// DefaultMinFreeDiskBytes is the headroom below which new acceptance is
	// refused. It is one workspace byte budget plus room for the WAL to grow and
	// be checkpointed, because a database that cannot checkpoint cannot make
	// progress on the work it already holds.
	DefaultMinFreeDiskBytes int64 = 1 << 30
)

// IngressLimits is the per-arrival admission contract. It stays comparable —
// no function fields — so a caller can test it against the zero value the way
// claim.go does with Limits.
type IngressLimits struct {
	// EndpointNonTerminal caps non-terminal work per endpoint.
	EndpointNonTerminal int
	// WorkspaceNonTerminal caps non-terminal work per workspace.
	WorkspaceNonTerminal int
	// WorkspaceRawBytes caps the raw input bytes of a workspace's non-terminal
	// work. Terminal payloads do NOT count here — see WorkspaceUsage for why
	// they are still reported.
	WorkspaceRawBytes int64
}

// DefaultIngressLimits returns §6's numbers.
func DefaultIngressLimits() IngressLimits {
	return IngressLimits{
		EndpointNonTerminal:  DefaultEndpointNonTerminal,
		WorkspaceNonTerminal: DefaultWorkspaceNonTerminal,
		WorkspaceRawBytes:    DefaultWorkspaceRawBytes,
	}
}

func (l IngressLimits) normalized() IngressLimits {
	if l.EndpointNonTerminal <= 0 {
		l.EndpointNonTerminal = DefaultEndpointNonTerminal
	}
	if l.WorkspaceNonTerminal <= 0 {
		l.WorkspaceNonTerminal = DefaultWorkspaceNonTerminal
	}
	if l.WorkspaceRawBytes <= 0 {
		l.WorkspaceRawBytes = DefaultWorkspaceRawBytes
	}
	return l
}

// sqlNonTerminal is the non-terminal state set, derived from State.Terminal()
// instead of being typed out for a fourth time beside sqlHoldsExecutionSlot and
// sqlOccupiesSession. The counts below and the retention guards in retention.go
// are exact complements by construction, which is the property that makes
// "terminal payloads do not count toward the byte limit" true rather than
// approximately true.
var sqlNonTerminal = stateList(func(s State) bool { return !s.Terminal() })

// IngressRequest is one arrival being admitted, before anything is written.
type IngressRequest struct {
	WorkspaceID string
	EndpointID  string
	// SourceDeliveryID and ContentKey are the delivery's identity. They are here
	// so the duplicate check can run before the limits — a re-delivery of work
	// we already hold is admitted however full we are.
	SourceDeliveryID string
	ContentKey       string
	// BodyBytes is the size of the raw input this arrival would add.
	BodyBytes int64
}

// CheckIngressTx admits or refuses one arrival, inside the caller's acceptance
// transaction.
//
// It belongs in the same transaction as the insert it guards. Counting outside
// it would let two concurrent arrivals both see room for the last slot — the
// same check-then-act race the claim path avoids, for the same reason. §6 says
// capacity is checked atomically.
//
// The counts are three single-row queries against the non-terminal set, not
// scans of the delivery history: the non-terminal population is what the limits
// bound, so the work each query touches is bounded by the limit itself rather
// than by how long the workspace has existed.
func (s *Store) CheckIngressTx(ctx context.Context, tx *sql.Tx, lim IngressLimits, req IngressRequest) error {
	lim = lim.normalized()

	// The duplicate gate, FIRST and unconditionally. Refusing a retry of work we
	// already hold is how a sender is driven to abandon work that exists.
	if req.SourceDeliveryID != "" {
		// A zero BodySHA256 means "identity lookup only": lookupDeliveryTx must
		// not be asked to compare a body we did not supply.
		existing, err := s.lookupDeliveryTx(ctx, tx, Delivery{
			WorkspaceID:      req.WorkspaceID,
			EndpointID:       req.EndpointID,
			SourceDeliveryID: req.SourceDeliveryID,
			ContentKey:       req.ContentKey,
		})
		if err != nil {
			return err
		}
		if existing != nil {
			return nil
		}
	}

	endpointCount, err := s.countEndpointNonTerminalTx(ctx, tx, req.WorkspaceID, req.EndpointID)
	if err != nil {
		return err
	}
	if endpointCount >= lim.EndpointNonTerminal {
		return fmt.Errorf("%w: endpoint %s holds %d of %d non-terminal work items",
			ErrEndpointFull, req.EndpointID, endpointCount, lim.EndpointNonTerminal)
	}

	wsCount, wsBytes, err := s.countWorkspaceNonTerminalTx(ctx, tx, req.WorkspaceID)
	if err != nil {
		return err
	}
	if wsCount >= lim.WorkspaceNonTerminal {
		return fmt.Errorf("%w: workspace %s holds %d of %d non-terminal work items",
			ErrWorkspaceFull, req.WorkspaceID, wsCount, lim.WorkspaceNonTerminal)
	}
	if wsBytes+req.BodyBytes > lim.WorkspaceRawBytes {
		return fmt.Errorf("%w: workspace %s holds %d bytes of %d, arrival adds %d",
			ErrWorkspaceBytesFull, req.WorkspaceID, wsBytes, lim.WorkspaceRawBytes, req.BodyBytes)
	}
	return nil
}

// countEndpointNonTerminalTx counts work items this endpoint produced that have
// not finished.
//
// It is driven from the delivery side, over the (workspace_id, endpoint_id)
// prefix of the ledger's unique index, and joins to the work item by primary
// key. The alternative — walking the workspace's work items and asking which
// endpoint each came from — has no index for the endpoint term at all.
func (s *Store) countEndpointNonTerminalTx(ctx context.Context, tx *sql.Tx, workspaceID, endpointID string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM webhook_deliveries d
		JOIN work_items w ON w.id = d.work_id
		WHERE d.workspace_id = ? AND d.endpoint_id = ?
		  AND w.state IN (`+sqlNonTerminal+`)`, workspaceID, endpointID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("work: count endpoint ingress: %w", err)
	}
	return n, nil
}

// countWorkspaceNonTerminalTx returns the workspace's non-terminal work count
// and the raw input bytes those items are holding.
//
// The count is over work_items, so work accepted through a path that is not a
// webhook still counts against the workspace cap. The bytes are over the
// deliveries that fed them, because raw input is the thing being bounded and
// only a delivery has any.
func (s *Store) countWorkspaceNonTerminalTx(ctx context.Context, tx *sql.Tx, workspaceID string) (int, int64, error) {
	var n int
	var bytes int64
	err := tx.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM work_items w
		     WHERE w.workspace_id = ? AND w.state IN (`+sqlNonTerminal+`)),
		  (SELECT COALESCE(SUM(d.body_bytes), 0) FROM webhook_deliveries d
		     JOIN work_items w2 ON w2.id = d.work_id
		     WHERE d.workspace_id = ? AND w2.state IN (`+sqlNonTerminal+`))`,
		workspaceID, workspaceID).Scan(&n, &bytes)
	if err != nil {
		return 0, 0, fmt.Errorf("work: count workspace ingress: %w", err)
	}
	return n, bytes, nil
}

// DiskGuard refuses new acceptance when the volume behind the database is
// short of space.
//
// §6 pairs this with the rule that gives it teeth: under disk pressure we
// refuse new work, and we do NOT delete a non-terminal payload to make room.
// Freeing space by discarding work we already accepted trades a visible refusal
// for a silent loss, which is the worse of the two every time.
type DiskGuard struct {
	// Path is any path on the volume to watch — the database file's directory.
	Path string
	// MinFree is the headroom below which acceptance is refused.
	MinFree int64

	// probe is the injection point. Tests set it to model a full volume; a
	// disk-pressure test that actually fills a disk is a test nobody runs twice.
	probe func(string) (diskusage.Stats, error)
}

// NewDiskGuard watches the volume holding path, refusing acceptance below
// DefaultMinFreeDiskBytes.
func NewDiskGuard(path string) *DiskGuard {
	return &DiskGuard{Path: path, MinFree: DefaultMinFreeDiskBytes, probe: diskusage.Usage}
}

// Check reports whether there is room to take on new work.
//
// A probe that fails is NOT treated as pressure. Refusing every arrival because
// statfs is unavailable — on a platform diskusage does not support, say — would
// take the ingress path down for a reason that has nothing to do with disk.
func (g *DiskGuard) Check() error {
	if g == nil || g.Path == "" {
		return nil
	}
	probe := g.probe
	if probe == nil {
		probe = diskusage.Usage
	}
	st, err := probe(g.Path)
	if err != nil {
		return nil
	}
	min := g.MinFree
	if min <= 0 {
		min = DefaultMinFreeDiskBytes
	}
	if int64(st.FreeBytes) < min {
		return fmt.Errorf("%w: %d bytes free on %s, need %d",
			ErrDiskPressure, st.FreeBytes, g.Path, min)
	}
	return nil
}

// WorkspaceUsage is the capacity report for one workspace: what it is holding
// against each limit, and what its finished work still costs on disk.
type WorkspaceUsage struct {
	WorkspaceID string

	// NonTerminalItems and NonTerminalBytes are what the ingress limits bound.
	NonTerminalItems int
	NonTerminalBytes int64

	// TerminalPayloadBytes is raw input still stored for work that has already
	// finished. §6 excludes it from the ingress byte limit — a workspace must
	// not be locked out of new work by its own history — but requires it to be
	// counted in the capacity report, because it is real disk either way. The
	// contract does the arithmetic as 36 000 × 64 KiB ≈ 2.2 GiB before indexes
	// and WAL; ProjectedTerminalPayloadBytes is that sum, so the number can be
	// asked for rather than left to a spreadsheet.
	TerminalPayloadBytes int64

	// ExpiredPayloads is how many finished deliveries have already had their
	// payload swept. Their replay is unavailable, and the UI has to say so.
	ExpiredPayloads int

	Limits IngressLimits
}

// Remaining reports the headroom under each limit, floored at zero.
func (u WorkspaceUsage) Remaining() (items int, bytes int64) {
	items = u.Limits.WorkspaceNonTerminal - u.NonTerminalItems
	if items < 0 {
		items = 0
	}
	bytes = u.Limits.WorkspaceRawBytes - u.NonTerminalBytes
	if bytes < 0 {
		bytes = 0
	}
	return items, bytes
}

// WorkspaceUsage returns the capacity report for one workspace.
func (s *Store) WorkspaceUsage(ctx context.Context, workspaceID string, lim IngressLimits) (WorkspaceUsage, error) {
	lim = lim.normalized()
	u := WorkspaceUsage{WorkspaceID: workspaceID, Limits: lim}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return u, fmt.Errorf("work: begin usage report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	u.NonTerminalItems, u.NonTerminalBytes, err = s.countWorkspaceNonTerminalTx(ctx, tx, workspaceID)
	if err != nil {
		return u, err
	}

	// body_bytes rather than LENGTH(raw_body): the two agree, and only one of
	// them makes SQLite read every stored payload to answer.
	err = tx.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN d.raw_body IS NOT NULL THEN d.body_bytes ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN d.raw_body IS NULL AND d.raw_body_expires_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM webhook_deliveries d
		LEFT JOIN work_items w ON w.id = d.work_id
		WHERE d.workspace_id = ?
		  AND (d.work_id IS NULL OR w.state IN (`+sqlTerminal+`))`,
		workspaceID).Scan(&u.TerminalPayloadBytes, &u.ExpiredPayloads)
	if err != nil {
		return u, fmt.Errorf("work: count terminal payload bytes: %w", err)
	}
	return u, nil
}

// UsageReport returns the capacity report for every workspace that holds a
// delivery or a work item, newest workspace id order, so an operator can ask
// "where is the disk going" without knowing which workspace to ask about.
func (s *Store) UsageReport(ctx context.Context, lim IngressLimits) ([]WorkspaceUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workspace_id FROM work_items
		UNION
		SELECT workspace_id FROM webhook_deliveries
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("work: list workspaces for usage: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("work: scan workspace for usage: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("work: list workspaces for usage: %w", err)
	}
	rows.Close()

	out := make([]WorkspaceUsage, 0, len(ids))
	for _, id := range ids {
		u, err := s.WorkspaceUsage(ctx, id, lim)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// §6's disk arithmetic for retained terminal payloads, made askable.
const (
	// ReferenceTerminalDeliveries is the retained-delivery count §6 sizes the
	// reference deployment against.
	ReferenceTerminalDeliveries int64 = 36000
	// ReferenceBodyBytes is §6's assumed average body: 64 KiB.
	ReferenceBodyBytes int64 = 64 << 10
)

// ProjectedTerminalPayloadBytes is what a given number of retained payloads of
// a given average size will cost on disk, BEFORE indexes and WAL.
//
// It exists so the sizing question has an answer in the code rather than in
// someone's spreadsheet. Called with §6's own inputs it reproduces §6's own
// figure: 36 000 × 64 KiB ≈ 2.2 GiB.
func ProjectedTerminalPayloadBytes(deliveries, avgBodyBytes int64) int64 {
	if deliveries <= 0 || avgBodyBytes <= 0 {
		return 0
	}
	return deliveries * avgBodyBytes
}

// ReferenceTerminalPayloadBytes is ProjectedTerminalPayloadBytes applied to
// §6's reference profile.
func ReferenceTerminalPayloadBytes() int64 {
	return ProjectedTerminalPayloadBytes(ReferenceTerminalDeliveries, ReferenceBodyBytes)
}
