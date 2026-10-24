package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func (m *collaborationHTTPManager) document(r *http.Request, key mooring.SessionKey, checkpoint []byte) (*collaborationHTTPDocument, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errCollaborationHTTP
	}
	if old := m.docs[key]; old != nil {
		m.mu.Unlock()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-old.ready:
		}
		if old.err != nil {
			return nil, old.err
		}
		if len(checkpoint) != 0 {
			return nil, errCollaborationHTTP
		}
		return old, nil
	}
	if len(m.docs) >= collaborationHTTPDocLimit {
		m.mu.Unlock()
		return nil, errCollaborationHTTP
	}
	ctx, cancel := context.WithCancel(m.ctx)
	d := &collaborationHTTPDocument{manager: m, key: key, ready: make(chan struct{}), ctx: ctx, cancel: cancel, connections: make(map[string]*collaborationHTTPConnection)}
	d.originals = newCollaborationLiveOriginals(d)
	m.docs[key] = d
	m.mu.Unlock()
	d.err = d.bootstrap(r, checkpoint)
	close(d.ready)
	if d.err != nil {
		d.close()
		m.mu.Lock()
		if m.docs[key] == d {
			delete(m.docs, key)
		}
		m.mu.Unlock()
		return nil, d.err
	}
	return d, nil
}
func (d *collaborationHTTPDocument) bootstrap(r *http.Request, checkpoint []byte) error {
	m := d.manager
	versions := mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	runtime, e := mooring.StartSessionRuntime(d.ctx, mooring.SessionRuntimeConfig{Runtime: mooring.RuntimeConfig{Enabled: true, Binary: m.config.Binary, Versions: versions, ControlProfile: mooring.PrivateSessionProfile}, Scope: d.key})
	if e != nil {
		return e
	}
	d.cell, e = newCollaborationSessionStore(true, m.pages, m.owners, m.fence, runtime, d.key, versions)
	if e != nil {
		_ = runtime.Close()
		return e
	}
	session, e := collaborationOpaqueID()
	if e != nil {
		return e
	}
	scope := protocol.Scope{WorkspaceID: protocol.Identifier(d.key.WorkspaceID), ArtifactID: protocol.Identifier(d.key.ArtifactID), ApplicationID: "wordcraft"}
	tuple := protocol.Versions{ProtocolVersion: versions.Protocol, ModelVersion: versions.Model, EngineVersion: versions.Engine}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	stop := context.AfterFunc(d.ctx, cancel)
	defer stop()
	r = r.WithContext(ctx)
	open := protocol.SessionOpen{Type: "session_open", RequestID: protocol.Identifier(session + "-open"), ControlProfile: mooring.PrivateSessionProfile, Scope: scope, SessionID: protocol.Identifier(session), RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Versions: tuple, RequiredCapabilities: []protocol.SessionOpenRequiredCapabilitiesItem{"checkpoint_identity_v1"}}
	opened := make(chan error, 1)
	finishOpen := beginBackgroundWork()
	go func() { defer finishOpen(); _, err := runtime.Open(ctx, open); opened <- err }()
	// Only this bootstrap owns NextControl before activation. The one pump takes
	// over afterwards; no concurrent reader or synthetic Opened confirmation.
	token, e := runtime.NextControl(ctx)
	if e != nil {
		return e
	}
	raw, e := token.Frame()
	if e != nil {
		return e
	}
	var acquire protocol.OwnerAcquire
	if json.Unmarshal(raw, &acquire) != nil || acquire.Type != "owner_acquire" || acquire.Scope != scope || acquire.RuntimeRef != open.RuntimeRef {
		return errCollaborationHTTP
	}
	d.owner, e = m.owners.Acquire(ctx, d.key, runtime.RuntimeRef())
	if e != nil {
		return e
	}
	if len(checkpoint) != 0 {
		if e = d.cell.initialize(r, d.owner, checkpoint); e != nil {
			return e
		}
	}
	snapshot, e := d.cell.startupCheckpoint(r, d.owner)
	if e != nil {
		return e
	}
	var permission string
	if e = m.pages.db.QueryRowContext(ctx, `SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=? AND deleted_at IS NULL`, d.key.WorkspaceID).Scan(&permission); e != nil {
		return e
	}
	remaining, e := d.ownerRemaining(ctx)
	if e != nil {
		return e
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: scope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(d.owner.Generation, 10)), Token: d.owner.Token}, LeaseDurationMs: protocol.PositiveCounter(strconv.FormatInt(remaining, 10)), Revision: snapshot.Checkpoint.Revision, RenewalNonce: acquire.RenewalNonce, PermissionVersion: protocol.Counter(permission)}
	raw, e = json.Marshal(grant)
	if e != nil {
		return e
	}
	if e = runtime.ReplyControl(ctx, token, raw); e != nil {
		return e
	}
	targets := make([]protocol.TargetVersion, 0, len(snapshot.Targets))
	for _, v := range snapshot.Targets {
		targets = append(targets, protocol.TargetVersion{TargetID: protocol.Identifier(v.TargetID), Revision: protocol.Counter(v.Revision), StructureRevision: protocol.Counter(v.StructureRevision), Deleted: v.Deleted})
	}
	restore := protocol.SessionRestore{Type: "session_restore", RequestID: protocol.Identifier(session + "-restore"), ControlProfile: mooring.PrivateSessionProfile, Scope: scope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: grant.Fence, Versions: tuple, Checkpoint: snapshot.Checkpoint, Targets: targets}
	if _, e = runtime.Restore(ctx, restore); e != nil {
		return e
	}
	if e = <-opened; e != nil {
		return e
	}
	d.activation, e = runtime.Activate(ctx, d.cell.store, d.owner, restore)
	if e != nil {
		return e
	}
	d.humans, e = newCollaborationSessionHumanRegistry(ctx, d.cell, d.activation, d.owner, m.clock, collaborationHTTPParticipantLimit, m.config.DeliveryTTL)
	if e != nil {
		return e
	}
	d.submissions, e = newCollaborationSubmissionRegistry(d.humans)
	if e != nil {
		return e
	}
	d.committer, e = newCollaborationSessionCommitterWithOriginals(ctx, d.cell, d.submissions, d.owner, d.originals)
	if e != nil {
		return e
	}
	d.pump, e = newCollaborationSessionPump(runtime, collaborationSessionPumpHandlers{Submit: d.submit, Status: d.status, Renew: d.renewOwner, Ticket: d.ticket, Delivery: d.renewDelivery, DeliveryGuard: d.deliveryGuard, Output: d.output})
	if e != nil {
		return e
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errCollaborationHTTP
	}
	m.workers.Add(1)
	m.mu.Unlock()
	finish := beginBackgroundWork()
	go func() { defer m.workers.Done(); defer finish(); _ = d.pump.run(d.ctx); d.close() }()
	return nil
}
func (d *collaborationHTTPDocument) close() {
	if d == nil {
		return
	}
	d.cancel()
	<-d.ready
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.cancel()
	connections := make([]*collaborationHTTPConnection, 0, len(d.connections))
	for _, c := range d.connections {
		connections = append(connections, c)
	}
	d.mu.Unlock()
	for _, c := range connections {
		c.close()
	}
	if d.cell != nil && d.cell.runtime != nil {
		_ = d.cell.runtime.Close()
	}
	// Release only after actual runtime death. Pending delivery expiry evidence
	// remains in SQL; no fabricated blocked acknowledgement or barrier completion.
	if d.cell != nil && d.cell.runtime != nil {
		select {
		case <-d.cell.runtime.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = d.manager.owners.Release(ctx, d.owner)
			d.manager.mu.Lock()
			if d.manager.docs[d.key] == d {
				delete(d.manager.docs, d.key)
			}
			d.manager.mu.Unlock()
		default:
		}
	}
}
func (d *collaborationHTTPDocument) renewOwner(ctx context.Context, q protocol.OwnerRenew) (protocol.OwnerGrant, error) {
	var empty protocol.OwnerGrant
	scope := protocol.Scope{WorkspaceID: protocol.Identifier(d.key.WorkspaceID), ArtifactID: protocol.Identifier(d.key.ArtifactID), ApplicationID: "wordcraft"}
	fence := protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(d.owner.Generation, 10)), Token: d.owner.Token}
	if q.Scope != scope || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != fence {
		return empty, errCollaborationHTTP
	}
	if _, e := d.manager.owners.Renew(ctx, d.owner); e != nil {
		return empty, e
	}
	var revision, permission string
	tx, e := d.manager.pages.db.BeginTx(ctx, nil)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback()
	if e = d.cell.check(ctx, tx); e != nil {
		return empty, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT d.revision,CAST(w.authority_revision AS TEXT) FROM mooring_collab_documents d JOIN workspaces w ON w.id=d.workspace_id WHERE d.page_id=? AND d.workspace_id=? AND d.application_id='wordcraft' AND w.deleted_at IS NULL`, d.key.ArtifactID, d.key.WorkspaceID).Scan(&revision, &permission); e != nil {
		return empty, e
	}
	if !pumpCounterAtLeast(protocol.Counter(permission), q.PermissionVersion) {
		return empty, errCollaborationHTTP
	}
	if e = d.manager.owners.Check(ctx, tx, d.owner); e != nil {
		return empty, e
	}
	if e = tx.Commit(); e != nil {
		return empty, e
	}
	remaining, e := d.ownerRemaining(ctx)
	if e != nil {
		return empty, e
	}
	return protocol.OwnerGrant{Type: "owner_grant", RequestID: q.RequestID, Scope: scope, SessionID: q.SessionID, RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: fence, LeaseDurationMs: protocol.PositiveCounter(strconv.FormatInt(remaining, 10)), Revision: protocol.Counter(revision), RenewalNonce: q.RenewalNonce, PermissionVersion: protocol.Counter(permission)}, nil
}

// Never grant a fresh full TTL after startup or SQL work has consumed part of
// the actual retained owner lease. The dispatcher measures from first send.
func (d *collaborationHTTPDocument) ownerRemaining(ctx context.Context) (int64, error) {
	var boot string
	var expires int64
	if e := d.manager.pages.db.QueryRowContext(ctx, `SELECT boot_id,expires_ns FROM mooring_session_owners WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND generation=?`, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, d.owner.OwnerID, d.owner.Generation).Scan(&boot, &expires); e != nil {
		return 0, e
	}
	current, now, e := d.manager.clock.Now()
	if e != nil || boot != current || now < 0 || int64(now) >= expires {
		return 0, errCollaborationHTTP
	}
	remaining := (expires - int64(now)) / int64(time.Millisecond)
	if remaining < 1 || remaining > 60000 {
		return 0, errCollaborationHTTP
	}
	return remaining, nil
}
