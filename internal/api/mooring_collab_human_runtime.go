package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Only a process lifetime and exact concrete pointer identity are shared here.
// Constructors still require the actual negotiated legacy runtime or the
// separately verified session activation. This interface grants no authority.
type collaborationHumanProcess interface {
	Done() <-chan struct{}
}

// Host lifecycle identity, never a human authentication context. Capture ONCE
// at registry construction; a restore must not refresh an existing registry.
func collaborationHumanLifecycleStamp(f *collaborationIngressFence) (collaborationIngressStamp, error) {
	if f == nil || !f.mu.TryRLock() {
		return collaborationIngressStamp{}, errCollaborationIngress
	}
	defer f.mu.RUnlock()
	if f.suspended || f.incarnation == "" {
		return collaborationIngressStamp{}, errCollaborationIngress
	}
	return collaborationIngressStamp{fence: f, incarnation: f.incarnation}, nil
}

func (s *collaborationHumanRegistry) acquireRuntimeOperation() (func(), error) {
	if s == nil || !s.runtimeMu.TryRLock() {
		return nil, errCollaborationAdmission
	}
	s.mu.Lock()
	active := !s.replacing && s.runtime != nil && s.bindingID != ""
	if active && s.sessionStore != nil {
		_, err := s.sessionStore.live()
		active = err == nil
	}
	s.mu.Unlock()
	if !active {
		s.runtimeMu.RUnlock()
		return nil, errCollaborationAdmission
	}
	return s.runtimeMu.RUnlock, nil
}

func (s *collaborationHumanRegistry) matchesRuntimeBinding(runtime collaborationHumanProcess, bindingID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.replacing && bindingID != "" && s.bindingID == bindingID && s.runtime == runtime
}

// The immutable binding must be captured when constructing a store. Holding a
// method value of a mutable registry would let old stores authenticate known
// retries after rebind without ever calling their dead preparation adapter.
type collaborationHumanStoreBinding struct {
	runtime   collaborationHumanProcess
	id        string
	adapter   mooring.TargetBatchAdapter
	authority mooring.BatchAuthority
}

func (s *collaborationHumanRegistry) storeBinding() (collaborationHumanStoreBinding, error) {
	unlock, err := s.acquireRuntimeOperation()
	if err != nil {
		return collaborationHumanStoreBinding{}, err
	}
	defer unlock()
	s.mu.Lock()
	runtime, id, adapter := s.runtime, s.bindingID, s.targetAdapter
	s.mu.Unlock()
	return collaborationHumanStoreBinding{runtime, id, adapter, func(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
		return s.authorizeBatchAtBinding(ctx, tx, access, runtime, id)
	}}, nil
}

// hostSessionID is selected once, before SessionOpen. The owner grant, restore,
// opened confirmation and reconnects must all echo this one identity.
func (s *collaborationHumanRegistry) hostSessionID() string { return s.sessionID }

