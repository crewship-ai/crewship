package mooring

import (
	"context"
	"database/sql"
	"encoding/json"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// These capabilities prove transport observations only. Source admission and
// current rights remain the host's independent responsibility on every send.
// One conservative reservation covers all retained closed-schema control DATA.
const participantBindingCharge = 8192

type SessionParticipantPending struct {
	runtime *SessionRuntime
	state   *sessionParticipantState
}
type SessionParticipantBinding struct {
	runtime *SessionRuntime
	state   *sessionParticipantState
}
type SessionRetiredDelivery struct {
	runtime *SessionRuntime
	state   *sessionParticipantState
}
type sessionParticipantState struct {
	pending                                                       *SessionParticipantPending
	declining, declined                                           bool
	done                                                          chan struct{}
	activation                                                    *SessionActivation
	open                                                          protocol.SessionParticipantOpen
	join                                                          protocol.SessionJoin
	ticket                                                        protocol.TicketValidationResult
	ticketBytes                                                   []byte
	writing, sent, ticketSent, captured, retiring, left, released bool
	participant, gate, permission                                 string
	watermark                                                     uint64
	predecessor                                                   *sessionParticipantState
	successor                                                     *sessionParticipantState
}

// SendTrackedParticipantOpen retains the original physical Open and observes
// its actual TicketValidated reply before any Joined can become a binding.
// The ordinary untracked transport API remains unchanged.
func (s *SessionRuntime) SendTrackedParticipantOpen(ctx context.Context, a *SessionActivation, q protocol.SessionParticipantOpen) (*SessionParticipantPending, error) {
	p, err := s.PrepareParticipantOpen(a, q)
	if err != nil {
		return nil, err
	}
	if err = s.SendPreparedParticipantOpen(ctx, a, p); err != nil {
		return nil, err
	}
	return p, nil
}

// PrepareParticipantOpen reserves an owned Open before the host publishes its
// Pending on the original connection. It emits no bytes and grants no rights.
func (s *SessionRuntime) PrepareParticipantOpen(a *SessionActivation, q protocol.SessionParticipantOpen) (*SessionParticipantPending, error) {
	return s.prepareParticipantOpen(a, q, nil)
}
func (s *SessionRuntime) prepareParticipantOpen(a *SessionActivation, q protocol.SessionParticipantOpen, old *sessionParticipantState) (*SessionParticipantPending, error) {
	if s.participantBinding(a, q.Scope, q.SessionID, q.RuntimeRef, q.Fence) != nil {
		return nil, ErrSessionRuntime
	}
	var join protocol.SessionJoin
	raw, e := json.Marshal(q)
	if e != nil || q.Type != "session_participant_open" || string(q.ControlProfile) != PrivateSessionProfile || !batchID(string(q.RequestID)) || !batchID(string(q.ConnectionRef)) || len(raw) > participantBindingCharge/2 || json.Unmarshal(q.Join, &join) != nil || join.Type != "session_join" || join.Ticket == "" || join.Versions != s.wireVersions() {
		return nil, ErrSessionRuntime
	}
	if _, header, err := s.validate(raw, false); err != nil || header.kind != "session_participant_open" || !s.participantInnerBinding(raw, header.kind) {
		return nil, ErrSessionRuntime
	}
	// Own every byte; neither caller changes nor reused RawMessage aliases count
	// as an observation of what the single writer actually sent.
	var owned protocol.SessionParticipantOpen
	if json.Unmarshal(raw, &owned) != nil {
		return nil, ErrSessionRuntime
	}
	st := &sessionParticipantState{done: make(chan struct{}), activation: a, open: owned, join: join, predecessor: old}
	s.mu.Lock()
	if s.closed || (old != nil && (!s.participantStateLocked(old) || !old.left || old.activation != a || old.successor != nil || q.ConnectionRef != old.open.ConnectionRef || q.RequestID == old.open.RequestID || join.RequestID == old.join.RequestID || join.Ticket == old.join.Ticket)) || !s.reserveLocked(participantBindingCharge) {
		s.mu.Unlock()
		return nil, ErrSessionRuntime
	}
	if s.participantStates == nil {
		s.participantStates = make(map[*sessionParticipantState]struct{})
	}
	pending := &SessionParticipantPending{s, st}
	st.pending = pending
	s.participantStates[st] = struct{}{}
	if old != nil {
		old.successor = st
	}
	s.mu.Unlock()
	return pending, nil
}

// SendPreparedParticipantOpen must follow publication of this exact Pending.
// Once a writer starts it, uncertainty is fatal and it cannot be discarded.
func (s *SessionRuntime) SendPreparedParticipantOpen(ctx context.Context, a *SessionActivation, p *SessionParticipantPending) error {
	if ctx == nil || p == nil || p.runtime != s {
		return ErrSessionRuntime
	}
	s.mu.Lock()
	st := p.state
	if !s.participantStateLocked(st) || st.pending != p || st.activation != a || st.writing || st.sent {
		s.mu.Unlock()
		return ErrSessionRuntime
	}
	open := st.open
	s.mu.Unlock()
	if err := s.participantBinding(a, open.Scope, open.SessionID, open.RuntimeRef, open.Fence); err != nil {
		return err
	}
	raw, err := json.Marshal(open)
	if err != nil {
		return ErrSessionRuntime
	}
	return s.sendParticipantTracked(ctx, a, raw, st)
}

// DiscardUnsentParticipantOpen atomically excludes a writer taking this Pending.
// An uncertain physical write is never made reusable by discarding metadata.
func (s *SessionRuntime) DiscardUnsentParticipantOpen(p *SessionParticipantPending) error {
	if p == nil || p.runtime != s {
		return ErrSessionRuntime
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := p.state
	if !s.participantStateLocked(st) || st.pending != p || st.writing || st.sent || st.ticketSent || st.captured || len(st.ticketBytes) != 0 {
		return ErrSessionRuntime
	}
	s.releaseParticipantStateLocked(st)
	return nil
}
func (s *SessionRuntime) wireVersions() protocol.Versions {
	return protocol.Versions{ProtocolVersion: s.versions.Protocol, ModelVersion: s.versions.Model, EngineVersion: s.versions.Engine}
}
func (s *SessionRuntime) participantStateLocked(st *sessionParticipantState) bool {
	_, ok := s.participantStates[st]
	return ok && !st.released && !s.closed
}

// prepareParticipantTicketLocked runs BEFORE the physical reply. The bytes
// have already passed the closed schema and are covered by the reservation.
func (s *SessionRuntime) prepareParticipantTicketLocked(entry *sessionControlEntry, raw []byte) (*sessionParticipantState, error) {
	if entry.kind != "ticket_validate" {
		return nil, nil
	}
	var q protocol.TicketValidationRequest
	var result protocol.TicketValidationResult
	if json.Unmarshal(entry.bytes, &q) != nil || json.Unmarshal(raw, &result) != nil {
		return nil, ErrSessionRuntime
	}
	var found *sessionParticipantState
	for st := range s.participantStates {
		if st.released || st.captured || st.join.Ticket != q.Ticket {
			continue
		}
		if found != nil {
			return nil, ErrSessionRuntime
		}
		found = st
	}
	if found == nil {
		return nil, nil
	}
	st := found
	openBytes, err := json.Marshal(st.open)
	if err != nil {
		return nil, ErrSessionRuntime
	}
	joinBytes, err := json.Marshal(st.join)
	if err != nil {
		return nil, ErrSessionRuntime
	}
	if len(openBytes)+len(joinBytes)+2*len(raw) > participantBindingCharge {
		return nil, ErrSessionRuntime
	}
	if st.declining || st.declined || (!st.writing && !st.sent) || st.ticketSent || len(st.ticketBytes) != 0 || result.Granted.Read == nil || !*result.Granted.Read || q.Scope != st.open.Scope || q.SessionID != st.open.SessionID || q.Fence != st.open.Fence || result.RequestID != q.RequestID || result.Scope != q.Scope || result.SessionID != q.SessionID || result.Generation != q.Fence.Generation || result.ValidationNonce != q.ValidationNonce || result.Versions != st.join.Versions || result.ParticipantRef == "" || result.GateID == "" || len(raw)+len(st.open.Join) > participantBindingCharge/2 {
		return nil, ErrSessionRuntime
	}
	st.ticket = result
	st.ticketBytes = append([]byte(nil), raw...)
	return st, nil
}

func (s *SessionRuntime) CaptureParticipantJoined(a *SessionActivation, p *SessionParticipantPending, c SessionControl) (*SessionParticipantBinding, error) {
	if s == nil || p == nil || p.runtime != s || a == nil || c.runtime != s {
		return nil, ErrSessionRuntime
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := p.state
	entry := s.child[c.id]
	if !s.participantStateLocked(st) || st.pending != p || st.activation != a || !st.sent || !st.ticketSent || st.captured || entry == nil || !entry.borrowed || entry.kind != "session_participant_output" {
		return nil, ErrSessionRuntime
	}
	var q protocol.SessionParticipantOutput
	var joined protocol.SessionJoined
	if json.Unmarshal(entry.bytes, &q) != nil || json.Unmarshal(q.Message, &joined) != nil || joined.Type != "session_joined" || q.RequestID != st.open.RequestID || q.Scope != st.open.Scope || q.SessionID != st.open.SessionID || q.RuntimeRef != st.open.RuntimeRef || q.Fence != st.open.Fence || q.ConnectionRef != st.open.ConnectionRef || q.ParticipantRef != st.ticket.ParticipantRef || q.GateID != st.ticket.GateID || q.PermissionVersion != st.ticket.PermissionVersion || joined.RequestID != st.join.RequestID || joined.Scope != q.Scope || joined.SessionID != q.SessionID || joined.Generation != q.Fence.Generation || joined.ParticipantRef != q.ParticipantRef || joined.GateID != q.GateID || joined.PermissionVersion != q.PermissionVersion || joined.Versions != st.join.Versions {
		return nil, ErrSessionRuntime
	}
	// Validate the entire predecessor relation before publishing any new
	// captured tuple or advancing its old-gate FD watermark.
	if old := st.predecessor; old != nil {
		if !s.participantStateLocked(old) || !old.left || old.successor != st || old.participant != string(q.ParticipantRef) || old.gate == string(q.GateID) {
			return nil, ErrSessionRuntime
		}
	}
	st.captured = true
	st.participant = string(q.ParticipantRef)
	st.gate = string(q.GateID)
	st.permission = string(q.PermissionVersion)
	if old := st.predecessor; old != nil {
		old.watermark = c.id
	}
	s.drainParticipantRetirementsLocked()
	return &SessionParticipantBinding{s, st}, nil
}

// RetireParticipant is a denial-only transition. A generic callback error or
// caller tuple cannot establish that the ORIGINAL SQL gate is closed.
func (s *SessionRuntime) RetireParticipant(ctx context.Context, a *SessionActivation, b *SessionParticipantBinding) (*SessionRetiredDelivery, error) {
	if ctx == nil || b == nil || b.runtime != s || a == nil {
		return nil, ErrSessionRuntime
	}
	s.mu.Lock()
	st := b.state
	if !s.participantStateLocked(st) || st.activation != a || !st.captured || st.retiring {
		s.mu.Unlock()
		return nil, ErrSessionRuntime
	}
	// Close new reply reservations now. An already reserved reply still owns
	// its original first-send grant and must finish before the physical Leave.
	st.retiring = true
	open := st.open
	gateID, participant, permission := st.gate, st.participant, st.permission
	s.mu.Unlock()
	if current, e := a.SessionRuntime(); e != nil || current != s {
		s.fail(ErrSessionRuntime)
		return nil, ErrSessionRuntime
	}
	e := a.store.config.Owners.transaction(ctx, func(tx *sql.Tx, _ string, _ int64) error {
		if e := a.store.config.Owners.Check(ctx, tx, a.owner); e != nil {
			return e
		}
		gate, e := loadDeliveryGate(ctx, tx, a.owner, gateID)
		if e != nil {
			return e
		}
		if !gate.closed || gate.participant != participant || gate.version != permission {
			return ErrSessionRuntime
		}
		return a.store.config.Owners.Check(ctx, tx, a.owner)
	})
	if e != nil {
		s.fail(e)
		return nil, e
	}
	for {
		s.mu.Lock()
		if s.closed {
			err := s.err
			s.mu.Unlock()
			return nil, err
		}
		waiting := false
		for _, entry := range s.child {
			if entry.replying && s.retiredEntryLocked(st, entry) {
				waiting = true
				break
			}
		}
		notify := s.notify
		s.mu.Unlock()
		if !waiting {
			break
		}
		// Neither the runtime mutex nor the SQL transaction spans this wait.
		// Completion signals under the same mutex, so no wake can be lost.
		select {
		case <-ctx.Done():
			s.fail(ctx.Err())
			return nil, ctx.Err()
		case <-s.closedCh:
			return nil, s.Err()
		case <-notify:
		}
	}
	id, e := prepareRequestID()
	if e != nil {
		s.fail(e)
		return nil, e
	}
	leave, e := json.Marshal(protocol.SessionLeave{Type: "session_leave", RequestID: id, Scope: open.Scope, SessionID: open.SessionID})
	if e != nil {
		s.fail(e)
		return nil, e
	}
	q := protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: protocol.Identifier(string(id) + "-leave"), ControlProfile: PrivateSessionProfile, Scope: open.Scope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: open.Fence, ConnectionRef: open.ConnectionRef, ParticipantRef: protocol.Identifier(participant), GateID: protocol.Identifier(gateID), PermissionVersion: protocol.Counter(permission), Message: leave}
	if e = s.SendParticipantInput(ctx, a, q); e != nil {
		s.fail(e)
		return nil, e
	}
	s.mu.Lock()
	st.left = true
	s.mu.Unlock()
	return &SessionRetiredDelivery{s, st}, nil
}
func (s *SessionRuntime) SendParticipantReopen(ctx context.Context, a *SessionActivation, r *SessionRetiredDelivery, q protocol.SessionParticipantOpen) (*SessionParticipantPending, error) {
	if r == nil || r.runtime != s {
		return nil, ErrSessionRuntime
	}
	p, err := s.PrepareParticipantReopen(a, r, q)
	if err != nil {
		return nil, err
	}
	if err = s.SendPreparedParticipantOpen(ctx, a, p); err != nil {
		return nil, err
	}
	return p, nil
}

// PrepareParticipantReopen preserves the original activation and connection.
func (s *SessionRuntime) PrepareParticipantReopen(a *SessionActivation, r *SessionRetiredDelivery, q protocol.SessionParticipantOpen) (*SessionParticipantPending, error) {
	if r == nil || r.runtime != s {
		return nil, ErrSessionRuntime
	}
	return s.prepareParticipantOpen(a, q, r.state)
}
func (s *SessionRuntime) retiredEntryLocked(st *sessionParticipantState, entry *sessionControlEntry) bool {
	if entry == nil {
		return false
	}
	switch entry.kind {
	case "delivery_renew":
		var q protocol.DeliveryRenew
		return json.Unmarshal(entry.bytes, &q) == nil && q.Scope == st.open.Scope && q.SessionID == st.open.SessionID && q.Fence == st.open.Fence && string(q.ParticipantRef) == st.participant && string(q.GateID) == st.gate && string(q.PermissionVersion) == st.permission
	case "session_participant_output":
		var q protocol.SessionParticipantOutput
		return json.Unmarshal(entry.bytes, &q) == nil && q.Scope == st.open.Scope && q.SessionID == st.open.SessionID && q.RuntimeRef == st.open.RuntimeRef && q.Fence == st.open.Fence && q.ConnectionRef == st.open.ConnectionRef && string(q.ParticipantRef) == st.participant && string(q.GateID) == st.gate && string(q.PermissionVersion) == st.permission
	}
	return false
}
func (s *SessionRuntime) ValidateRetiredOutput(c SessionControl, r *SessionRetiredDelivery) error {
	if r == nil || r.runtime != s || c.runtime != s {
		return ErrSessionRuntime
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := r.state
	entry := s.child[c.id]
	if !s.participantStateLocked(st) || !st.left || entry == nil || !entry.borrowed || entry.kind != "session_participant_output" || (st.watermark != 0 && c.id >= st.watermark) || !s.retiredEntryLocked(st, entry) {
		return ErrSessionRuntime
	}
	return nil
}
func (s *SessionRuntime) CompleteRetiredDeliveryControl(c SessionControl, r *SessionRetiredDelivery) error {
	if r == nil || r.runtime != s || c.runtime != s {
		return ErrSessionRuntime
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := r.state
	entry := s.child[c.id]
	if !s.participantStateLocked(st) || !st.left || entry == nil || !entry.borrowed || entry.replying || entry.kind != "delivery_renew" || (st.watermark != 0 && c.id >= st.watermark) || !s.retiredEntryLocked(st, entry) {
		return ErrSessionRuntime
	}
	delete(s.child, c.id)
	s.removeChildIDLocked(entry)
	s.count--
	s.bytes -= len(entry.bytes)
	s.drainParticipantRetirementsLocked()
	return nil
}
func (s *SessionRuntime) drainParticipantRetirementsLocked() {
	for st := range s.participantStates {
		if !st.left || st.watermark == 0 {
			continue
		}
		pending := false
		for _, entry := range s.child {
			if s.retiredEntryLocked(st, entry) {
				pending = true
				break
			}
		}
		if !pending {
			s.releaseParticipantStateLocked(st)
		}
	}
}

// A released proof may still be referenced by a caller. Erase its payload and
// both graph links as well as removing its charged runtime-table membership.
func (s *SessionRuntime) releaseParticipantStateLocked(st *sessionParticipantState) {
	if st == nil || st.released {
		return
	}
	if _, ok := s.participantStates[st]; !ok {
		return
	}
	if old := st.predecessor; old != nil && old.successor == st {
		old.successor = nil
	}
	if next := st.successor; next != nil && next.predecessor == st {
		next.predecessor = nil
	}
	st.predecessor = nil
	st.successor = nil
	st.pending = nil
	st.activation = nil
	st.open = protocol.SessionParticipantOpen{}
	st.join = protocol.SessionJoin{}
	st.ticket = protocol.TicketValidationResult{}
	st.ticketBytes = nil
	st.participant = ""
	st.gate = ""
	st.permission = ""
	st.released = true
	close(st.done)
	delete(s.participantStates, st)
	s.count--
	s.bytes -= participantBindingCharge
}
func (s *SessionRuntime) releaseAllParticipantStatesLocked() {
	for st := range s.participantStates {
		s.releaseParticipantStateLocked(st)
	}
}
func (s *SessionRuntime) participantGrantAllowedLocked(entry *sessionControlEntry) bool {
	if entry.kind != "delivery_renew" {
		return true
	}
	for st := range s.participantStates {
		if st.retiring && s.retiredEntryLocked(st, entry) {
			return false
		}
	}
	return true
}

// Done observes erased metadata lifetime only. It is not a delivery, source,
// revocation ACK, or current-gate authority. Zero proofs have no signal.
func (r *SessionRetiredDelivery) Done() <-chan struct{} {
	if r == nil || r.runtime == nil || r.state == nil {
		return nil
	}
	return r.state.done
}

// DeclineParticipantTicket writes ONLY the fixed denial for this actual pending
// admission. It grants no rights, invents no gate/expiry, and is not a native ACK.
// Source denial remains the original API's successful current-author SQL proof.
func (s *SessionRuntime) DeclineParticipantTicket(ctx context.Context, a *SessionActivation, p *SessionParticipantPending, c SessionControl) error {
	if ctx == nil || a == nil || a.RequireHumanNativePrepare() != nil || p == nil || p.runtime != s || c.runtime != s {
		return ErrSessionRuntime
	}
	s.mu.Lock()
	st := p.state
	entry := s.child[c.id]
	var request protocol.TicketValidationRequest
	if !s.participantStateLocked(st) || st.pending != p || st.activation != a || !st.sent || st.captured || st.ticketSent || st.declining || st.declined || len(st.ticketBytes) != 0 || entry == nil || !entry.borrowed || entry.replying || entry.kind != "ticket_validate" || json.Unmarshal(entry.bytes, &request) != nil || request.Scope != st.open.Scope || request.SessionID != st.open.SessionID || request.Fence != st.open.Fence || request.Ticket != st.join.Ticket {
		s.mu.Unlock()
		return ErrSessionRuntime
	}
	if old := st.predecessor; old != nil && (!s.participantStateLocked(old) || !old.left || old.activation != a || old.successor != st) {
		s.mu.Unlock()
		return ErrSessionRuntime
	}
	// Atomically exclude a positive reply before taking the physical writer.
	entry.replying = true
	st.declining = true
	scope := request.Scope
	s.mu.Unlock()
	raw, err := json.Marshal(protocol.ProtocolError{Type: "protocol_error", RequestID: request.RequestID, Scope: &scope, Reason: protocol.Rejection{Code: "permission_revoked", Message: "participant permission revoked", Retry: "do_not_retry"}})
	if err == nil {
		raw, err = CanonicalE3WireJSON(raw)
	}
	if err != nil {
		s.fail(err)
		return err
	}
	// Generic session validate/ReplyControl remain unchanged and reject this
	// type. The internally constructed, closed public object is the ONLY escape.
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || !s.validator.schema.matches(s.validator.schema.root, value, 0) {
		s.fail(ErrSessionRuntime)
		return ErrSessionRuntime
	}
	err = a.store.config.Owners.transaction(ctx, func(tx *sql.Tx, _ string, _ int64) error {
		if current, e := a.SessionRuntime(); e != nil || current != s {
			return ErrSessionRuntime
		}
		if e := a.store.config.Owners.Check(ctx, tx, a.owner); e != nil {
			return e
		}
		if e := s.write(ctx, raw); e != nil {
			return e
		}
		return a.store.config.Owners.Check(ctx, tx, a.owner)
	})
	if err != nil {
		s.fail(err)
		return err
	}
	s.mu.Lock()
	st.declined = true
	st.declining = false
	s.mu.Unlock()
	// Metadata deliberately stays charged: this negative write is not a Joined
	// watermark proving the native reader consumed it or drained old outputs.
	if err = s.complete(c, true); err != nil {
		s.fail(err)
	}
	return err
}
