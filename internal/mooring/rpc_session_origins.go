package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

const sessionOriginCapability = "origin_inventory_v1"

type allocatedSessionOrigin struct {
	origin uint64
	clock  uint32
}

// SessionOriginInventory is callback-scoped evidence from the actual concrete
// process, not an origin grant, ACL, writer constructor or serialized witness.
// Its private shared state makes zero values, copied values and escaped contexts
// unusable after the writer callback. No public list/bool can construct it.
type SessionOriginInventory struct{ state *sessionOriginInventoryState }
type sessionOriginContextKey struct{}
type sessionOriginInventoryState struct {
	mu         sync.Mutex
	active     bool
	store      *BatchStore
	activation *SessionActivation
	tx         *sql.Tx
	basis      FullCheckpointBasis
	entries    []allocatedSessionOrigin
}

// Visit makes allocated identities available only inside their original active
// writer callback. The consumer still owns issuance authority, burned-ID union,
// quota, uniqueness and publication AFTER successful COMMIT. It must not publish
// pending allocations or turn a baseline identity into an issued source.
func (p *SessionOriginInventory) Visit(ctx context.Context, tx *sql.Tx, store *BatchStore, owner OwnerLease, basis FullCheckpointBasis, visit func(uint64, uint32) error) error {
	if ctx == nil || ctx.Err() != nil || p == nil || p.state == nil || visit == nil {
		return ErrPrivateControl
	}
	state := p.state
	if !state.mu.TryLock() {
		return ErrPrivateControl
	}
	defer state.mu.Unlock()
	if !state.active || state.tx != tx || state.store != store || state.basis != basis || state.activation == nil || state.activation.owner != owner || ctx.Value(sessionOriginContextKey{}) != state {
		return ErrPrivateControl
	}
	if _, err := store.currentOriginCheckpoint(ctx, tx, state.activation, basis); err != nil {
		return err
	}
	for _, entry := range state.entries {
		if err := visit(entry.origin, entry.clock); err != nil {
			return err
		}
	}
	_, err := store.currentOriginCheckpoint(ctx, tx, state.activation, basis)
	return err
}

// WithSessionOrigins uses the immutable store authority and actual FULL writer.
// It accepts no caller authorization override or copied runtime acknowledgement.
// The opaque result dies before final fences/COMMIT, including on callback error.
// This read-purpose integration does not qualify agent issuance or initialization
// continuation; those require their own original-source authority operations.
func (s *BatchStore) WithSessionOrigins(ctx context.Context, activation *SessionActivation, basis FullCheckpointBasis, consume func(context.Context, *sql.Tx, *SessionOriginInventory) error) error {
	if ctx == nil || ctx.Err() != nil || s == nil || s.ready() != nil || activation == nil || activation.store != s || !validFullCheckpointBasis(basis) || consume == nil {
		return ErrPrivateControl
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		cp, err := s.currentOriginCheckpoint(ctx, tx, activation, basis)
		if err != nil {
			return err
		}
		adapter, ok := s.config.Targets.Adapter.(*SessionCausalAdapter)
		if !ok || adapter == nil || adapter.session != activation.runtime {
			return ErrPrivateControl
		}
		entries, err := adapter.inspectSessionOrigins(ctx, activation.owner, cp)
		if err != nil {
			return err
		}
		if _, err = s.currentOriginCheckpoint(ctx, tx, activation, basis); err != nil {
			return err
		}
		state := &sessionOriginInventoryState{active: true, store: s, activation: activation, tx: tx, basis: basis, entries: entries}
		scoped, cancel := context.WithCancel(context.WithValue(ctx, sessionOriginContextKey{}, state))
		closeProof := func() { state.mu.Lock(); state.active = false; state.mu.Unlock(); cancel() }
		// Also revoke copies/contexts if trusted host callback code unwinds.
		defer closeProof()
		err = consume(scoped, tx, &SessionOriginInventory{state: state})
		closeProof()
		if err != nil {
			return err
		}
		_, err = s.currentOriginCheckpoint(ctx, tx, activation, basis)
		return err
	})
}

