package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"github.com/gorilla/websocket"
)

const collaborationHTTPSendDeadline = 250 * time.Millisecond

func (d *collaborationHTTPDocument) wireScope() protocol.Scope {
	return protocol.Scope{WorkspaceID: protocol.Identifier(d.key.WorkspaceID), ArtifactID: protocol.Identifier(d.key.ArtifactID), ApplicationID: protocol.Identifier(d.key.ApplicationID)}
}
func (d *collaborationHTTPDocument) wireFence() protocol.Fence {
	return protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(d.owner.Generation, 10)), Token: d.owner.Token}
}
func (d *collaborationHTTPDocument) wireVersions() protocol.Versions {
	return protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "0.2.0", EngineVersion: "0.10.0"}
}

func (m *collaborationHTTPManager) websocket(w http.ResponseWriter, r *http.Request) {
	key, rec, e := m.scope(r)
	if e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	if !m.pages.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	// Browser credentials are accepted only from the original same-origin host.
	// No client header can select a different trusted frontend origin.
	origin, e := url.Parse(r.Header.Get("Origin"))
	if e != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		replyError(w, 403, "Mooring origin denied")
		return
	}

	expectedOrigin := m.config.PublicOrigin
	if expectedOrigin == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expectedOrigin = scheme + "://" + r.Host
	}
	if origin.String() != expectedOrigin {
		replyError(w, 403, "Mooring origin denied")
		return
	}

	ref := r.URL.Query().Get("connection_ref")
	if !mooring.ValidWireIdentifier(ref) {
		replyError(w, 403, "Mooring access denied")
		return
	}
	m.mu.Lock()
	d := m.docs[key]
	m.mu.Unlock()
	if d == nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	d.mu.Lock()
	c := d.connections[ref]
	d.mu.Unlock()
	if c == nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	if _, e = d.humans.bindHumanRequest(r, c.admission.participant); e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	c.mu.Lock()
	if c.used || c.closed || c.auditedOwner != rec.OwnerUserID {
		c.mu.Unlock()
		replyError(w, 403, "Mooring access denied")
		return
	}
	c.used = true
	// The live authenticated HTTP request must match the ORIGINAL admission. Its
	// fresh middleware snapshot cannot replace any original counter or horizon.
	operationContext, cancelOperation := context.WithCancel(context.WithoutCancel(r.Context()))
	stopOperation := context.AfterFunc(d.ctx, cancelOperation)
	defer stopOperation()
	defer cancelOperation()
	c.request = r.Clone(operationContext)
	c.request.Body = nil
	c.request.Header = r.Header.Clone()
	c.request.Header.Del("Authorization")
	c.request.Header.Del("Cookie")
	c.mu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		c.close()
		replyError(w, 503, "Mooring session unavailable")
		return
	}
	m.workers.Add(1)
	m.mu.Unlock()
	defer m.workers.Done()
	defer c.close()
	// The authenticated exact-origin check above owns origin policy.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if m.pages.logger != nil {
		m.pages.logger.Info("mooring websocket bound", "connection_ref", c.ref, "admission_ref", collaborationAdmissionRef(c.admission))
	}
	closedReason := "transport_error"
	var stopLiveness func() bool
	defer func() {
		pingFailed := false
		if stopLiveness != nil {
			pingFailed = stopLiveness()
		}
		phase := "join"
		c.mu.Lock()
		if c.joinedDelivered {
			phase = "active"
		}
		sessionClosed := c.closed
		c.mu.Unlock()
		if sessionClosed || d.ctx.Err() != nil {
			closedReason = "session_closed"
		} else if pingFailed {
			closedReason = "ping_write_failed"
		}
		collaborationLogSocketTermination(m.pages.logger, c.ref, phase, closedReason)
	}()
	func() {
		c.mu.Lock()
		if c.closed {
			closedReason = "session_closed"
			c.mu.Unlock()
			_ = ws.Close()
			return
		}
		c.socket = ws
		c.mu.Unlock()
		ws.SetReadLimit(collaborationHTTPFrameLimit)
		stop := context.AfterFunc(d.ctx, func() { _ = ws.Close() })
		defer stop()
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, raw, err := ws.ReadMessage()
		if err != nil {
			closedReason = collaborationSocketCloseReason(err)
			return
		}
		normalized, err := m.schema.ValidateE3(raw)
		if err != nil {
			return
		}
		var join protocol.SessionJoin
		if json.Unmarshal(normalized, &join) != nil || join.Type != "session_join" || join.Ticket != c.ticket || join.Versions != d.wireVersions() {
			return
		}
		request, err := collaborationOpaqueID()
		if err != nil {
			return
		}
		c.mu.Lock()
		c.joinRequest = join.RequestID
		c.openRequest = protocol.Identifier(request)
		c.mu.Unlock()
		if err = d.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
			return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
				if _, e := d.humans.checkCaptured(ctx, tx, c.admission, false, 0); e != nil {
					return e
				}
				pending, e := d.cell.runtime.PrepareParticipantOpen(d.activation, protocol.SessionParticipantOpen{Type: "session_participant_open", RequestID: protocol.Identifier(request), ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(c.ref), Join: normalized})
				if e != nil {
					return e
				}
				c.mu.Lock()
				c.participantPending = pending
				c.mu.Unlock()
				return d.cell.runtime.SendPreparedParticipantOpen(ctx, d.activation, pending)
			})
		}); err != nil {
			return
		}
		stopLiveness = collaborationSocketLiveness(operationContext, ws, time.Minute, 30*time.Second)
		for {
			_ = ws.SetReadDeadline(time.Now().Add(time.Minute))
			_, raw, err = ws.ReadMessage()
			if err != nil {
				closedReason = collaborationSocketCloseReason(err)
				return
			}
			normalized, err = m.schema.ValidateE3(raw)
			if err != nil {
				return
			}
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(normalized, &header) != nil {
				return
			}
			if err = c.ensureCurrentGate(operationContext); err != nil {
				return
			}
			if header.Type == "checkpoint_request" {
				if c.checkpoint(normalized) != nil {
					return
				}
				continue
			}
			if header.Type == "submit_changes" {
				if !c.admission.write {
					if c.rejectUnqualified(normalized) != nil {
						return
					}
				} else if c.submitOriginal(normalized) != nil {
					return
				}
				continue
			}
			// These controls are not qualified by the current native actor/source
			// bridge. Reject locally without sending them into the shared process or
			// converting them to a durable operation outcome.
			if header.Type == "presence" || header.Type == "operation_status" {
				if c.rejectUnqualified(normalized) != nil {
					return
				}
				continue
			}
			return

		}
	}()
}