// replaceRuntime is exclusively a trusted Rust-only recovery operation. It
// never creates an admission, recaptures credentials, or installs content. The
// manager must additionally validate exact SessionOpened before forwarding any
// content from the replacement. Errors after closure leave admission closed;
// the manager may retry this method, preserving the same original registry.
func (s *collaborationHumanRegistry) replaceRuntime(ctx context.Context, replacement *mooring.Runtime, owner mooring.OwnerLease) (collaborationHumanStoreBinding, error) {
	var empty collaborationHumanStoreBinding
	if s == nil || replacement == nil || owner.Key != s.scope {
		return empty, errCollaborationAdmission
	}
	var adapter mooring.TargetBatchAdapter
	var err error
	switch s.readPolicy {
	case "":
		adapter, err = mooring.NewRPCPrepareAdapter(replacement, s.scope, s.versions)
	case mooring.CausalTargetReadPolicy:
		adapter, err = mooring.NewCausalRPCPrepareAdapter(replacement, s.scope, s.versions)
	default:
		return empty, errCollaborationAdmission
	}
	if err != nil {
		return empty, err
	}
	// Do not wait for old host operations while holding the SQL writer. A busy
	// bounded operation causes a retry; it cannot be replaced underneath commit.
	if !s.runtimeMu.TryLock() {
		return empty, errCollaborationAdmission
	}
	defer s.runtimeMu.Unlock()
	release, err := s.fence.acquireOriginal(s.lifecycle)
	if err != nil {
		return empty, err
	}
	defer release()
	s.mu.Lock()
	old := s.runtime
	// A prepare-only runtime may never replace a verified live session. Such
	// recovery needs another actual activation and its original-store proof.
	if _, legacy := old.(*mooring.Runtime); !legacy {
		s.mu.Unlock()
		return empty, errCollaborationAdmission
	}
	if old == nil || old == replacement || old.Done() == nil || owner.Generation <= s.ownerGeneration {
		s.mu.Unlock()
		return empty, errCollaborationAdmission
	}
	select {
	case <-old.Done():
	default:
		s.mu.Unlock()
		return empty, errCollaborationAdmission
	}
	s.replacing = true
	clear(s.tickets)
	s.mu.Unlock()

	// Close every issued content gate in the scope, including disconnected
	// admissions. Preserve all immutable issuance/expiry and barrier evidence.
	tx, err := s.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return empty, err
	}
	var synchronous int
	if err = tx.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous < 2 {
		return empty, errCollaborationAdmission
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE page_id=? AND workspace_id=? AND application_id=?`, s.scope.ArtifactID, s.scope.WorkspaceID, s.scope.ApplicationID); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}

	// Closed is not an acknowledgement. Only actual blocked acknowledgement or
	// the last original BOOTTIME lease expiry permits replacement admission.
	tx, err = s.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return empty, err
	}
	boot, now, err := s.clock.Now()
	if err != nil || now < 0 {
		return empty, errCollaborationAdmission
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_delivery_gates WHERE page_id=? AND workspace_id=? AND application_id=? AND (closed=0 OR (blocked=0 AND boot_id=? AND expires_ns>?)))`, s.scope.ArtifactID, s.scope.WorkspaceID, s.scope.ApplicationID, boot, int64(now)).Scan(&pending); err != nil {
		return empty, err
	}
	if pending {
		return empty, mooring.ErrDeliveryClosed
	}
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	select {
	case <-replacement.Done():
		return empty, errCollaborationAdmission
	default:
	}
	id, err := collaborationOpaqueID()
	if err != nil {
		return empty, err
	}
	s.mu.Lock()
	s.runtime, s.targetAdapter, s.bindingID, s.replacing = replacement, adapter, id, false
	s.ownerGeneration = owner.Generation
	s.mu.Unlock()
	// runtimeMu is already held exclusively; do not recursively acquire it.
	return collaborationHumanStoreBinding{replacement, id, adapter, func(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
		return s.authorizeBatchAtBinding(ctx, tx, access, replacement, id)
	}}, nil
}

// reconnect is called by the trusted manager for an EXISTING admission. It
// neither accepts a fresh HTTP credential nor changes source/epoch/deadline.
func (s *collaborationHumanRegistry) reconnect(ctx context.Context, binding collaborationHumanStoreBinding, owner mooring.OwnerLease, participant string) (string, error) {
	unlock, err := s.acquireRuntimeOperation()
	if err != nil {
		return "", err
	}
	defer unlock()
	if owner.Key != s.scope || !s.matchesRuntimeBinding(binding.runtime, binding.id) {
		return "", errCollaborationAdmission
	}
	s.mu.Lock()
	admission, ok := s.admissions[participant]
	s.mu.Unlock()
	if !ok {
		return "", errCollaborationAdmission
	}
	release, err := s.fence.acquireOriginal(admission.ingress)
	if err != nil {
		return "", err
	}
	defer release()
	tx, err := s.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return "", err
	}
	if _, err = s.checkCaptured(ctx, tx, admission, admission.write, 2*time.Second); err != nil {
		return "", err
	}
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	boot, now, err := s.clock.Now()
	if err != nil || boot != admission.boot || now < 0 {
		return "", errCollaborationAdmission
	}
	if now >= admission.currentDeadline() {
		return "", errCollaborationAdmissionHorizon
	}
	ticket, err := collaborationOpaqueID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(boot, now)
	if _, ok := s.admissions[participant]; !ok || len(s.tickets) >= s.limit*2 {
		return "", errCollaborationAdmission
	}
	ttl := 15 * time.Second
	if remaining := admission.currentDeadline() - now; remaining < ttl {
		ttl = remaining
	}
	s.tickets[ticket] = collaborationHumanJoinTicket{participant: participant, owner: owner, boot: boot, deadline: now + ttl}
	return ticket, nil
}