func (s *BatchStore) currentOriginCheckpoint(ctx context.Context, tx *sql.Tx, activation *SessionActivation, basis FullCheckpointBasis) (protocol.Checkpoint, error) {
	var empty protocol.Checkpoint
	if ctx == nil || ctx.Err() != nil || activation == nil || activation.store != s || !validFullCheckpointBasis(basis) {
		return empty, ErrPrivateControl
	}
	owner := activation.owner
	// Source authorization precedes any checkpoint/head lookup or decoding.
	if err := s.authorize(ctx, tx, owner.Key, "read_checkpoint", nil, "", ""); err != nil {
		return empty, err
	}
	if err := activation.Check(ctx, tx, s); err != nil {
		return empty, err
	}
	if !activation.runtime.originInventorySelected() {
		return empty, ErrPrivateControl
	}
	var local bool
	var head string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id=? AND application_id=?,CASE WHEN length(CAST(revision AS BLOB))<=20 THEN revision ELSE '' END FROM mooring_collab_documents WHERE page_id=?`, owner.Key.WorkspaceID, owner.Key.ApplicationID, owner.Key.ArtifactID).Scan(&local, &head); err != nil {
		return empty, err
	}
	if !local || head != basis.Revision {
		return empty, ErrCheckpointIdentity
	}
	cp, _, err := s.loadRetainedFullCheckpoint(ctx, tx, owner.Key, head, true)
	if err != nil {
		return empty, err
	}
	if string(cp.Digest) != basis.Digest {
		return empty, ErrCheckpointIdentity
	}
	// Decoding or callback SQL must not turn an earlier decision into a lease.
	if err = s.authorize(ctx, tx, owner.Key, "read_checkpoint", nil, "", ""); err != nil {
		return empty, err
	}
	if err = activation.Check(ctx, tx, s); err != nil {
		return empty, err
	}
	if !activation.runtime.originInventorySelected() {
		return empty, ErrPrivateControl
	}
	return cp, nil
}

func (s *SessionRuntime) originInventorySelected() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.observed.invalid || !s.observed.activated || s.observed.opened == nil || !sessionCapabilitiesEqual(s.observed.requiredCapabilities, s.observed.opened.Capabilities) {
		return false
	}
	for _, capability := range s.observed.requiredCapabilities {
		if string(capability) == sessionOriginCapability {
			return true
		}
	}
	return false
}
func sessionCapabilitiesEqual(required []protocol.SessionOpenRequiredCapabilitiesItem, actual []protocol.SessionOpenedCapabilitiesItem) bool {
	if len(required) == 0 || len(required) != len(actual) {
		return false
	}
	for i, capability := range required {
		if string(capability) != string(actual[i]) {
			return false
		}
	}
	return true
}

func (a *SessionCausalAdapter) inspectSessionOrigins(ctx context.Context, owner OwnerLease, cp protocol.Checkpoint) ([]allocatedSessionOrigin, error) {
	if a == nil || a.session == nil || a.causal == nil || !a.session.originInventorySelected() || owner.Key != a.session.scope || owner.OwnerID != a.session.runtimeRef {
		return nil, ErrPrivateControl
	}
	id, err := prepareRequestID()
	if err != nil {
		return nil, err
	}
	a.session.mu.Lock()
	session := a.session.sessionID
	a.session.mu.Unlock()
	request := protocol.SessionInspectOrigins{Type: "session_inspect_origins", RequestID: id, ControlProfile: PrivateSessionProfile, Scope: a.causal.base.wireScope(), SessionID: protocol.Identifier(session), RuntimeRef: protocol.Identifier(a.session.runtimeRef), Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}, Versions: a.causal.base.wireVersions(), Checkpoint: cp}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, ErrPrivateControl
	}
	raw, err = a.session.request(ctx, raw, 5*time.Second)
	if err != nil {
		return nil, err
	}
	invalid := func() ([]allocatedSessionOrigin, error) {
		a.session.fail(ErrPrivateControl)
		return nil, ErrPrivateControl
	}
	var response protocol.SessionOriginsInspected
	if json.Unmarshal(raw, &response) != nil || response.Type != "session_origins_inspected" || response.ControlProfile != PrivateSessionProfile || response.RequestID != id || response.Scope != request.Scope || response.SessionID != request.SessionID || response.RuntimeRef != request.RuntimeRef || response.Fence != request.Fence || response.Versions != request.Versions || response.CheckpointBasis.Revision != cp.Revision || response.CheckpointBasis.Digest != cp.Digest || response.CheckpointGeneration != cp.Generation {
		return invalid()
	}
	identity, err := VerifyCheckpointIdentity(cp, CheckpointIdentityLimits{DecodedBytes: maxPreparedCheckpoint, PreimageBytes: sessionOutputBytes})
	if err != nil || string(response.ContentDigest) != identity.ContentDigest || len(response.Allocated) > 4096 {
		return invalid()
	}
	entries := make([]allocatedSessionOrigin, 0, len(response.Allocated))
	var previous uint64
	for i, entry := range response.Allocated {
		origin, e := batchCounter(string(entry.Origin))
		if e != nil || origin > (1<<53)-1 {
			return invalid()
		}
		clock, e := batchCounter(string(entry.Clock))
		if e != nil || clock == 0 || clock > 1<<32-1 || (i > 0 && origin <= previous) {
			return invalid()
		}
		entries = append(entries, allocatedSessionOrigin{origin: origin, clock: uint32(clock)})
		previous = origin
	}
	if !a.session.originInventorySelected() {
		return invalid()
	}
	return entries, nil
}
