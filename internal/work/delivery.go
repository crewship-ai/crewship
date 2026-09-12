package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Delivery is one inbound webhook, recorded before anything is dispatched.
//
// The ledger exists because today the only record of a delivery is four columns
// on the endpoint row — last fired, last status, last run, a counter — which
// cannot answer "did you receive this one", "what happened to it", or "show me
// the payload again". A delivery is never deleted on failure: a retry becomes a
// new attempt of the same work, and a manual replay a new work item.
type Delivery struct {
	ID          string
	WorkspaceID string
	EndpointID  string
	// EndpointKind is which surface accepted it: agent, routine or page. The
	// three have different legacy contracts and must stay distinguishable.
	EndpointKind string
	// Profile is the signature profile that verified it — github,
	// standard-webhooks, or one of the legacy adapters. It is recorded rather
	// than inferred, because §5 forbids an endpoint switching profile
	// heuristically on the presence of a header.
	Profile string

	// SourceDeliveryID is the sender's own identifier. Identity is
	// (workspace, endpoint, source id) — never the signature, which changes
	// with a fresh timestamp for the same logical delivery.
	SourceDeliveryID string
	// ContentKey suppresses byte-identical replays on profiles whose signature
	// does not cover a delivery id. Empty means the profile has real identity
	// and needs no content fallback.
	ContentKey string

	BodySHA256 string
	BodyBytes  int
	RawBody    []byte
	// RawBodyExpiresAt is when the payload may be dropped. After that a manual
	// replay is unavailable and the UI has to say so rather than offering a
	// button that cannot work.
	RawBodyExpiresAt time.Time

	EventType    string
	EventAction  string
	SigningKeyID string

	// FilterDecision is "accepted" or "ignored". An ignored delivery is still
	// recorded: §5 requires the decision and its reason to be recoverable, so a
	// ping or a filtered event is auditable rather than invisible.
	FilterDecision string
	FilterReason   string
	TargetRevision string

	ReceivedAt     time.Time
	DedupExpiresAt time.Time
}

const (
	FilterAccepted = "accepted"
	FilterIgnored  = "ignored"
)

// DefaultDedupWindow is §6's: dedup keys and receipts live at least 30 days from
// acceptance, and for the whole non-terminal life of the work.
const DefaultDedupWindow = 30 * 24 * time.Hour

// AcceptDeliveryTx records a delivery and, when the filter accepted it, the work
// it produced — in ONE transaction, inside the caller's.
//
// This is invariant I1 and I2 in one function. Either both rows exist or
// neither does, so there is no window in which a sender has been told "accepted"
// about work that does not exist, and no window in which work exists that no
// delivery explains.
//
// Re-delivery is the normal case, not an error: a provider that did not hear the
// response will send the same delivery again. It gets the ORIGINAL receipt back
// with Duplicate set, and no second attempt is created merely because the
// message arrived twice.
func (s *Store) AcceptDeliveryTx(ctx context.Context, tx *sql.Tx, d Delivery, w AcceptRequest) (Receipt, error) {
	if strings.TrimSpace(d.WorkspaceID) == "" || strings.TrimSpace(d.EndpointID) == "" {
		return Receipt{}, errors.New("work: delivery requires a workspace and an endpoint")
	}
	if strings.TrimSpace(d.SourceDeliveryID) == "" {
		return Receipt{}, errors.New("work: delivery requires a source delivery id")
	}
	switch d.FilterDecision {
	case FilterAccepted, FilterIgnored:
	default:
		return Receipt{}, fmt.Errorf("work: unknown filter decision %q", d.FilterDecision)
	}

	// An existing record settles it before anything is written. Checked first so
	// the common re-delivery does no work and takes no locks it does not need.
	if existing, err := s.lookupDeliveryTx(ctx, tx, d); err != nil {
		return Receipt{}, err
	} else if existing != nil {
		return *existing, nil
	}

	now := s.now().UTC()
	if d.ReceivedAt.IsZero() {
		d.ReceivedAt = now
	}
	if d.DedupExpiresAt.IsZero() {
		d.DedupExpiresAt = d.ReceivedAt.Add(DefaultDedupWindow)
	}
	if d.ID == "" {
		d.ID = s.idFn()
	}

	var receipt Receipt
	var workID any
	if d.FilterDecision == FilterAccepted {
		if err := s.CheckIngressTx(ctx, tx, s.ingressLimits, IngressRequest{WorkspaceID: d.WorkspaceID, EndpointID: d.EndpointID, SourceDeliveryID: d.SourceDeliveryID, BodyBytes: int64(d.BodyBytes)}); err != nil {
			return Receipt{}, err
		}
		w.SourceRef = d.ID
		if w.TargetRevision == "" {
			w.TargetRevision = d.TargetRevision
		}
		r, err := s.AcceptTx(ctx, tx, w)
		if err != nil {
			return Receipt{}, err
		}
		receipt, workID = r, r.WorkID
	}

	var rawExpires any
	if !d.RawBodyExpiresAt.IsZero() {
		rawExpires = tsformat.Format(d.RawBodyExpiresAt)
	}
	// Empty string, not NULL: the column is NOT NULL and the partial unique
	// index is `WHERE content_key != ''`, so '' is the sentinel that means "this
	// profile has real identity and needs no content fallback".
	contentKey := d.ContentKey

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (
			id, workspace_id, endpoint_id, endpoint_kind, profile,
			source_delivery_id, content_key, body_sha256, body_bytes,
			raw_body, raw_body_expires_at, event_type, event_action, signing_key_id,
			filter_decision, filter_reason, target_revision, work_id,
			received_at, dedup_expires_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.WorkspaceID, d.EndpointID, d.EndpointKind, d.Profile,
		d.SourceDeliveryID, contentKey, d.BodySHA256, d.BodyBytes,
		d.RawBody, rawExpires, d.EventType, d.EventAction, d.SigningKeyID,
		d.FilterDecision, d.FilterReason, d.TargetRevision, workID,
		tsformat.Format(d.ReceivedAt), tsformat.Format(d.DedupExpiresAt),
	); err != nil {
		// A concurrent acceptance of the same delivery lost the race here rather
		// than at the lookup above. Same answer: the original receipt.
		if isUniqueViolation(err) {
			if existing, lerr := s.lookupDeliveryTx(ctx, tx, d); lerr == nil && existing != nil {
				return *existing, nil
			}
			return Receipt{}, fmt.Errorf("%w: %s", ErrDuplicateDelivery, d.SourceDeliveryID)
		}
		return Receipt{}, fmt.Errorf("work: insert delivery: %w", err)
	}

	receipt.DeliveryID = d.ID
	if d.FilterDecision == FilterIgnored {
		receipt.State = ""
	}
	return receipt, nil
}

