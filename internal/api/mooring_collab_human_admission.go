package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCollaborationAdmission = errors.New("collaboration admission unavailable")

type collaborationHumanBindingKey struct{}
type collaborationHumanBinding struct {
	registry    *collaborationHumanRegistry
	participant string
	operation   *collaborationHumanOperation
}

// A bound caller is not enough: the complete store operation must retain its
// ingress lease through commit and bounded response emission. Escaped contexts
// become unusable when the callback returns.
type collaborationHumanOperation struct{ active atomic.Bool }

// Process-local capabilities deliberately do not survive a Go host restart or
// database restore. Canonical SQL policy remains the authority; these records
// preserve the ORIGINAL binding rather than recapturing it during reconnect.
// No HTTP/WS route installs this inactive component.
type collaborationHumanAdmission struct {
	source               auth.OriginalCredential
	ingress              collaborationIngressStamp
	member               string
	memberRevision       int64
	permission           string
	session, participant string
	write                bool
	boot                 string
	deadline             time.Duration
}

type collaborationHumanJoinTicket struct {
	participant string
	owner       mooring.OwnerLease
	boot        string
	deadline    time.Duration
}

type collaborationHumanRegistry struct {
	// Acquired BEFORE the SQL writer and held through complete bounded host
	// operations. Replacement drains those operations before changing bindings.
	runtimeMu       sync.RWMutex
	mu              sync.Mutex
	lifecycle       collaborationIngressStamp
	bindingID       string
	replacing       bool
	ownerGeneration int64
	fence           *collaborationIngressFence
	pages           *PageHandler
	owners          *mooring.OwnerStore
	runtime         *mooring.Runtime
	targetAdapter   mooring.TargetBatchAdapter
	readPolicy      string
	clock           mooring.OwnerClock
	scope           mooring.SessionKey
	versions        mooring.RuntimeVersions
	sessionID       string
	limit           int
	reservations    int
	admissions      map[string]collaborationHumanAdmission
	tickets         map[string]collaborationHumanJoinTicket
}

func newCollaborationHumanRegistry(enabled bool, pages *PageHandler, owners *mooring.OwnerStore, runtime *mooring.Runtime, clock mooring.OwnerClock, fence *collaborationIngressFence, scope mooring.SessionKey, versions mooring.RuntimeVersions, limit int) (*collaborationHumanRegistry, error) {
	return newCollaborationHumanRegistryPolicy(enabled, pages, owners, runtime, clock, fence, scope, versions, limit, "")
}

// Causal is a distinct host-configured capability, never fallback after a
// failed strict factory or a version/profile supplied by a participant.
func newCollaborationCausalHumanRegistry(enabled bool, pages *PageHandler, owners *mooring.OwnerStore, runtime *mooring.Runtime, clock mooring.OwnerClock, fence *collaborationIngressFence, scope mooring.SessionKey, versions mooring.RuntimeVersions, limit int) (*collaborationHumanRegistry, error) {
	return newCollaborationHumanRegistryPolicy(enabled, pages, owners, runtime, clock, fence, scope, versions, limit, mooring.CausalTargetReadPolicy)
}

