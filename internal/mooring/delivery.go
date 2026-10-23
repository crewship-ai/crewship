package mooring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"
)

var (
	ErrDeliveryDisabled = errors.New("Mooring delivery authority disabled")
	ErrDeliveryClosed   = errors.New("delivery gate closed")
	ErrDeliveryCapacity = errors.New("delivery admission capacity exhausted")
)

// DeliveryAccess is a host authorization intent. The callback resolves the
// authenticated human/run principal and CURRENT rights in this writer tx.
// Neither Participant nor a runtime capability is itself an authorization.
type DeliveryAccess struct {
	Intent      string
	Scope       SessionKey
	Participant string
}
type DeliveryAuthority func(context.Context, *sql.Tx, DeliveryAccess) (permissionVersion string, err error)
type DeliveryGrant struct {
	GateID               string
	RenewalNonce         string
	PermissionVersion    string
	OwnerGeneration      int64
	DurationMilliseconds int64
}
type DeliveryBarrier struct {
	ID                    string
	Complete              bool
	RemainingMilliseconds int64
	// NoIssuedGates is a prepared empty-set decision, never a wire confirmation.
	// The enclosing ACL transaction MUST commit before reporting completion.
	// Its transient ID has no persistent status row. Retry revocation if needed.
	NoIssuedGates bool
}

// DeliveryStore is unused and independently disabled. It issues no content,
// tickets or Saved receipts. Every dispatcher must enforce the corresponding
// Rust send-time CLOCK_BOOTTIME gate before each frame. ACK is accepted only
// from the current authenticated runtime after ALL paths for a gate stop.
type DeliveryStore struct {
	owners    *OwnerStore
	enabled   bool
	authorize DeliveryAuthority
	ttl       time.Duration
	maxGates  int
}