// lookupDeliveryTx finds a delivery already recorded for this identity, by
// source id first and then by the profile's content key.
//
// A same-id-different-body arrival is a conflict, not a duplicate: something
// changed underneath an identifier that was supposed to be stable, and §5 says
// answer 409 and keep the original record rather than guess which is right.
func (s *Store) lookupDeliveryTx(ctx context.Context, tx *sql.Tx, d Delivery) (*Receipt, error) {
	scan := func(row *sql.Row) (*Receipt, string, error) {
		var id, bodySHA, decision string
		var workID sql.NullString
		err := row.Scan(&id, &bodySHA, &decision, &workID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("work: lookup delivery: %w", err)
		}
		r := &Receipt{DeliveryID: id, WorkID: workID.String, Duplicate: true}
		return r, bodySHA, nil
	}

	r, bodySHA, err := scan(tx.QueryRowContext(ctx,
		`SELECT id, body_sha256, filter_decision, work_id FROM webhook_deliveries
		 WHERE workspace_id = ? AND endpoint_id = ? AND source_delivery_id = ?`,
		d.WorkspaceID, d.EndpointID, d.SourceDeliveryID))
	if err != nil {
		return nil, err
	}
	if r != nil {
		// A pure lookup supplies no body, and must not be told its own absent
		// hash disagrees with the stored one.
		if d.BodySHA256 != "" && bodySHA != d.BodySHA256 {
			return nil, fmt.Errorf("%w: delivery %s already recorded with a different body",
				ErrDeliveryConflict, d.SourceDeliveryID)
		}
		if err := s.fillReceiptStateTx(ctx, tx, r); err != nil {
			return nil, err
		}
		return r, nil
	}

	if d.ContentKey == "" {
		return nil, nil
	}
	// GitHub does not sign its delivery id, so the same verified bytes arriving
	// under a fresh id are the same delivery. This is a deliberate merge of
	// byte-identical payloads on one endpoint, not a claim about GitHub event
	// identity in general.
	r, _, err = scan(tx.QueryRowContext(ctx,
		`SELECT id, body_sha256, filter_decision, work_id FROM webhook_deliveries
		 WHERE workspace_id = ? AND endpoint_id = ? AND content_key = ?`,
		d.WorkspaceID, d.EndpointID, d.ContentKey))
	if err != nil || r == nil {
		return r, err
	}
	if err := s.fillReceiptStateTx(ctx, tx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// fillReceiptStateTx reports the work's CURRENT state, so a re-delivery answers
// with what actually happened rather than repeating "queued" forever.
func (s *Store) fillReceiptStateTx(ctx context.Context, tx *sql.Tx, r *Receipt) error {
	if r.WorkID == "" {
		return nil
	}
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM work_items WHERE id = ?`, r.WorkID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("work: read receipt state: %w", err)
	}
	r.State = State(state)
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// LookupDelivery answers "did you receive this one" outside a transaction. The
// caller still needs workspace-scoped authorization: a receipt is an identifier,
// not a capability to read the work behind it.
func (s *Store) LookupDelivery(ctx context.Context, workspaceID, endpointID, sourceDeliveryID string, expectedBodySHA256 ...string) (*Receipt, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("work: begin delivery lookup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	expected := ""
	if len(expectedBodySHA256) > 0 {
		expected = expectedBodySHA256[0]
	}
	r, err := s.lookupDeliveryTx(ctx, tx, Delivery{
		WorkspaceID: workspaceID, EndpointID: endpointID, SourceDeliveryID: sourceDeliveryID, BodySHA256: expected,
	})
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, ErrNotFound
	}
	return r, nil
}