func newCollaborationHumanRegistryPolicy(enabled bool, pages *PageHandler, owners *mooring.OwnerStore, runtime *mooring.Runtime, clock mooring.OwnerClock, fence *collaborationIngressFence, scope mooring.SessionKey, versions mooring.RuntimeVersions, limit int, policy string) (*collaborationHumanRegistry, error) {
	if !enabled || fence == nil || pages == nil || pages.db == nil || owners == nil || clock == nil || limit < 1 || limit > 1000 || scope.ApplicationID != "wordcraft" {
		return nil, errCollaborationAdmission
	}
	// Only an actual negotiated host-child runtime can establish this binding.
	// A copied RuntimeReady body or client-selected version strings cannot.
	var adapter mooring.TargetBatchAdapter
	var err error
	switch policy {
	case "":
		adapter, err = mooring.NewRPCPrepareAdapter(runtime, scope, versions)
	case mooring.CausalTargetReadPolicy:
		adapter, err = mooring.NewCausalRPCPrepareAdapter(runtime, scope, versions)
	default:
		return nil, errCollaborationAdmission
	}
	if err != nil {
		return nil, err
	}
	if _, _, err := clock.Now(); err != nil {
		return nil, err
	}
	session, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	lifecycle, err := collaborationHumanLifecycleStamp(fence)
	if err != nil {
		return nil, err
	}
	return &collaborationHumanRegistry{lifecycle: lifecycle, bindingID: session, targetAdapter: adapter, readPolicy: policy, fence: fence, pages: pages, owners: owners, runtime: runtime, clock: clock, scope: scope, versions: versions, sessionID: session, limit: limit, admissions: make(map[string]collaborationHumanAdmission), tickets: make(map[string]collaborationHumanJoinTicket)}, nil
}

func collaborationOpaqueID() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func (s *collaborationHumanRegistry) sweepLocked(boot string, now time.Duration) {
	for key, value := range s.admissions {
		if value.boot != boot || now >= value.deadline {
			delete(s.admissions, key)
		}
	}
	for key, value := range s.tickets {
		_, admitted := s.admissions[value.participant]
		if !admitted || value.boot != boot || now >= value.deadline {
			delete(s.tickets, key)
		}
	}
	// Never remove or acknowledge persisted delivery gates here. Outstanding
	// gate expiry evidence belongs to the SQL revocation barrier, even after a
	// disconnected participant disappears from this registry.
}

// issue captures an existing document admission after actual human HTTP auth.
// It does not initialize documents or accept agent provenance. The host owns
// the supplied runtime lease; neither it nor this method is a public endpoint.
func (s *collaborationHumanRegistry) issue(ctx context.Context, owner mooring.OwnerLease, write bool) (string, collaborationHumanAdmission, error) {
	var empty collaborationHumanAdmission
	unlock, err := s.acquireRuntimeOperation()
	if err != nil {
		return "", empty, err
	}
	defer unlock()
	ingress, err := s.fence.capture(ctx)
	if err != nil {
		return "", empty, err
	}
	release, err := s.fence.acquire(ctx, ingress)
	if err != nil {
		return "", empty, err
	}
	defer release()
	boot, now, err := s.clock.Now()
	if err != nil || now < 0 || now > time.Duration(math.MaxInt64)-5*time.Minute || owner.Key != s.scope {
		return "", empty, errCollaborationAdmission
	}
	if !mooring.ValidWireIdentifier(boot) {
		return "", empty, errCollaborationAdmission
	}
	// Never hold this mutex while waiting for the SQL writer: commit authority
	// takes that writer first, then resolves an admission from this registry.
	s.mu.Lock()
	s.sweepLocked(boot, now)
	if s.ownerGeneration != 0 && s.ownerGeneration != owner.Generation {
		s.mu.Unlock()
		return "", empty, errCollaborationAdmission
	}
	if len(s.admissions)+s.reservations >= s.limit || len(s.tickets)+s.reservations >= s.limit*2 {
		s.mu.Unlock()
		return "", empty, errCollaborationAdmission
	}
	s.reservations++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.reservations--; s.mu.Unlock() }()
	participant, err := collaborationOpaqueID()
	if err != nil {
		return "", empty, err
	}
	ticket, err := collaborationOpaqueID()
	if err != nil {
		return "", empty, err
	}
	tx, err := s.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return "", empty, err
	}
	defer tx.Rollback()
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return "", empty, err
	}
	intent := "read"
	if write {
		intent = "write"
	}
	proof, err := s.pages.authorizeCollaborationHumanPage(ctx, tx, s.scope, intent)
	if err != nil {
		return "", empty, err
	}
	source := auth.OriginalCredentialFromContext(ctx)
	if source == nil || !mooring.ValidWireIdentifier(proof.memberID) {
		return "", empty, errCollaborationAdmission
	}
	ttl := 5 * time.Minute
	if !proof.credentialExpiry.IsZero() && time.Until(proof.credentialExpiry) < ttl {
		ttl = time.Until(proof.credentialExpiry)
	}
	if ttl < time.Millisecond {
		return "", empty, errCollaborationAdmission
	}
	// The same opaque identity is scoped by this registry; never a user-supplied
	// name or a guessed stable user ID. A new admission gets a new participant.
	admission := collaborationHumanAdmission{ingress: ingress, source: *source, member: proof.memberID, memberRevision: proof.memberRevision, permission: proof.permissionVersion, session: s.sessionID, participant: participant, write: write, boot: boot, deadline: now + ttl}
	if err = s.owners.Check(ctx, tx, owner); err != nil {
		return "", empty, err
	}
	if err = tx.Commit(); err != nil {
		return "", empty, err
	}
	currentBoot, currentNow, err := s.clock.Now()
	if err != nil || currentBoot != boot || currentNow < now || currentNow >= admission.deadline || (!proof.credentialExpiry.IsZero() && !time.Now().Before(proof.credentialExpiry)) {
		return "", empty, errCollaborationAdmission
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.admissions[participant]; exists {
		return "", empty, errCollaborationAdmission
	}
	if _, exists := s.tickets[ticket]; exists {
		return "", empty, errCollaborationAdmission
	}
	s.admissions[participant] = admission
	s.ownerGeneration = owner.Generation
	ticketTTL := 15 * time.Second
	if remaining := admission.deadline - currentNow; remaining < ticketTTL {
		ticketTTL = remaining
	}
	s.tickets[ticket] = collaborationHumanJoinTicket{participant: participant, owner: owner, boot: boot, deadline: currentNow + ticketTTL}
	return ticket, admission, nil
}

