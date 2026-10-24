package api

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// This cell belongs to exactly one initialized store. Its Authority closure is
// immutable; activation installs one binding, never a replacement configuration.
// No public route constructs it.
type collaborationSessionStore struct {
	operations sync.RWMutex
	mu         sync.Mutex
	pages      *PageHandler
	owners     *mooring.OwnerStore
	fence      *collaborationIngressFence
	lifecycle  collaborationIngressStamp
	scope      mooring.SessionKey
	runtime    *mooring.SessionRuntime
	adapter    *mooring.SessionCausalAdapter
	store      *mooring.BatchStore
	binding    bool
	closed     bool
	activation *mooring.SessionActivation
	authority  mooring.BatchAuthority
}
type collaborationSessionStartupKey struct{}
type collaborationSessionStartup struct {
	cell   *collaborationSessionStore
	intent string
	owner  mooring.OwnerLease
	active atomic.Bool
}

func newCollaborationSessionStore(enabled bool, pages *PageHandler, owners *mooring.OwnerStore, fence *collaborationIngressFence, runtime *mooring.SessionRuntime, scope mooring.SessionKey, versions mooring.RuntimeVersions) (*collaborationSessionStore, error) {
	if !enabled || pages == nil || pages.db == nil || owners == nil || fence == nil || scope.ApplicationID != "wordcraft" {
		return nil, errCollaborationAdmission
	}
	adapter, err := mooring.NewSessionCausalAdapter(runtime, scope, versions)
	if err != nil {
		return nil, err
	}
	lifecycle, err := collaborationHumanLifecycleStamp(fence)
	if err != nil {
		return nil, err
	}
	c := &collaborationSessionStore{pages: pages, owners: owners, fence: fence, lifecycle: lifecycle, scope: scope, runtime: runtime, adapter: adapter}
	c.store = mooring.NewBatchStore(pages.db, mooring.BatchStoreConfig{Enabled: true, Owners: owners, Authority: c.authorize, Limits: mooring.DefaultBatchLimits(), CheckpointIdentity: &mooring.BatchCheckpointIdentityConfig{Enabled: true, Adapter: adapter, MaxMetadataBytes: mooring.MaxPrivatePrepareRPCFrame}, Targets: &mooring.TargetStoreConfig{Enabled: true, ReadPolicy: mooring.CausalTargetReadPolicy, HistoryBudget: mooring.DefaultBatchHistoryBudget(), Adapter: adapter, Limits: mooring.DefaultBatchTargetLimits()}})
	return c, nil
}

func (c *collaborationSessionStore) authorize(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
	if c == nil || tx == nil || access.Scope != c.scope || !access.FromStore(c.store) {
		return errCollaborationAdmission
	}
	c.mu.Lock()
	closed, binding, activation, authority := c.closed, c.binding, c.activation, c.authority
	c.mu.Unlock()
	if closed || binding {
		return errCollaborationAdmission
	}
	if activation != nil {
		if authority == nil {
			return errCollaborationAdmission
		}
		if err := activation.Check(ctx, tx, c.store); err != nil {
			return err
		}
		return authority(ctx, tx, access)
	}
	operation, ok := ctx.Value(collaborationSessionStartupKey{}).(*collaborationSessionStartup)
	if !ok || operation == nil || operation.cell != c || !operation.active.Load() || operation.intent != access.Intent || (access.Intent != "initialize" && access.Intent != "read_checkpoint") || access.Operation != nil || access.Participant != "" || access.TaskID != "" {
		return errCollaborationAdmission
	}
	if !c.startupRuntimeMatches(operation.owner) {
		return errCollaborationAdmission
	}
	if err := c.owners.Check(ctx, tx, operation.owner); err != nil {
		return err
	}
	intent := operation.intent
	if intent == "initialize" && access.IsCommitFence() {
		intent = "initialize_commit"
	}
	_, err := c.pages.authorizeCollaborationHumanPage(ctx, tx, c.scope, intent)
	return err
}

// Startup authority is tied to this actual process and its exact captured owner.
// It grants no participant delivery rights and cannot survive process death.
func (c *collaborationSessionStore) startupRuntimeMatches(owner mooring.OwnerLease) bool {
	if c == nil || c.runtime == nil || c.runtime.Done() == nil || owner.Key != c.scope || owner.OwnerID != c.runtime.RuntimeRef() {
		return false
	}
	select {
	case <-c.runtime.Done():
		return false
	default:
		return true
	}
}

// The original authenticated ingress/source survives for exactly one startup
// operation. Its private purpose prevents initialization and read contexts from
// being exchanged, and escaped contexts are unusable after this call returns.
func (c *collaborationSessionStore) runStartup(r *http.Request, owner mooring.OwnerLease, intent string, call func(context.Context) error) error {
	if c == nil || r == nil || call == nil || (intent != "initialize" && intent != "read_checkpoint") || !c.startupRuntimeMatches(owner) || !c.operations.TryRLock() {
		return errCollaborationAdmission
	}
	defer c.operations.RUnlock()
	stamp, err := c.fence.capture(r.Context())
	if err != nil || stamp != c.lifecycle {
		return errCollaborationAdmission
	}
	release, err := c.fence.acquire(r.Context(), stamp)
	if err != nil {
		return err
	}
	defer release()
	c.mu.Lock()
	ready := !c.closed && !c.binding && c.activation == nil
	c.mu.Unlock()
	if !ready {
		return errCollaborationAdmission
	}
	operation := &collaborationSessionStartup{cell: c, intent: intent, owner: owner}
	operation.active.Store(true)
	defer operation.active.Store(false)
	ctx := context.WithValue(r.Context(), collaborationSessionStartupKey{}, operation)
	return call(ctx)
}

