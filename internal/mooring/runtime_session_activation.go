package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Small observed summaries only: never retain another encoded full checkpoint.
// They are populated by this channel's actual writer/sole reader, not a caller's
// copied acknowledgement. One startup transcript is allowed per process.
type sessionObserved struct {
	openHash, restoreHash          string
	acquire                        *protocol.OwnerAcquire
	grant                          *protocol.OwnerGrant
	restored                       *protocol.SessionRestored
	opened                         *protocol.SessionOpened
	invalid, activated, activating bool
}

func (s *SessionRuntime) observeSentLocked(kind string, raw []byte) {
	var slot *string
	switch kind {
	case "session_open":
		slot = &s.observed.openHash
	case "session_restore":
		slot = &s.observed.restoreHash
	default:
		return
	}
	hash := canonicalDigest(raw)
	if *slot != "" && *slot != hash {
		s.observed.invalid = true
	}
	*slot = hash
}
func (s *SessionRuntime) observeResponseLocked(kind string, raw []byte) {
	switch kind {
	case "session_opened":
		var v protocol.SessionOpened
		if s.observed.opened != nil || json.Unmarshal(raw, &v) != nil {
			s.observed.invalid = true
			return
		}
		s.observed.opened = &v
	case "session_restored":
		var v protocol.SessionRestored
		if s.observed.restored != nil || json.Unmarshal(raw, &v) != nil {
			s.observed.invalid = true
			return
		}
		s.observed.restored = &v
	}
}
func (s *SessionRuntime) observeOwnerLocked(entry *sessionControlEntry, raw []byte) {
	if entry.kind != "owner_acquire" {
		return
	}
	var a protocol.OwnerAcquire
	var g protocol.OwnerGrant
	if json.Unmarshal(entry.bytes, &a) != nil || json.Unmarshal(raw, &g) != nil {
		s.observed.invalid = true
		return
	}
	if s.observed.acquire != nil {
		s.observed.invalid = true
		return
	}
	s.observed.acquire = &a
	s.observed.grant = &g
}

// SessionActivation proves INITIAL owned startup on one actual process and
// exact SQL checkpoint. It is not participant permission or perpetual owner
// authority. Later commits, ticket issues and sends need fresh authority/fences.
// Its fields cannot be reconstructed from wire JSON.
type SessionActivation struct {
	runtime   *SessionRuntime
	store     *BatchStore
	owner     OwnerLease
	sessionID string
	basis     SessionBasis
	fullBasis FullCheckpointBasis
	targets   []BatchTargetVersion
}

func (a *SessionActivation) SessionID() string {
	if a == nil {
		return ""
	}
	return a.sessionID
}
func (a *SessionActivation) Scope() SessionKey {
	if a == nil {
		return SessionKey{}
	}
	return a.owner.Key
}
func (a *SessionActivation) Versions() RuntimeVersions {
	if a == nil || a.runtime == nil {
		return RuntimeVersions{}
	}
	return a.runtime.versions
}
func (a *SessionActivation) RuntimeRef() string {
	if a == nil || a.runtime == nil {
		return ""
	}
	return a.runtime.runtimeRef
}
func (a *SessionActivation) InitialBasis() SessionBasis {
	if a == nil {
		return SessionBasis{}
	}
	return a.basis
}

// InitialFullBasis is retained full wire identity, distinct from InitialBasis's
// internal opaque-content digest. Neither is continuing reader authority.
func (a *SessionActivation) InitialFullBasis() (FullCheckpointBasis, error) {
	if a == nil || !validFullCheckpointBasis(a.fullBasis) {
		return FullCheckpointBasis{}, ErrSessionRuntime
	}
	return a.fullBasis, nil
}
func (a *SessionActivation) InitialTargets() []BatchTargetVersion {
	if a == nil {
		return nil
	}
	return append([]BatchTargetVersion(nil), a.targets...)
}

// Only the concrete correlated session transport is returned, never *Runtime.
func (a *SessionActivation) SessionRuntime() (*SessionRuntime, error) {
	if a == nil || a.runtime == nil || a.store == nil {
		return nil, ErrSessionRuntime
	}
	a.runtime.mu.Lock()
	closed := a.runtime.closed || !a.runtime.observed.activated || a.runtime.observed.invalid
	a.runtime.mu.Unlock()
	if closed {
		return nil, ErrSessionRuntime
	}
	return a.runtime, nil
}
func (a *SessionActivation) NewCausalAdapter() (*SessionCausalAdapter, error) {
	s, err := a.SessionRuntime()
	if err != nil {
		return nil, err
	}
	return NewSessionCausalAdapter(s, a.owner.Key, s.versions)
}