func (s *collaborationHumanRegistry) capturedContext(ctx context.Context, admission collaborationHumanAdmission) context.Context {
	// Called only after this registry has resolved a host-issued capability.
	// Do not adopt a fresh source from a later HTTP request or runtime message.
	u := &AuthUser{ID: admission.source.UserID, OriginalCredential: &admission.source}
	if admission.source.Kind == AuthKindSession {
		u.SessionID = admission.source.ID
	} else {
		u.TokenID = admission.source.ID
	}
	ctx = context.WithValue(ctx, ctxUser, u)
	ctx = context.WithValue(ctx, ctxAuthKind, admission.source.Kind)
	return auth.WithOriginalCredential(ctx, admission.source)
}

func (s *collaborationHumanRegistry) checkCaptured(ctx context.Context, tx *sql.Tx, admission collaborationHumanAdmission, write bool, requiredRemaining time.Duration) (string, error) {
	// This local check is not a lease over the caller's later commit/send.
	// Entrypoints must hold an outer ingress lease around the complete operation.
	release, err := s.fence.acquireOriginal(admission.ingress)
	if err != nil {
		return "", err
	}
	defer release()
	boot, now, err := s.clock.Now()
	if err != nil || boot != admission.boot || now < 0 || now >= admission.deadline || (write && !admission.write) {
		return "", errCollaborationAdmission
	}
	intent := "read"
	if write {
		intent = "write"
	}
	proof, err := s.pages.authorizeCollaborationHumanPage(s.capturedContext(ctx, admission), tx, s.scope, intent)
	if err != nil {
		return "", err
	}
	if proof.permissionVersion != admission.permission || proof.memberID != admission.member || proof.memberRevision != admission.memberRevision {
		return "", errCollaborationAdmission
	}
	boot, now, err = s.clock.Now()
	if err != nil || boot != admission.boot || now < 0 || now >= admission.deadline || admission.deadline-now < requiredRemaining {
		return "", errCollaborationAdmission
	}
	if !proof.credentialExpiry.IsZero() && time.Until(proof.credentialExpiry) < requiredRemaining {
		return "", errCollaborationAdmission
	}
	return proof.permissionVersion, nil
}