// Transport pongs prove only that this original socket is responsive. They
// never refresh an admission, delivery gate, lease, or application authority.
// Keep the existing idle bound while allowing a browser with no authored data
// to remain connected; browsers answer RFC 6455 pings without application code.
func collaborationSocketLiveness(parent context.Context, ws *websocket.Conn, idle, interval time.Duration) func() bool {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var pingFailed atomic.Bool
	_ = ws.SetReadDeadline(time.Now().Add(idle))
	ws.SetPongHandler(func(payload string) error {
		if payload != "mooring" {
			return nil
		}
		return ws.SetReadDeadline(time.Now().Add(idle))
	})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ws.WriteControl(websocket.PingMessage, []byte("mooring"), time.Now().Add(collaborationHTTPSendDeadline)) != nil {
					pingFailed.Store(true)
					_ = ws.Close()
					return
				}
			}
		}
	}()
	return func() bool { cancel(); <-done; return pingFailed.Load() }
}

func (c *collaborationHTTPConnection) close() {
	if c == nil {
		return
	}
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	ws, joined, pending, binding := c.socket, c.joined, c.participantPending, c.participantBinding
	alreadyLeft := binding == nil && c.transition != nil
	c.mu.Unlock()
	d := c.document
	if d.originals != nil {
		d.originals.retire(c)
	}
	if ws != nil {
		_ = ws.Close()
	}
	d.mu.Lock()
	if d.connections[c.ref] == c {
		delete(d.connections, c.ref)
	}
	d.mu.Unlock()
	if pending != nil {
		// A possibly sent Open has no observed current Joined. Never fabricate a
		// gate or send speculative/duplicate Leave on disconnect during handshake.
		if d.cell.runtime.DiscardUnsentParticipantOpen(pending) != nil {
			d.cancel()
		}
		return
	}
	if joined.gate.GateID == "" || alreadyLeft || d.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := d.manager.pages.db.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=?`, joined.gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, c.admission.participant, d.owner.OwnerID, d.owner.Generation); err != nil {
		d.cancel()
		return
	}
	if binding == nil {
		d.cancel()
		return
	}
	proof, err := d.cell.runtime.RetireParticipant(ctx, d.activation, binding)
	if err != nil {
		d.cancel()
		return
	}
	c.mu.Lock()
	if c.retirements == nil {
		c.retirements = make(map[string]*collaborationRetiredGate)
	}
	c.retirements[joined.gate.GateID] = &collaborationRetiredGate{gate: joined.gate, proof: proof}
	c.participantBinding = nil
	c.mu.Unlock()
	// This denial-only Leave is not a native revocation ACK. Without a fresh
	// correlated Joined its proof stays charged through the unread FD tail.
}

func (d *collaborationHTTPDocument) ticket(ctx context.Context, q protocol.TicketValidationRequest) (protocol.TicketValidationResult, error) {
	var empty protocol.TicketValidationResult
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != d.wireFence() {
		return empty, errCollaborationHTTP
	}
	d.mu.Lock()
	var c *collaborationHTTPConnection
	for _, candidate := range d.connections {
		candidate.mu.Lock()
		match := candidate.ticket == q.Ticket
		candidate.mu.Unlock()
		if match {
			c = candidate
			break
		}
	}
	d.mu.Unlock()
	if c == nil {
		return empty, errCollaborationHTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.used || c.socket == nil || c.joined.delivery != nil {
		return empty, errCollaborationHTTP
	}
	joined, e := d.humans.join(ctx, d.cell.runtime, d.owner, q.Ticket, string(q.ValidationNonce))
	if e != nil {
		return empty, e
	}
	if joined.admission.participant != c.admission.participant {
		return empty, errCollaborationHTTP
	}
	c.joined = joined
	read, write := true, c.admission.write
	return protocol.TicketValidationResult{Type: "ticket_validated", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, Generation: q.Fence.Generation, ParticipantRef: protocol.Identifier(c.admission.participant), PermissionVersion: protocol.Counter(joined.gate.PermissionVersion), Granted: protocol.Capabilities{Read: &read, Write: &write}, Versions: d.wireVersions(), ExpiresInMs: protocol.PositiveCounter(strconv.FormatInt(joined.gate.DurationMilliseconds, 10)), ValidationNonce: q.ValidationNonce, GateID: protocol.Identifier(joined.gate.GateID)}, nil
}

// Only an already-entered callback/reply is serialized here. This does not
// erase queued renewals, authorize a recipient or acknowledge revocation.
func (d *collaborationHTTPDocument) deliveryGuard(ctx context.Context, q protocol.DeliveryRenew) (func(), error) {
	if ctx.Err() != nil || q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != d.wireFence() {
		return nil, errCollaborationHTTP
	}
	d.mu.Lock()
	var c *collaborationHTTPConnection
	for _, candidate := range d.connections {
		if candidate.admission.participant == string(q.ParticipantRef) {
			c = candidate
			break
		}
	}
	d.mu.Unlock()
	if c == nil {
		return nil, errCollaborationHTTP
	}
	c.deliveryMu.Lock()
	c.mu.Lock()
	valid := !c.closed && c.joined.delivery != nil && q.GateID == protocol.Identifier(c.joined.gate.GateID) && q.PermissionVersion == protocol.Counter(c.joined.gate.PermissionVersion)
	c.mu.Unlock()
	if !valid || ctx.Err() != nil {
		c.deliveryMu.Unlock()
		return nil, errCollaborationHTTP
	}
	return c.deliveryMu.Unlock, nil
}
func (d *collaborationHTTPDocument) renewDelivery(ctx context.Context, q protocol.DeliveryRenew) (protocol.DeliveryGrant, error) {
	d.mu.Lock()
	var c *collaborationHTTPConnection
	for _, candidate := range d.connections {
		if candidate.admission.participant == string(q.ParticipantRef) {
			c = candidate
			break
		}
	}
	d.mu.Unlock()
	if c == nil {
		return protocol.DeliveryGrant{}, errCollaborationHTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return protocol.DeliveryGrant{}, errCollaborationHTTP
	}
	return c.joined.renewDelivery(ctx, d.humans, d.owner, q)
}

// checkedSend holds the ORIGINAL operation and SQL writer through the physical
// bounded send. A current canonical authorization, exact SQL gate and current
// owner must all have enough remaining horizon for that send; no renewal here.
func (c *collaborationHTTPConnection) checkedSend(raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checkedSendLocked(raw)
}

// Caller holds c.mu through the SQL writer and physical send.
func (c *collaborationHTTPConnection) checkedSendLocked(raw []byte) error {
	return c.checkedSendRequestLocked(c.request, raw)
}

// The operation request preserves the original principal/context deadline.
// A catch-up phase only adds cancellation; it never renews the original source.
func (c *collaborationHTTPConnection) checkedSendRequestLocked(request *http.Request, raw []byte) error {
	if len(raw) > collaborationHTTPFrameLimit || c.closed || c.socket == nil || c.joined.delivery == nil {
		return errCollaborationHTTP
	}
	d := c.document
	return d.humans.runHumanOperation(request, c.admission.participant, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if e := d.cell.check(ctx, tx); e != nil {
				return d.humans.logAdmissionDenial(c.admission, "send_session_store", e)
			}
			permission, e := d.humans.checkCaptured(ctx, tx, c.admission, false, collaborationHTTPSendDeadline)
			if e != nil {
				return e
			}
			tokenHash := sha256.Sum256([]byte(d.owner.Token))
			var boot string
			var expires int64
			var closed bool
			e = tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns,closed FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND owner_token_hash=? AND permission_version=?`, c.joined.gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, c.admission.participant, d.owner.OwnerID, d.owner.Generation, tokenHash[:], permission).Scan(&boot, &expires, &closed)
			if e != nil {
				return d.humans.logAdmissionDenial(c.admission, "send_gate_lookup", e)
			}
			currentBoot, now, e := d.manager.clock.Now()
			if e != nil || boot != currentBoot || now < 0 {
				return d.humans.logAdmissionDenial(c.admission, "send_gate_clock", errCollaborationHTTP)
			}
			if closed {
				return d.humans.logAdmissionDenial(c.admission, "send_gate_closed", errCollaborationHTTP)
			}
			if int64(now) >= expires {
				return d.humans.logAdmissionDenial(c.admission, "send_gate_expired", errCollaborationHTTP)
			}
			if expires-int64(now) < int64(collaborationHTTPSendDeadline) {
				return d.humans.logAdmissionDenial(c.admission, "send_gate_horizon", errCollaborationHTTP)
			}
			var ownerBoot string
			var ownerExpires int64
			if e = tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns FROM mooring_session_owners WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND generation=?`, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, d.owner.OwnerID, d.owner.Generation).Scan(&ownerBoot, &ownerExpires); e != nil {
				return d.humans.logAdmissionDenial(c.admission, "send_owner_lookup", e)
			}
			if ownerBoot != currentBoot || int64(now) >= ownerExpires || ownerExpires-int64(now) < int64(collaborationHTTPSendDeadline) {
				return d.humans.logAdmissionDenial(c.admission, "send_owner_horizon", errCollaborationHTTP)
			}
			if e = d.cell.owners.Check(ctx, tx, d.owner); e != nil {
				return d.humans.logAdmissionDenial(c.admission, "send_owner_check", e)
			}
			if e = c.socket.SetWriteDeadline(time.Now().Add(collaborationHTTPSendDeadline)); e != nil {
				return d.humans.logAdmissionDenial(c.admission, "send_write_deadline", e)
			}
			if e = c.socket.WriteMessage(websocket.TextMessage, raw); e != nil {
				return d.humans.logAdmissionDenial(c.admission, "send_transport", e)
			}
			return nil
		})
	})
}
func (d *collaborationHTTPDocument) output(ctx context.Context, q protocol.SessionParticipantOutput) error {
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.RuntimeRef != protocol.Identifier(d.cell.runtime.RuntimeRef()) || q.Fence != d.wireFence() {
		return errCollaborationHTTP
	}
	d.mu.Lock()
	c := d.connections[string(q.ConnectionRef)]
	d.mu.Unlock()
	if c == nil {
		return errCollaborationHTTP
	}
	raw, e := d.manager.schema.ValidateE3(q.Message)
	if e != nil {
		return e
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return errCollaborationHTTP
	}
	if header.Type != "session_joined" {
		return c.committedOutput(ctx, q, raw, header.Type)
	}

	var joined protocol.SessionJoined
	if json.Unmarshal(raw, &joined) != nil || joined.Type != "session_joined" || joined.Scope != d.wireScope() || joined.SessionID != protocol.Identifier(d.activation.SessionID()) || joined.Versions != d.wireVersions() || joined.Generation != d.wireFence().Generation {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.joinedDelivered || c.joinRequest == "" || c.openRequest == "" || q.RequestID != c.openRequest || joined.RequestID != c.joinRequest || q.ParticipantRef != protocol.Identifier(c.admission.participant) || q.GateID != protocol.Identifier(c.joined.gate.GateID) || q.PermissionVersion != protocol.Counter(c.joined.gate.PermissionVersion) || joined.ParticipantRef != q.ParticipantRef || joined.GateID != q.GateID || joined.PermissionVersion != q.PermissionVersion {
		return errCollaborationHTTP
	}
	if e = c.checkedSendLocked(raw); e != nil {
		return e
	}
	c.joinedDelivered = true
	return nil
}

func (c *collaborationHTTPConnection) rejectUnqualified(raw []byte) error {
	var request struct {
		RequestID protocol.Identifier  `json:"request_id"`
		Scope     protocol.Scope       `json:"scope"`
		Session   *protocol.Identifier `json:"session_id"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Scope != c.document.wireScope() || request.Session != nil && *request.Session != protocol.Identifier(c.document.activation.SessionID()) {
		return errCollaborationHTTP
	}
	scope := c.document.wireScope()
	response, e := json.Marshal(protocol.ProtocolError{Type: "protocol_error", RequestID: request.RequestID, Scope: &scope, Reason: protocol.Rejection{Code: "invalid_request", Message: "control not available in this session", Retry: "do_not_retry"}})
	if e != nil {
		return e
	}
	return c.checkedSend(response)
}

