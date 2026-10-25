package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

type collaborationLiveAgent struct {
	mu, callMu, deliveryMu sync.Mutex
	document               *collaborationHTTPDocument
	confirmed              collaborationAgentConfirmed
	ref, ticket            string
	participant            *collaborationAgentSubmissionParticipant
	pending                *mooring.SessionParticipantPending
	binding                *mooring.SessionParticipantBinding
	openID, joinID         protocol.Identifier
	delivery               *mooring.DeliveryStore
	gate                   mooring.DeliveryGrant
	ready                  chan struct{}
	joined, closed         bool
	retired                *mooring.SessionRetiredDelivery
}

func (d *collaborationHTTPDocument) agentParticipant(ref, participant, ticket string) *collaborationLiveAgent {
	if ref == "" && participant == "" && ticket == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.agents {
		a.mu.Lock()
		match := (ref == "" || a.ref == ref) && (participant == "" || a.participant != nil && a.participant.id == participant) && (ticket == "" || a.ticket == ticket)
		a.mu.Unlock()
		if match {
			return a
		}
	}
	return nil
}

func (c *collaborationAgentTaskController) lookup(r *http.Request) (*collaborationLiveAgent, error) {
	if r == nil || len(r.Header.Get("Authorization")) > 2048 {
		return nil, errCollaborationAgentAccess
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, errCollaborationAgentAccess
	}
	workspace, crew := InternalTokenWorkspaceFromContext(r.Context()), InternalTokenCrewFromContext(r.Context())
	ws, agent, run, ok := internaltoken.ValidateAgentRunToken(internaltoken.DeriveAgentRunKey(c.manager.pages.artifactMasterToken, workspace, crew), parts[1])
	if !ok || ws != workspace {
		return nil, errCollaborationAgentAccess
	}
	c.mu.Lock()
	a := c.confirmed[run]
	c.mu.Unlock()
	if a == nil || a.confirmed.agentID != agent || a.confirmed.crewID != crew || a.confirmed.key.WorkspaceID != ws || a.confirmed.store != c.manager.agentMandates {
		return nil, errCollaborationAgentAccess
	}
	return a, nil
}

// Every operation holds the original task ingress and original runtime lifetime.
// No HTTP user's context or freshly projected run principal is introduced.
func (a *collaborationLiveAgent) run(ctx context.Context, call func(context.Context) error) error {
	d := a.document
	if ctx == nil || call == nil || !d.cell.operations.TryRLock() {
		return errCollaborationAgentAccess
	}
	defer d.cell.operations.RUnlock()
	s := a.confirmed.store
	s.mu.Lock()
	entry, ok := s.attached[a.confirmed.mandate]
	s.mu.Unlock()
	if !ok || entry.binding.attempt.runID != a.confirmed.runID {
		return errCollaborationAgentAccess
	}
	release, err := s.fence.acquireOriginal(entry.binding.task.ingress)
	if err != nil {
		return err
	}
	defer release()
	if _, err = d.cell.live(); err != nil {
		return err
	}
	return call(ctx)
}

func (a *collaborationLiveAgent) allowed(ctx context.Context, tx *sql.Tx, intent string) (collaborationAgentPageAccess, error) {
	d := a.document
	if a.participant == nil || a.confirmed.store != d.manager.agentMandates || d.cell.check(ctx, tx) != nil || d.manager.owners.Check(ctx, tx, d.owner) != nil {
		return collaborationAgentPageAccess{}, errCollaborationAgentAccess
	}
	decision, err := a.confirmed.store.DecideCaptured(ctx, tx, a.participant.captured, d.key, intent)
	if err != nil {
		return collaborationAgentPageAccess{}, err
	}
	if decision.kind == collaborationAgentCanonicalPermissionChanged {
		return collaborationAgentPageAccess{}, errCollaborationAuthorChanged
	}
	if decision.kind != collaborationAgentAllowed {
		return collaborationAgentPageAccess{}, errCollaborationAgentAccess
	}
	return decision.access, nil
}