// Check must run inside this exact store's writer transaction and the complete
// host lifetime lease. It intentionally does not pin the initial document head:
// real saved commits advance it. It does not replace canonical author checks.
func (a *SessionActivation) Check(ctx context.Context, tx *sql.Tx, store *BatchStore) error {
	if a == nil || a.store != store || store == nil {
		return ErrSessionRuntime
	}
	if _, err := a.SessionRuntime(); err != nil {
		return err
	}
	if store.config.Targets == nil {
		return ErrSessionRuntime
	}
	adapter, ok := store.config.Targets.Adapter.(*SessionCausalAdapter)
	if !ok || adapter == nil || adapter.session != a.runtime || !store.checkpointIdentityEnabled() || store.config.CheckpointIdentity.Adapter != adapter {
		return ErrSessionRuntime
	}
	return store.config.Owners.Check(ctx, tx, a.owner)
}

// Activate compares the supplied original Restore to the hash of the actual
// sent request, then verifies BOTH actual correlated acknowledgements and owner
// acquisition. No caller-provided Opened/Restored is accepted as evidence.
func (s *SessionRuntime) Activate(ctx context.Context, store *BatchStore, owner OwnerLease, restore protocol.SessionRestore) (*SessionActivation, error) {
	if s == nil || store == nil || store.ready() != nil || !store.targetsEnabled() || store.config.Targets.ReadPolicy != CausalTargetReadPolicy || owner.Key != s.scope || owner.OwnerID != s.runtimeRef {
		return nil, ErrSessionRuntime
	}
	adapter, ok := store.config.Targets.Adapter.(*SessionCausalAdapter)
	if !ok || adapter == nil || adapter.session != s || !store.checkpointIdentityEnabled() || store.config.CheckpointIdentity.Adapter != adapter {
		return nil, ErrSessionRuntime
	}
	raw, err := json.Marshal(restore)
	if err != nil {
		return nil, err
	}
	canonical, _, err := s.validate(raw, false)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.observed.activating {
		s.mu.Unlock()
		return nil, ErrSessionRuntime
	}
	s.observed.activating = true
	defer func() { s.mu.Lock(); s.observed.activating = false; s.mu.Unlock() }()
	o := s.observed
	sessionID := s.sessionID
	closed := s.closed
	s.mu.Unlock()
	if closed || o.invalid || o.activated || o.openHash == "" || o.restoreHash != canonicalDigest(canonical) || o.acquire == nil || o.grant == nil || o.restored == nil || o.opened == nil {
		return nil, ErrSessionRuntime
	}
	scope := restore.Scope
	versions := restore.Versions
	fence := restore.Fence
	if fence.Generation != protocol.Counter(strconv.FormatInt(owner.Generation, 10)) || fence.Token != owner.Token || restore.SessionID != protocol.Identifier(sessionID) || restore.RuntimeRef != protocol.Identifier(s.runtimeRef) || o.acquire.Scope != scope || o.acquire.RuntimeRef != restore.RuntimeRef || o.grant.Scope != scope || o.grant.SessionID != restore.SessionID || o.grant.RuntimeRef != restore.RuntimeRef || o.grant.Fence != fence || o.grant.RenewalNonce != o.acquire.RenewalNonce || o.grant.RequestID != o.acquire.RequestID {
		return nil, ErrSessionRuntime
	}
	duration, err := batchCounter(string(o.grant.LeaseDurationMs))
	if err != nil || duration == 0 || duration > uint64(store.config.Owners.ttl.Milliseconds()) {
		return nil, ErrSessionRuntime
	}
	cp := restore.Checkpoint
	if cp.Scope != scope || cp.Versions != versions || cp.Revision != o.grant.Revision {
		return nil, ErrSessionRuntime
	}
	identity, err := VerifyCheckpointIdentity(cp, CheckpointIdentityLimits{DecodedBytes: maxPreparedCheckpoint, PreimageBytes: sessionOutputBytes})
	if err != nil {
		return nil, err
	}
	restoredBytes := identity.State
	basis := protocol.ConfirmedCheckpointBasis{Revision: cp.Revision, Digest: cp.Digest}
	for _, v := range []struct {
		scope        protocol.Scope
		session, ref protocol.Identifier
		fence        protocol.Fence
		versions     protocol.Versions
		basis        protocol.ConfirmedCheckpointBasis
	}{{o.opened.Scope, o.opened.SessionID, o.opened.RuntimeRef, o.opened.Fence, o.opened.Versions, o.opened.ConfirmedBasis}, {o.restored.Scope, o.restored.SessionID, o.restored.RuntimeRef, o.restored.Fence, o.restored.Versions, o.restored.ConfirmedBasis}} {
		if v.scope != scope || v.session != restore.SessionID || v.ref != restore.RuntimeRef || v.fence != fence || v.versions != versions || v.basis != basis {
			return nil, ErrSessionRuntime
		}
	}
	// Restoration request correlation was enforced by the sole reader; additionally
	// retain its exact request identifier rather than accepting any copied receipt.
	if o.restored.RequestID != restore.RequestID {
		return nil, ErrSessionRuntime
	}
	var result *SessionActivation
	err = store.transaction(ctx, func(tx *sql.Tx) error {
		if err := store.config.Owners.Check(ctx, tx, owner); err != nil {
			return err
		}
		retained, retainedIdentity, err := store.loadRetainedFullCheckpoint(ctx, tx, owner.Key, string(cp.Revision), true)
		if err != nil || retained.Digest != cp.Digest || retainedIdentity.ContentDigest != identity.ContentDigest || !bytes.Equal(retainedIdentity.State, restoredBytes) {
			return ErrSessionRuntime
		}
		head := string(retained.Revision)
		var tracking int
		if err = tx.QueryRowContext(ctx, `SELECT target_tracking FROM mooring_collab_documents WHERE page_id=?`, owner.Key.ArtifactID).Scan(&tracking); err != nil || tracking != 1 {
			return ErrSessionRuntime
		}
		rev, err := batchCounter(head)
		if err != nil {
			return err
		}
		targets, err := initialSessionTargets(ctx, tx, store, owner.Key, rev)
		if err != nil {
			return err
		}
		if len(targets) != len(restore.Targets) {
			return ErrSessionRuntime
		}
		actual := make(map[string]BatchTargetVersion, len(targets))
		for _, v := range targets {
			actual[v.TargetID] = v
		}
		for _, v := range restore.Targets {
			key := string(v.TargetID)
			expected, exists := actual[key]
			if !exists || expected.Revision != string(v.Revision) || expected.StructureRevision != string(v.StructureRevision) || expected.Deleted != v.Deleted {
				return ErrSessionRuntime
			}
			delete(actual, key)
		}
		if len(actual) != 0 {
			return ErrSessionRuntime
		}
		// No channel death or competing activation may race the final SQL fence.
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.observed.invalid || s.observed.activated {
			return ErrSessionRuntime
		}
		if err = store.config.Owners.Check(ctx, tx, owner); err != nil {
			return err
		}
		result = &SessionActivation{runtime: s, store: store, owner: owner, sessionID: sessionID, basis: SessionBasis{Revision: head, Digest: identity.ContentDigest}, fullBasis: FullCheckpointBasis{Revision: head, Digest: string(cp.Digest)}, targets: append([]BatchTargetVersion(nil), targets...)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.observed.invalid || s.observed.activated {
		return nil, ErrSessionRuntime
	}
	s.observed.activated = true
	return result, nil
}

// Initial inventory has no fake atomic operation or prospective revisions.
func initialSessionTargets(ctx context.Context, tx *sql.Tx, store *BatchStore, key SessionKey, head uint64) ([]BatchTargetVersion, error) {
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id,target_id,revision,structure_revision,deleted FROM mooring_collab_target_versions WHERE page_id=? ORDER BY target_id`, key.ArtifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []BatchTargetVersion{}
	for rows.Next() {
		var workspace string
		var v BatchTargetVersion
		if err = rows.Scan(&workspace, &v.TargetID, &v.Revision, &v.StructureRevision, &v.Deleted); err != nil {
			return nil, err
		}
		rev, e1 := batchCounter(v.Revision)
		structure, e2 := batchCounter(v.StructureRevision)
		if workspace != key.WorkspaceID || !batchID(v.TargetID) || e1 != nil || e2 != nil || structure > rev || rev > head {
			return nil, ErrInvalid
		}
		if len(result) >= store.config.Targets.Limits.Targets {
			return nil, ErrBatchCapacity
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