func NewDeliveryStore(owners *OwnerStore, enabled bool, authority DeliveryAuthority, ttl time.Duration, maxGates int) *DeliveryStore {
	return &DeliveryStore{owners, enabled, authority, ttl, maxGates}
}
func (s *DeliveryStore) valid() error {
	if !s.enabled {
		return ErrDeliveryDisabled
	}
	if s.owners == nil || !s.owners.enabled || s.authorize == nil || s.ttl < time.Millisecond || s.ttl > 2*time.Second || s.ttl%time.Millisecond != 0 || s.ttl > s.owners.ttl || s.maxGates < 1 || s.maxGates > 5000 {
		return ErrInvalid
	}
	return nil
}
func deliveryID() (string, error) {
	var raw [32]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(raw[:]), nil
}
func (s *DeliveryStore) authorizeTx(ctx context.Context, tx *sql.Tx, intent string, key SessionKey, participant string) (string, error) {
	if !ownerIDValid(participant) {
		return "", ErrInvalid
	}
	if e := ownerScopeExists(ctx, tx, key); e != nil {
		return "", e
	}
	version, e := s.authorize(ctx, tx, DeliveryAccess{intent, key, participant})
	if e != nil {
		return "", e
	}
	counter, parseErr := strconv.ParseUint(version, 10, 64)
	if parseErr != nil || strconv.FormatUint(counter, 10) != version {
		return "", ErrInvalid
	}
	return version, nil
}
func (s *DeliveryStore) Open(ctx context.Context, l OwnerLease, participant, request string) (DeliveryGrant, error) {
	if e := s.valid(); e != nil {
		return DeliveryGrant{}, e
	}
	if !ownerIDValid(request) {
		return DeliveryGrant{}, ErrInvalid
	}
	var grant DeliveryGrant
	e := s.owners.transaction(ctx, func(tx *sql.Tx, _ string, _ int64) error {
		if e := s.owners.Check(ctx, tx, l); e != nil {
			return e
		}
		version, e := s.authorizeTx(ctx, tx, "connect", l.Key, participant)
		if e != nil {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_delivery_gates WHERE page_id=?`, l.Key.ArtifactID).Scan(&count); e != nil {
			return e
		}
		if count >= s.maxGates {
			return ErrDeliveryCapacity
		}
		if e = s.owners.Check(ctx, tx, l); e != nil {
			return e
		}
		boot, now, e := s.owners.now()
		if e != nil {
			return e
		}
		if now > math.MaxInt64-int64(s.ttl) {
			return ErrInvalid
		}
		id, e := deliveryID()
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO mooring_delivery_gates(id,page_id,workspace_id,application_id,participant_ref,owner_id,owner_generation,owner_token_hash,permission_version,boot_id,expires_ns,renewal_nonce) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, l.Key.ArtifactID, l.Key.WorkspaceID, l.Key.ApplicationID, participant, l.OwnerID, l.Generation, ownerTokenHash(l.Token), version, boot, now+int64(s.ttl), request)
		if e == nil {
			grant = DeliveryGrant{id, request, version, l.Generation, s.ttl.Milliseconds()}
		}
		return e
	})
	if e != nil {
		return DeliveryGrant{}, e
	}
	return grant, nil
}

type deliveryGate struct {
	ordinal                             int64
	participant, version, boot, request string
	expires                             int64
	closed, blocked                     bool
}

func loadDeliveryGate(ctx context.Context, tx *sql.Tx, l OwnerLease, id string) (deliveryGate, error) {
	var g deliveryGate
	e := tx.QueryRowContext(ctx, `SELECT ordinal,participant_ref,permission_version,boot_id,expires_ns,renewal_nonce,closed,blocked FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND owner_generation=? AND owner_token_hash=?`, id, l.Key.ArtifactID, l.Key.WorkspaceID, l.Key.ApplicationID, l.OwnerID, l.Generation, ownerTokenHash(l.Token)).Scan(&g.ordinal, &g.participant, &g.version, &g.boot, &g.expires, &g.request, &g.closed, &g.blocked)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrDeliveryClosed
	}
	return g, e
}
func (s *DeliveryStore) Renew(ctx context.Context, l OwnerLease, id, request string) (DeliveryGrant, error) {
	if e := s.valid(); e != nil {
		return DeliveryGrant{}, e
	}
	if !ownerIDValid(id) || !ownerIDValid(request) {
		return DeliveryGrant{}, ErrInvalid
	}
	var grant DeliveryGrant
	var denied error
	e := s.owners.transaction(ctx, func(tx *sql.Tx, boot string, now int64) error {
		if e := s.owners.Check(ctx, tx, l); e != nil {
			return e
		}
		g, e := loadDeliveryGate(ctx, tx, l, id)
		if e != nil {
			return e
		}
		closeGate := func() error {
			denied = ErrDeliveryClosed
			_, e := tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE id=?`, id)
			return e
		}
		if g.closed || g.boot != boot || now >= g.expires {
			return closeGate()
		}
		version, e := s.authorizeTx(ctx, tx, "renew", l.Key, g.participant)
		if e != nil || version != g.version {
			return closeGate()
		}
		if e = s.owners.Check(ctx, tx, l); e != nil {
			return e
		}
		freshBoot, freshNow, e := s.owners.now()
		if e != nil {
			return e
		}
		if freshBoot != g.boot || freshNow >= g.expires {
			return closeGate()
		}
		if g.request != request {
			_, e = tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET expires_ns=?,renewal_nonce=? WHERE id=?`, freshNow+int64(s.ttl), request, id)
			if e != nil {
				return e
			}
		}
		grant = DeliveryGrant{id, request, version, l.Generation, s.ttl.Milliseconds()}
		return nil
	})
	if e != nil {
		return DeliveryGrant{}, e
	}
	if denied != nil {
		return DeliveryGrant{}, denied
	}
	return grant, nil
}

// RevokeTx must run inside the canonical ACL change's writer transaction.
// It permanently closes ALL generations of this participant's gates and records
// an immutable barrier membership. Regrant requires fresh gate identities.
func (s *DeliveryStore) RevokeTx(ctx context.Context, tx *sql.Tx, key SessionKey, participant string) (DeliveryBarrier, error) {
	if e := s.valid(); e != nil {
		return DeliveryBarrier{}, e
	}
	if tx == nil {
		return DeliveryBarrier{}, ErrInvalid
	}
	if e := ownerFence(ctx, tx); e != nil {
		return DeliveryBarrier{}, e
	}
	var sync int
	if e := tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); e != nil {
		return DeliveryBarrier{}, e
	}
	if sync < 2 {
		return DeliveryBarrier{}, ErrInvalid
	}
	if _, e := s.authorizeTx(ctx, tx, "revoke", key, participant); e != nil {
		return DeliveryBarrier{}, e
	}
	var cutoff int64
	if e := tx.QueryRowContext(ctx, `SELECT coalesce(max(ordinal),0) FROM mooring_delivery_gates WHERE page_id=? AND workspace_id=? AND application_id=? AND participant_ref=?`, key.ArtifactID, key.WorkspaceID, key.ApplicationID, participant).Scan(&cutoff); e != nil {
		return DeliveryBarrier{}, e
	}
	if cutoff == 0 {
		id, e := deliveryID()
		return DeliveryBarrier{ID: id, NoIssuedGates: true}, e
	}
	// Repeated revocation without new admission reuses the same immutable set.
	// Each new nonempty barrier charges at least one newly admitted gate, so
	// barriers remain bounded by gate admission without ever blocking revocation.
	var id string
	e := tx.QueryRowContext(ctx, `SELECT id FROM mooring_delivery_barriers WHERE page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND cutoff_ordinal=?`, key.ArtifactID, key.WorkspaceID, key.ApplicationID, participant, cutoff).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		id, e = deliveryID()
		if e != nil {
			return DeliveryBarrier{}, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO mooring_delivery_barriers(id,page_id,workspace_id,application_id,participant_ref,cutoff_ordinal) VALUES(?,?,?,?,?,?)`, id, key.ArtifactID, key.WorkspaceID, key.ApplicationID, participant, cutoff)
	}
	if e != nil {
		return DeliveryBarrier{}, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE page_id=? AND workspace_id=? AND application_id=? AND participant_ref=?`, key.ArtifactID, key.WorkspaceID, key.ApplicationID, participant)
	// No Complete claim until the caller actually commits the ACL transaction.
	return DeliveryBarrier{ID: id}, e
}
func (s *DeliveryStore) Barrier(ctx context.Context, key SessionKey, participant, id string) (DeliveryBarrier, error) {
	if e := s.valid(); e != nil {
		return DeliveryBarrier{}, e
	}
	if !ownerIDValid(id) {
		return DeliveryBarrier{}, ErrInvalid
	}
	result := DeliveryBarrier{ID: id}
	e := s.owners.transaction(ctx, func(tx *sql.Tx, boot string, now int64) error {
		if _, e := s.authorizeTx(ctx, tx, "barrier", key, participant); e != nil {
			return e
		}
		var cutoff int64
		e := tx.QueryRowContext(ctx, `SELECT cutoff_ordinal FROM mooring_delivery_barriers WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=?`, id, key.ArtifactID, key.WorkspaceID, key.ApplicationID, participant).Scan(&cutoff)
		if e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT boot_id,expires_ns,blocked FROM mooring_delivery_gates WHERE page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND ordinal<=?`, key.ArtifactID, key.WorkspaceID, key.ApplicationID, participant, cutoff)
		if e != nil {
			return e
		}
		defer rows.Close()
		var max int64
		for rows.Next() {
			var b string
			var expiry int64
			var blocked bool
			if e = rows.Scan(&b, &expiry, &blocked); e != nil {
				return e
			}
			if !blocked && b == boot && expiry > now && expiry-now > int64(2*time.Second) {
				return ErrInvalid
			}
			if !blocked && b == boot && expiry > now && expiry-now > max {
				max = expiry - now
			}
		}
		if e = rows.Err(); e != nil {
			return e
		}
		result.Complete = max == 0
		result.RemainingMilliseconds = (max + int64(time.Millisecond) - 1) / int64(time.Millisecond)
		return nil
	})
	if e != nil {
		return DeliveryBarrier{}, e
	}
	return result, nil
}
func (s *DeliveryStore) AcknowledgeBlocked(ctx context.Context, l OwnerLease, id, barrier string) error {
	if e := s.valid(); e != nil {
		return e
	}
	if !ownerIDValid(id) || !ownerIDValid(barrier) {
		return ErrInvalid
	}
	return s.owners.transaction(ctx, func(tx *sql.Tx, _ string, _ int64) error {
		if e := s.owners.Check(ctx, tx, l); e != nil {
			return e
		}
		g, e := loadDeliveryGate(ctx, tx, l, id)
		if e != nil {
			return e
		}
		if !g.closed {
			return ErrDeliveryClosed
		}
		var exists int
		if e = tx.QueryRowContext(ctx, `SELECT 1 FROM mooring_delivery_barriers WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND cutoff_ordinal>=?`, barrier, l.Key.ArtifactID, l.Key.WorkspaceID, l.Key.ApplicationID, g.participant, g.ordinal).Scan(&exists); e != nil {
			return e
		}
		if e := s.owners.Check(ctx, tx, l); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET blocked=1 WHERE id=? AND closed=1`, id)
		return e
	})
}