func (a *collaborationLiveAgent) ensure(ctx context.Context, r *http.Request) error {
	a.mu.Lock()
	joined, closed, started := a.joined, a.closed, a.pending != nil || a.ticket != ""
	a.mu.Unlock()
	if closed {
		return errCollaborationAgentAccess
	}
	if joined {
		return nil
	}
	if !started {
		p, err := a.document.submissions.admitAgent(ctx, r, a.confirmed.store, a.confirmed.mandate)
		if err != nil {
			return err
		}
		ticket, err := collaborationOpaqueID()
		if err != nil {
			return err
		}
		open, err := collaborationOpaqueID()
		if err != nil {
			return err
		}
		join, err := collaborationOpaqueID()
		if err != nil {
			return err
		}
		d := a.document
		packet, err := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: protocol.Identifier(join), Ticket: ticket, Versions: d.wireVersions()})
		if err != nil {
			return err
		}
		pending, err := d.cell.runtime.PrepareParticipantOpen(d.activation, protocol.SessionParticipantOpen{Type: "session_participant_open", RequestID: protocol.Identifier(open), ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(a.ref), Join: packet})
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.participant, a.ticket, a.openID, a.joinID, a.pending = p, ticket, protocol.Identifier(open), protocol.Identifier(join), pending
		a.mu.Unlock()
		if err = a.run(ctx, func(ctx context.Context) error {
			return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
				if _, e := a.allowed(ctx, tx, "task"); e != nil {
					return e
				}
				return d.cell.runtime.SendPreparedParticipantOpen(ctx, d.activation, pending)
			})
		}); err != nil {
			if d.cell.runtime.DiscardUnsentParticipantOpen(pending) != nil {
				d.cancel()
			}
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.document.ctx.Done():
		return errCollaborationAgentAccess
	case <-a.ready:
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.joined || a.closed {
		return errCollaborationAgentAccess
	}
	return nil
}