// bindHumanRequest uses authenticated transport identity, never a request-body
// user ID. A refreshed HTTP credential cannot extend the original admission.
func (s *collaborationHumanRegistry) bindHumanRequest(r *http.Request, participant string) (context.Context, error) {
	if r == nil {
		return nil, errCollaborationAdmission
	}
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		return nil, errCollaborationAdmission
	}
	u := UserFromContext(r.Context())
	if u == nil {
		return nil, errCollaborationAdmission
	}
	s.mu.Lock()
	admission, ok := s.admissions[participant]
	s.mu.Unlock()
	if !ok || u.ID != admission.source.UserID || AuthKindFromContext(r.Context()) != admission.source.Kind {
		return nil, errCollaborationAdmission
	}
	if admission.source.Kind == AuthKindSession && (u.SessionID != admission.source.ID || u.TokenID != "") {
		return nil, errCollaborationAdmission
	}
	if admission.source.Kind == AuthKindCLIToken && (u.TokenID != admission.source.ID || u.SessionID != "") {
		return nil, errCollaborationAdmission
	}
	release, err := s.fence.acquire(r.Context(), admission.ingress)
	if err != nil {
		return nil, err
	}
	release()
	return context.WithValue(r.Context(), collaborationHumanBindingKey{}, collaborationHumanBinding{registry: s, participant: participant}), nil
}

// runHumanOperation is the only batch/status entry wrapper. fn includes the
// entire store transaction AND response decision, not just authorization. It
// acquires the fence before the SQL writer and never holds the registry mutex
// while waiting for either. Long-lived streams acquire per bounded frame.
func (s *collaborationHumanRegistry) runHumanOperation(r *http.Request, participant string, fn func(context.Context) error) error {
	if fn == nil {
		return errCollaborationAdmission
	}
	unlock, err := s.acquireRuntimeOperation()
	if err != nil {
		return err
	}
	defer unlock()
	ctx, err := s.bindHumanRequest(r, participant)
	if err != nil {
		return err
	}
	s.mu.Lock()
	admission, ok := s.admissions[participant]
	s.mu.Unlock()
	if !ok {
		return errCollaborationAdmission
	}
	release, err := s.fence.acquire(ctx, admission.ingress)
	if err != nil {
		return err
	}
	defer release()
	operation := &collaborationHumanOperation{}
	operation.active.Store(true)
	defer operation.active.Store(false)
	ctx = context.WithValue(ctx, collaborationHumanBindingKey{}, collaborationHumanBinding{registry: s, participant: participant, operation: operation})
	return fn(ctx)
}

// Plug directly into BatchStore.Authority so status/unknown recovery keeps the
// store's SQL writer linearization. No loose status cache or historical author
// write check replaces the current reader admission.
func (s *collaborationHumanRegistry) authorizeBatchAtBinding(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess, runtime *mooring.Runtime, bindingID string) error {
	if !s.matchesRuntimeBinding(runtime, bindingID) {
		return errCollaborationAdmission
	}
	binding, ok := ctx.Value(collaborationHumanBindingKey{}).(collaborationHumanBinding)
	if !ok || binding.registry != s || binding.operation == nil || !binding.operation.active.Load() || tx == nil || access.Scope != s.scope {
		return errCollaborationAdmission
	}
	s.mu.Lock()
	admission, ok := s.admissions[binding.participant]
	s.mu.Unlock()
	if !ok || (access.Participant != "" && access.Participant != admission.participant) {
		return errCollaborationAdmission
	}
	write := false
	switch access.Intent {
	case "outcome", "read_checkpoint":
	case "write", "task":
		write = true
	default:
		return errCollaborationAdmission
	}
	if access.Operation != nil && access.Operation.Participant != admission.participant {
		return errCollaborationAdmission
	}
	_, err := s.checkCaptured(ctx, tx, admission, write, 0)
	return err
}

type collaborationHumanJoined struct {
	admission collaborationHumanAdmission
	gate      mooring.DeliveryGrant
	// Private host handle only; the participant receives the bounded grant,
	// never an owner token, datastore or authority callback.
	delivery *mooring.DeliveryStore
}