func (c *collaborationSessionStore) initialize(r *http.Request, owner mooring.OwnerLease, checkpoint []byte) error {
	return c.runStartup(r, owner, "initialize", func(ctx context.Context) error {
		return c.store.Initialize(ctx, owner, checkpoint)
	})
}

// Read the exact retained wrapper and target inventory under the immutable
// original store authority. The generic store checks source and owner again
// before its FULL writer finishes. No caller-built wrapper becomes proof.
func (c *collaborationSessionStore) startupCheckpoint(r *http.Request, owner mooring.OwnerLease) (mooring.FullCheckpointSnapshot, error) {
	var snapshot mooring.FullCheckpointSnapshot
	err := c.runStartup(r, owner, "read_checkpoint", func(ctx context.Context) error {
		var err error
		snapshot, err = c.store.ReadFullCheckpoint(ctx, owner)
		return err
	})
	if err != nil {
		return mooring.FullCheckpointSnapshot{}, err
	}
	return snapshot, nil
}

// Binding never holds mu while waiting for SQL: authority is invoked by a
// writer and also needs mu. The operation lock drains startup before its writer.
func (c *collaborationSessionStore) bind(ctx context.Context, activation *mooring.SessionActivation, owner mooring.OwnerLease, authority mooring.BatchAuthority) error {
	if c == nil || activation == nil || authority == nil || owner.Key != c.scope || activation.Scope() != c.scope || !c.operations.TryLock() {
		return errCollaborationAdmission
	}
	defer c.operations.Unlock()
	release, err := c.fence.acquireOriginal(c.lifecycle)
	if err != nil {
		return err
	}
	defer release()
	runtime, err := activation.SessionRuntime()
	if err != nil || runtime != c.runtime {
		return errCollaborationAdmission
	}
	c.mu.Lock()
	if c.closed || c.binding || c.activation != nil {
		c.mu.Unlock()
		return errCollaborationAdmission
	}
	c.binding = true
	c.mu.Unlock()
	success := false
	defer func() {
		c.mu.Lock()
		c.binding = false
		if !success {
			c.closed = true
		}
		c.mu.Unlock()
	}()
	tx, err := c.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	var synchronous int
	if err = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous < 2 {
		return errCollaborationAdmission
	}
	if err = activation.Check(ctx, tx, c.store); err != nil {
		return err
	}
	if err = c.owners.Check(ctx, tx, owner); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if _, err = activation.SessionRuntime(); err != nil {
		return err
	}
	c.mu.Lock()
	c.activation, c.authority = activation, authority
	c.mu.Unlock()
	success = true
	return nil
}

func (c *collaborationSessionStore) live() (*mooring.SessionRuntime, error) {
	c.mu.Lock()
	activation, closed, binding := c.activation, c.closed, c.binding
	c.mu.Unlock()
	if activation == nil || closed || binding {
		return nil, errCollaborationAdmission
	}
	return activation.SessionRuntime()
}
func (c *collaborationSessionStore) check(ctx context.Context, tx *sql.Tx) error {
	c.mu.Lock()
	activation, closed, binding := c.activation, c.closed, c.binding
	c.mu.Unlock()
	if activation == nil || closed || binding {
		return errCollaborationAdmission
	}
	return activation.Check(ctx, tx, c.store)
}

// The constructor derives session identity and versions only from the actual
// opaque startup proof. It returns nothing until the original store is bound.
func newCollaborationSessionHumanRegistry(ctx context.Context, c *collaborationSessionStore, activation *mooring.SessionActivation, owner mooring.OwnerLease, clock mooring.OwnerClock, limit int) (*collaborationHumanRegistry, error) {
	if c == nil || activation == nil || clock == nil || limit < 1 || limit > 1000 || activation.Scope() != c.scope || !mooring.ValidWireIdentifier(activation.SessionID()) {
		return nil, errCollaborationAdmission
	}
	runtime, err := activation.SessionRuntime()
	if err != nil || runtime != c.runtime {
		return nil, errCollaborationAdmission
	}
	if _, _, err = clock.Now(); err != nil {
		return nil, err
	}
	id, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	s := &collaborationHumanRegistry{sessionStore: c, lifecycle: c.lifecycle, bindingID: id, fence: c.fence, pages: c.pages, owners: c.owners, runtime: runtime, targetAdapter: c.adapter, readPolicy: mooring.CausalTargetReadPolicy, clock: clock, scope: c.scope, versions: activation.Versions(), sessionID: activation.SessionID(), ownerGeneration: owner.Generation, limit: limit, admissions: make(map[string]collaborationHumanAdmission), tickets: make(map[string]collaborationHumanJoinTicket)}
	authority := func(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
		return s.authorizeBatchAtBinding(ctx, tx, access, runtime, id)
	}
	if err = c.bind(ctx, activation, owner, authority); err != nil {
		return nil, err
	}
	return s, nil
}