func (c *collaborationHTTPConnection) checkpoint(raw []byte) error {
	var q protocol.CheckpointRequest
	if json.Unmarshal(raw, &q) != nil || q.Type != "checkpoint_request" || q.Scope != c.document.wireScope() {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var response []byte
	var delivered protocol.Checkpoint
	e := c.document.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
		snapshot, e := c.document.cell.store.ReadFullCheckpoint(ctx, c.document.owner)
		if e != nil {
			return e
		}
		// Only the actual current SQL snapshot is returned, never a caller-selected
		// historic revision or the untrusted initialization body.
		if q.Revision != snapshot.Checkpoint.Revision {
			return errCollaborationHTTP
		}
		response, e = json.Marshal(protocol.CheckpointResult{Type: "checkpoint_result", RequestID: q.RequestID, Checkpoint: snapshot.Checkpoint})
		delivered = snapshot.Checkpoint
		return e
	})
	if e != nil {
		return e
	}
	if e = c.checkedSendLocked(response); e != nil {
		return e
	}
	return c.recordObservedCheckpoint(delivered)
}
func (d *collaborationHTTPDocument) submit(ctx context.Context, q protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
	return d.committer.submit(ctx, q)
}
func (d *collaborationHTTPDocument) status(ctx context.Context, q protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
	var empty protocol.SessionStatusResult
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.RuntimeRef != protocol.Identifier(d.cell.runtime.RuntimeRef()) || q.Fence != d.wireFence() || q.Message.Scope != q.Scope {
		return empty, errCollaborationHTTP
	}
	d.mu.Lock()
	c := d.connections[string(q.ConnectionRef)]
	d.mu.Unlock()
	if c == nil {
		return empty, errCollaborationHTTP
	}
	c.mu.Lock()
	valid := !c.closed && q.ParticipantRef == protocol.Identifier(c.admission.participant) && q.GateID == protocol.Identifier(c.joined.gate.GateID) && q.PermissionVersion == protocol.Counter(c.joined.gate.PermissionVersion)
	c.mu.Unlock()
	if !valid {
		return empty, errCollaborationHTTP
	}
	var result *mooring.BatchOperationResult
	err := d.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
		var e error
		result, e = d.cell.store.Status(ctx, d.key, string(q.Message.OpID), string(q.Message.ExpectedFingerprint))
		return e
	})
	if err != nil {
		return empty, err
	}
	var outcome []byte
	if result == nil {
		outcome, _ = json.Marshal(protocol.StatusResultResultVariant2{OpID: q.Message.OpID, Status: "unknown"})
	} else {
		outcome, err = json.Marshal(result)
		if err != nil {
			return empty, err
		}
	}
	return protocol.SessionStatusResult{Type: "session_status_result", RequestID: q.RequestID, ControlProfile: mooring.PrivateSessionProfile, Scope: q.Scope, SessionID: q.SessionID, RuntimeRef: q.RuntimeRef, Fence: q.Fence, ConnectionRef: q.ConnectionRef, ParticipantRef: q.ParticipantRef, GateID: q.GateID, PermissionVersion: q.PermissionVersion, Message: protocol.StatusResult{Type: "operation_status_result", RequestID: q.Message.RequestID, Scope: q.Scope, Result: outcome}}, nil
}