func (a *collaborationLiveAgent) ticketReply(ctx context.Context, token mooring.SessionControl, q protocol.TicketValidationRequest) error {
	d := a.document
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.pending == nil || a.delivery != nil || a.ticket != q.Ticket || q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != d.wireFence() {
		return errCollaborationAgentAccess
	}
	ttl := d.manager.config.DeliveryTTL
	authority := func(ctx context.Context, tx *sql.Tx, access mooring.DeliveryAccess) (string, error) {
		if access.Scope != d.key || access.Participant != a.participant.id || (access.Intent != "connect" && access.Intent != "renew") {
			return "", errCollaborationAgentAccess
		}
		proof, err := a.allowed(ctx, tx, "task")
		if err != nil {
			return "", err
		}
		if time.Until(proof.credentialExpiry) < ttl {
			return "", errCollaborationAgentAccess
		}
		var boot string
		var expires int64
		if err = tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns FROM mooring_session_owners WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND generation=?`, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, d.owner.OwnerID, d.owner.Generation).Scan(&boot, &expires); err != nil {
			return "", err
		}
		current, now, err := d.manager.clock.Now()
		if err != nil || current != boot || now < 0 || expires-int64(now) < int64(ttl) {
			return "", errCollaborationAgentAccess
		}
		return proof.currentDeliveryPermission, nil
	}
	delivery := mooring.NewDeliveryStore(d.manager.owners, true, authority, ttl, collaborationHTTPParticipantLimit*4)
	var grant mooring.DeliveryGrant
	err := a.run(ctx, func(ctx context.Context) error {
		var err error
		grant, err = delivery.Open(ctx, d.owner, a.participant.id, string(q.ValidationNonce))
		return err
	})
	if err != nil {
		if err == errCollaborationAuthorChanged {
			if e := d.cell.runtime.DeclineParticipantTicket(ctx, d.activation, a.pending, token); e != nil {
				return e
			}
			a.pending = nil
			a.closed = true
			close(a.ready)
			return nil
		}
		return err
	}
	a.delivery, a.gate = delivery, grant
	read, write := true, true
	result := protocol.TicketValidationResult{Type: "ticket_validated", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, Generation: q.Fence.Generation, ParticipantRef: protocol.Identifier(a.participant.id), PermissionVersion: protocol.Counter(grant.PermissionVersion), Granted: protocol.Capabilities{Read: &read, Write: &write}, Versions: d.wireVersions(), ExpiresInMs: protocol.PositiveCounter(strconv.FormatInt(grant.DurationMilliseconds, 10)), ValidationNonce: q.ValidationNonce, GateID: protocol.Identifier(grant.GateID)}
	return collaborationPumpTicketReply(ctx, d.cell.runtime, token, q, result)
}

func (m *collaborationHTTPManager) agentTool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m.mu.Lock()
	controller := m.agentTasks
	closed := m.closed
	m.mu.Unlock()
	if controller == nil || closed {
		replyError(w, 503, "document task unavailable")
		return
	}
	a, err := controller.lookup(r)
	if err != nil {
		replyError(w, 403, "document task denied")
		return
	}
	a.callMu.Lock()
	defer a.callMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	binding, err := a.document.humans.storeBinding()
	if err != nil {
		replyError(w, 503, "document task unavailable")
		return
	}
	// The original document/runtime operation, not the internal request or run
	// bearer, installs the admission/sealing scope. It spans the bounded reply.
	err = a.document.submissions.run(ctx, binding, func(bound context.Context) error {
		a.tool(w, r.WithContext(bound))
		return nil
	})
	if err != nil {
		replyError(w, 503, "document task unavailable")
	}
}

func (a *collaborationLiveAgent) tool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := a.ensure(ctx, r); err != nil {
		replyError(w, 403, "document task denied")
		return
	}
	if r.Method == http.MethodGet {
		a.read(w, r)
		return
	}
	if r.Method != http.MethodPost {
		replyError(w, 405, "method unavailable")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, collaborationHTTPFrameLimit))
	if err != nil {
		replyError(w, 413, "original submission too large")
		return
	}
	group, err := a.document.originals.captureAgent(ctx, a, r, raw)
	if err != nil {
		replyError(w, 409, "original submission not admitted")
		return
	}
	outer, packet, duplicate, err := a.document.originals.sendPublicGroup(group)
	if err != nil {
		replyError(w, 409, "original submission not admitted")
		return
	}
	if !duplicate {
		attempted, physicallySent := false, false
		err = a.run(ctx, func(ctx context.Context) error {
			return a.document.submissions.withWriter(ctx, func(tx *sql.Tx) error {
				if err := a.checkGate(ctx, tx, "task", 0); err != nil {
					return err
				}
				attempted = true
				e := a.document.cell.runtime.SendParticipantInput(ctx, a.document.activation, protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: outer, ControlProfile: mooring.PrivateSessionProfile, Scope: a.document.wireScope(), SessionID: protocol.Identifier(a.document.activation.SessionID()), RuntimeRef: protocol.Identifier(a.document.cell.runtime.RuntimeRef()), Fence: a.document.wireFence(), ConnectionRef: protocol.Identifier(a.ref), ParticipantRef: protocol.Identifier(a.participant.id), GateID: protocol.Identifier(a.gate.GateID), PermissionVersion: protocol.Counter(a.gate.PermissionVersion), Message: packet})
				physicallySent = e == nil
				return e
			})
		})
		if err != nil {
			// Before physical I/O, canceled/denied work is definitely unsent.
			// The transport poisons its runtime on EVERY physical write error;
			// a still-live runtime therefore proves this attempt emitted no bytes.
			// A later SQL COMMIT failure after successful send is not that proof.
			if !physicallySent && (!attempted || a.document.cell.runtime.Err() == nil) {
				if a.document.originals.retryUnsentAgentGroup(group, a) != nil {
					a.document.cancel()
				}
			}
			replyError(w, 503, "submission uncertain; retain exact original")
			return
		}
	}
	select {
	case <-ctx.Done():
		replyError(w, 503, "submission uncertain; retain exact original")
		return
	case <-a.document.ctx.Done():
		replyError(w, 503, "submission uncertain; retain exact original")
		return
	case <-group.agentReceiptReady:
	}
	a.document.originals.mu.Lock()
	receipt := group.receipt
	delivered := group.agentReceiptDelivered
	a.document.originals.mu.Unlock()
	if !delivered || receipt == nil {
		replyError(w, 503, "submission uncertain; retain exact original")
		return
	}
	raw, err = json.Marshal(receipt)
	if err != nil || a.send(r, w, raw) != nil {
		return
	}
}

func (a *collaborationLiveAgent) read(w http.ResponseWriter, r *http.Request) {
	d := a.document
	snapshot, err := d.cell.agentCheckpoint(r, a.confirmed.store, a.confirmed.mandate, d.owner)
	if err != nil {
		replyError(w, 403, "document task denied")
		return
	}
	state, err := base64.StdEncoding.Strict().DecodeString(string(snapshot.Checkpoint.State))
	if err != nil || len(state) > mooring.MaxPrivatePrepareRPCFrame {
		replyError(w, 503, "document unavailable")
		return
	}
	var cp struct {
		Native   json.RawMessage `json:"native"`
		BlockIDs []string        `json:"block_ids"`
	}
	if json.Unmarshal(state, &cp) != nil || len(cp.Native) == 0 {
		replyError(w, 503, "document unavailable")
		return
	}
	raw, err := json.Marshal(struct {
		Scope       protocol.Scope               `json:"scope"`
		SessionID   string                       `json:"session_id"`
		Versions    protocol.Versions            `json:"versions"`
		TaskID      string                       `json:"task_id"`
		Participant string                       `json:"participant_ref"`
		Revision    protocol.Counter             `json:"revision"`
		Native      json.RawMessage              `json:"native"`
		BlockIDs    []string                     `json:"block_ids"`
		Targets     []mooring.BatchTargetVersion `json:"targets"`
	}{d.wireScope(), d.activation.SessionID(), d.wireVersions(), a.confirmed.workID, a.participant.id, snapshot.Checkpoint.Revision, cp.Native, cp.BlockIDs, snapshot.Targets})
	if err != nil || a.send(r, w, raw) != nil {
		return
	}
}

// Physical HTTP delivery is serialized with the current SAME-writer authority
// and exact SQL gate. No previously captured proof can waive a current check.
func (a *collaborationLiveAgent) send(r *http.Request, w http.ResponseWriter, raw []byte) error {
	if len(raw) > 2<<20 {
		return mooring.ErrBatchCapacity
	}
	d := a.document
	return a.run(r.Context(), func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if err := a.checkGate(ctx, tx, "read", collaborationHTTPSendDeadline); err != nil {
				return err
			}
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(collaborationHTTPSendDeadline)); err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write(raw); err != nil {
				return err
			}
			// net/http may buffer small JSON. Complete its physical flush before
			// releasing the original ingress and SAME-writer delivery fences.
			return http.NewResponseController(w).Flush()
		})
	})
}

func (a *collaborationLiveAgent) checkGate(ctx context.Context, tx *sql.Tx, intent string, minimum time.Duration) error {
	a.mu.Lock()
	closed, gate, joined := a.closed, a.gate, a.joined
	a.mu.Unlock()
	if closed || !joined || gate.GateID == "" {
		return errCollaborationAgentAccess
	}
	d := a.document
	proof, err := a.allowed(ctx, tx, intent)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(d.owner.Token))
	var boot string
	var expires int64
	var denied bool
	err = tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns,closed FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND owner_token_hash=? AND permission_version=?`, gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, a.participant.id, d.owner.OwnerID, d.owner.Generation, hash[:], proof.currentDeliveryPermission).Scan(&boot, &expires, &denied)
	current, now, clockErr := d.manager.clock.Now()
	if err != nil || clockErr != nil || boot != current || denied || now < 0 || int64(now) >= expires || expires-int64(now) < int64(minimum) || time.Until(proof.credentialExpiry) < minimum {
		return errCollaborationAgentAccess
	}
	return nil
}
