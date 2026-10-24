package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"golang.org/x/net/websocket"
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
	server := websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			_ = ws.Close()
			return
		}
		c.socket = ws
		c.mu.Unlock()
		ws.MaxPayloadBytes = collaborationHTTPFrameLimit
		stop := context.AfterFunc(d.ctx, func() { _ = ws.Close() })
		defer stop()
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var raw []byte
		if websocket.Message.Receive(ws, &raw) != nil {
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
		if err = d.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
			return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
				if _, e := d.humans.checkCaptured(ctx, tx, c.admission, false, 0); e != nil {
					return e
				}
				return d.cell.runtime.SendParticipantOpen(ctx, d.activation, protocol.SessionParticipantOpen{Type: "session_participant_open", RequestID: protocol.Identifier(request), ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(c.ref), Join: normalized})
			})
		}); err != nil {
			return
		}
		for {
			_ = ws.SetReadDeadline(time.Now().Add(time.Minute))
			raw = nil
			if websocket.Message.Receive(ws, &raw) != nil {
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
			if header.Type == "checkpoint_request" {
				if c.checkpoint(normalized) != nil {
					return
				}
				continue
			}
			// Raw human changes require the typed host-original prepare context. No
			// agent-command fallback or loose role/context callback can qualify them.
			if header.Type == "submit_changes" {
				return
			}
			if header.Type != "presence" && header.Type != "operation_status" {
				return
			}
			if err = c.input(normalized); err != nil {
				return
			}
		}
	}}
	server.ServeHTTP(w, r)
}

func (c *collaborationHTTPConnection) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	ws := c.socket
	gate := c.joined.gate.GateID
	c.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
	d := c.document
	d.mu.Lock()
	if d.connections[c.ref] == c {
		delete(d.connections, c.ref)
	}
	d.mu.Unlock()
	if gate != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = d.manager.pages.db.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=?`, gate, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, c.admission.participant, d.owner.OwnerID, d.owner.Generation)
	}
	// Closing a socket is not a runtime blocked-ACK or revocation completion.
}

func (d *collaborationHTTPDocument) ticket(ctx context.Context, q protocol.TicketValidationRequest) (protocol.TicketValidationResult, error) {
	var empty protocol.TicketValidationResult
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != d.wireFence() {
		return empty, errCollaborationHTTP
	}
	d.mu.Lock()
	var c *collaborationHTTPConnection
	for _, candidate := range d.connections {
		if candidate.ticket == q.Ticket {
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
	if len(raw) > collaborationHTTPFrameLimit {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.socket == nil || c.joined.delivery == nil {
		return errCollaborationHTTP
	}
	d := c.document
	return d.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if e := d.cell.check(ctx, tx); e != nil {
				return e
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
				return e
			}
			currentBoot, now, e := d.manager.clock.Now()
			if e != nil || boot != currentBoot || closed || now < 0 || int64(now) >= expires || expires-int64(now) < int64(collaborationHTTPSendDeadline) {
				return errCollaborationHTTP
			}
			var ownerBoot string
			var ownerExpires int64
			if e = tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns FROM mooring_session_owners WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND generation=?`, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, d.owner.OwnerID, d.owner.Generation).Scan(&ownerBoot, &ownerExpires); e != nil {
				return e
			}
			if ownerBoot != currentBoot || int64(now) >= ownerExpires || ownerExpires-int64(now) < int64(collaborationHTTPSendDeadline) {
				return errCollaborationHTTP
			}
			if e = d.cell.owners.Check(ctx, tx, d.owner); e != nil {
				return e
			}
			if e = c.socket.SetWriteDeadline(time.Now().Add(collaborationHTTPSendDeadline)); e != nil {
				return e
			}
			return websocket.Message.Send(c.socket, string(raw))
		})
	})
}
func (d *collaborationHTTPDocument) output(_ context.Context, q protocol.SessionParticipantOutput) error {
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.RuntimeRef != protocol.Identifier(d.cell.runtime.RuntimeRef()) || q.Fence != d.wireFence() {
		return errCollaborationHTTP
	}
	d.mu.Lock()
	c := d.connections[string(q.ConnectionRef)]
	d.mu.Unlock()
	if c == nil {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	valid := q.ParticipantRef == protocol.Identifier(c.admission.participant) && q.GateID == protocol.Identifier(c.joined.gate.GateID) && q.PermissionVersion == protocol.Counter(c.joined.gate.PermissionVersion)
	c.mu.Unlock()
	if !valid {
		return errCollaborationHTTP
	}
	raw, e := d.manager.schema.ValidateE3(q.Message)
	if e != nil {
		return e
	}
	var inner struct {
		Type       string               `json:"type"`
		Scope      *protocol.Scope      `json:"scope"`
		Session    *protocol.Identifier `json:"session_id"`
		Versions   *protocol.Versions   `json:"versions"`
		Generation *protocol.Counter    `json:"generation"`
	}
	if json.Unmarshal(raw, &inner) != nil || inner.Scope == nil || *inner.Scope != d.wireScope() || inner.Session != nil && *inner.Session != protocol.Identifier(d.activation.SessionID()) || inner.Versions != nil && *inner.Versions != d.wireVersions() || inner.Generation != nil && *inner.Generation != d.wireFence().Generation {
		return errCollaborationHTTP
	}
	if inner.Type == "session_joined" {
		var j protocol.SessionJoined
		if json.Unmarshal(raw, &j) != nil || j.ParticipantRef != q.ParticipantRef || j.GateID != q.GateID || j.PermissionVersion != q.PermissionVersion {
			return errCollaborationHTTP
		}
	}
	return c.checkedSend(raw)
}
func (c *collaborationHTTPConnection) input(raw []byte) error {
	d := c.document
	c.mu.Lock()
	joined, closed := c.joined, c.closed
	c.mu.Unlock()
	if closed || joined.delivery == nil {
		return errCollaborationHTTP
	}
	id, e := collaborationOpaqueID()
	if e != nil {
		return e
	}
	return d.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if _, e := d.humans.checkCaptured(ctx, tx, c.admission, false, 0); e != nil {
				return e
			}
			return d.cell.runtime.SendParticipantInput(ctx, d.activation, protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: protocol.Identifier(id), ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(c.ref), ParticipantRef: protocol.Identifier(c.admission.participant), GateID: protocol.Identifier(joined.gate.GateID), PermissionVersion: protocol.Counter(joined.gate.PermissionVersion), Message: raw})
		})
	})
}
func (c *collaborationHTTPConnection) checkpoint(raw []byte) error {
	var q protocol.CheckpointRequest
	if json.Unmarshal(raw, &q) != nil || q.Type != "checkpoint_request" || q.Scope != c.document.wireScope() {
		return errCollaborationHTTP
	}
	var response []byte
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
		return e
	})
	if e != nil {
		return e
	}
	return c.checkedSend(response)
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
