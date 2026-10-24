package mooring

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// SendParticipantOpen sends one bounded host notification on the original FD.
// Activation is initial transport provenance, NOT current author, owner or
// recipient authority. The host must retain those operation leases, validate
// the original ticket and bound raw ingress before calling this method. Success
// means transport write only: no join, acceptance, terminal outcome or Saved.
func (s *SessionRuntime) SendParticipantOpen(ctx context.Context, activation *SessionActivation, request protocol.SessionParticipantOpen) error {
	if ctx == nil || request.Type != "session_participant_open" || string(request.ControlProfile) != PrivateSessionProfile || !batchID(string(request.RequestID)) || !batchID(string(request.ConnectionRef)) || len(request.Join) == 0 || len(request.Join) > sessionParticipantFrameBytes {
		return ErrSessionRuntime
	}
	if err := s.participantBinding(activation, request.Scope, request.SessionID, request.RuntimeRef, request.Fence); err != nil {
		return err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ErrSessionRuntime
	}
	return s.sendParticipant(ctx, activation, raw)
}

// SendParticipantInput encodes the original inner message in the closed
// canonical envelope; callers retain original raw proposal bytes separately.
// No source seal or write permission is
// inferred from its participant/gate identifiers or from the supplied JSON.
func (s *SessionRuntime) SendParticipantInput(ctx context.Context, activation *SessionActivation, request protocol.SessionParticipantInput) error {
	if ctx == nil || request.Type != "session_participant_input" || string(request.ControlProfile) != PrivateSessionProfile || !batchID(string(request.RequestID)) || !batchID(string(request.ConnectionRef)) || !batchID(string(request.ParticipantRef)) || !batchID(string(request.GateID)) || len(request.Message) == 0 || len(request.Message) > sessionParticipantFrameBytes {
		return ErrSessionRuntime
	}
	if err := s.participantBinding(activation, request.Scope, request.SessionID, request.RuntimeRef, request.Fence); err != nil {
		return err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ErrSessionRuntime
	}
	return s.sendParticipant(ctx, activation, raw)
}

const sessionParticipantFrameBytes = 256 << 10

func (s *SessionRuntime) participantBinding(a *SessionActivation, scope protocol.Scope, session, runtime protocol.Identifier, fence protocol.Fence) error {
	if s == nil || a == nil || a.runtime != s || a.store == nil || a.owner.Key != s.scope || a.owner.OwnerID != s.runtimeRef || a.sessionID != string(session) || string(runtime) != s.runtimeRef ||
		string(scope.WorkspaceID) != s.scope.WorkspaceID || string(scope.ArtifactID) != s.scope.ArtifactID || string(scope.ApplicationID) != s.scope.ApplicationID ||
		string(fence.Generation) != strconv.FormatInt(a.owner.Generation, 10) || fence.Token != a.owner.Token {
		return ErrSessionRuntime
	}
	if current, err := a.SessionRuntime(); err != nil || current != s {
		return ErrSessionRuntime
	}
	return nil
}

func (s *SessionRuntime) sendParticipant(ctx context.Context, activation *SessionActivation, raw []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(raw) > sessionParticipantFrameBytes {
		return ErrSessionRuntime
	}
	normalized, header, err := s.validate(raw, false)
	if err != nil || len(normalized) > sessionParticipantFrameBytes || (header.kind != "session_participant_open" && header.kind != "session_participant_input") {
		return ErrSessionRuntime
	}
	// Charge the same global control budget while queued and during I/O. The
	// normalized bytes are now owned; no caller slice is retained after return.
	s.mu.Lock()
	if s.closed || !s.observed.activated || s.observed.invalid || !s.reserveLocked(len(normalized)) {
		s.mu.Unlock()
		return ErrSessionRuntime
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.count--; s.bytes -= len(normalized); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err() // queued cancellation emitted no bytes
	case <-s.closedCh:
		return s.Err()
	case <-s.writer:
	}
	defer func() { s.writer <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Recheck initial process provenance AFTER waiting for the single FD writer.
	// No policy callback or replacement transport can be injected here.
	if current, e := activation.SessionRuntime(); e != nil || current != s {
		return ErrSessionRuntime
	}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { s.fail(ctx.Err()); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	deadline, _ := ctx.Deadline()
	err = s.process.RPC.conn.SetWriteDeadline(deadline)
	if err == nil {
		err = s.process.RPC.Write(normalized)
	}
	if err == nil {
		err = s.process.RPC.conn.SetWriteDeadline(time.Time{})
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.fail(err)
	}
	return err
}