// join publishes no content. Only a successfully opened, current SQL gate may
// become SessionJoined. The Rust dispatcher must still enforce its own
// suspend-aware deadline, measured from the FIRST request send, on every frame.
func (s *collaborationHumanRegistry) join(ctx context.Context, runtime *mooring.Runtime, owner mooring.OwnerLease, ticket, nonce string) (collaborationHumanJoined, error) {
	var empty collaborationHumanJoined
	unlock, err := s.acquireRuntimeOperation()
	if err != nil {
		return empty, err
	}
	defer unlock()
	admission, err := s.consume(runtime, owner, ticket, nonce)
	if err != nil {
		return empty, err
	}
	release, err := s.fence.acquireOriginal(admission.ingress)
	if err != nil {
		return empty, err
	}
	defer release()
	const ttl = 2 * time.Second
	s.mu.Lock()
	bindingID := s.bindingID
	s.mu.Unlock()
	authority := func(ctx context.Context, tx *sql.Tx, access mooring.DeliveryAccess) (string, error) {
		if !s.matchesRuntimeBinding(runtime, bindingID) {
			return "", errCollaborationAdmission
		}
		if access.Scope != s.scope || access.Participant != admission.participant || (access.Intent != "connect" && access.Intent != "renew") {
			return "", errCollaborationAdmission
		}
		permission, err := s.checkCaptured(ctx, tx, admission, false, ttl)
		if err != nil {
			return "", err
		}
		// The fixed delivery TTL must also fit the CURRENT owner horizon.
		// A positive check just before expiry cannot issue two fresh seconds.
		var boot string
		var expires int64
		if err := tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns FROM mooring_session_owners WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND generation=?`, s.scope.ArtifactID, s.scope.WorkspaceID, s.scope.ApplicationID, owner.OwnerID, owner.Generation).Scan(&boot, &expires); err != nil {
			return "", err
		}
		currentBoot, now, err := s.clock.Now()
		if err != nil || boot != currentBoot || now < 0 || int64(now) >= expires || expires-int64(now) < int64(ttl) {
			return "", errCollaborationAdmission
		}
		return permission, nil
	}
	delivery := mooring.NewDeliveryStore(s.owners, true, authority, ttl, s.limit*4)
	gate, err := delivery.Open(ctx, owner, admission.participant, nonce)
	if err != nil {
		return empty, err
	}
	return collaborationHumanJoined{admission: admission, gate: gate, delivery: delivery}, nil
}

// consume burns a ticket atomically BEFORE any expensive join work. A failed
// join needs a fresh ticket; no retry can rebind this ticket to another nonce,
// runtime, owner generation or gate. Its caller must be the inherited runtime
// dispatcher and must open/recheck the SQL delivery gate before SessionJoined.
func (s *collaborationHumanRegistry) consume(runtime *mooring.Runtime, owner mooring.OwnerLease, ticket, nonce string) (collaborationHumanAdmission, error) {
	var empty collaborationHumanAdmission
	if runtime == nil || len(ticket) != 64 || !mooring.ValidWireIdentifier(nonce) {
		return empty, errCollaborationAdmission
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replacing || runtime != s.runtime {
		return empty, errCollaborationAdmission
	}
	issued, exists := s.tickets[ticket]
	if !exists {
		return empty, errCollaborationAdmission
	}
	delete(s.tickets, ticket)
	boot, now, err := s.clock.Now()
	if err != nil || now < 0 {
		return empty, errCollaborationAdmission
	}
	s.sweepLocked(boot, now)
	admission, admitted := s.admissions[issued.participant]
	if !admitted || issued.owner != owner || issued.boot != boot || now >= issued.deadline {
		return empty, errCollaborationAdmission
	}
	release, err := s.fence.acquireOriginal(admission.ingress)
	if err != nil {
		return empty, err
	}
	release()
	return admission, nil
}
