package mooring

import (
	"context"
	"encoding/json"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"time"
)

// SessionCausalAdapter can only be created from the concrete negotiated session
// process/multiplexer. It never exposes a raw Runtime or its legacy Exchange.
// This is transport/scope provenance, not operation authority or Saved.
type SessionCausalAdapter struct {
	causal  *CausalRPCPrepareAdapter
	session *SessionRuntime
}

func NewSessionCausalAdapter(s *SessionRuntime, scope SessionKey, versions RuntimeVersions) (*SessionCausalAdapter, error) {
	if s == nil || s.process == nil || s.process.RPC == nil || s.process.selectedControlProfile != PrivateSessionProfile || s.process.selectedVersions != versions || s.scope != scope || s.versions != versions || scope.ApplicationID != "wordcraft" || !batchID(scope.WorkspaceID) || !batchID(scope.ArtifactID) {
		return nil, ErrPrivateControl
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, ErrPrivateControl
	}
	v, err := newPrivateControlValidator()
	if err != nil {
		return nil, err
	}
	base := &RPCPrepareAdapter{runtime: s.process, scope: scope, versions: versions, validator: v}
	return &SessionCausalAdapter{session: s, causal: &CausalRPCPrepareAdapter{base: base, roundTrip: func(ctx context.Context, raw []byte) ([]byte, error) { return s.request(ctx, raw, 5*time.Second) }}}, nil
}
func (a *SessionCausalAdapter) ValidateTargets(ctx context.Context, checkpoint []byte) ([]BatchTargetIdentity, error) {
	return a.causal.ValidateTargets(ctx, checkpoint)
}
func (a *SessionCausalAdapter) PrepareTargets(context.Context, []byte, BatchChange, BatchPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	return BatchTargetCandidate{}, nil, ErrPrivateControl
}
func (a *SessionCausalAdapter) PrepareCausalTargets(ctx context.Context, checkpoint []byte, change BatchChange, labels BatchCausalPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	if a != nil && a.session != nil && a.session.requireHumanNativePrepare() == nil {
		return BatchTargetCandidate{}, nil, ErrPrivateControl
	}
	return a.causal.PrepareCausalTargets(ctx, checkpoint, change, labels)
}

func (a *SessionCausalAdapter) PrepareSessionTargets(ctx context.Context, checkpoint []byte, change BatchChange, labels BatchCausalPrepareContext, human *protocol.SessionHumanPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	if a == nil || a.session == nil || a.causal == nil || a.session.requireHumanNativePrepare() != nil {
		return BatchTargetCandidate{}, nil, ErrPrivateControl
	}
	return a.causal.prepareCausalTargets(ctx, checkpoint, change, labels, human, true)
}

// DescribeCheckpoint uses the same concrete sole-reader lane, even while Open
// awaits ownership. It does not install a candidate or grant scope authority.
func (a *SessionCausalAdapter) DescribeCheckpoint(ctx context.Context, checkpoint []byte) (BatchCheckpointDescription, error) {
	var empty BatchCheckpointDescription
	if a == nil || a.session == nil || a.causal == nil {
		return empty, ErrPrivateControl
	}
	opaque, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		return empty, err
	}
	id, err := prepareRequestID()
	if err != nil {
		return empty, err
	}
	a.session.mu.Lock()
	sessionID := a.session.sessionID
	a.session.mu.Unlock()
	if !batchID(sessionID) {
		return empty, ErrPrivateControl
	}
	request := protocol.SessionDescribeCheckpoint{Type: "session_describe_checkpoint", RequestID: id, ControlProfile: PrivateSessionProfile, Scope: a.causal.base.wireScope(), SessionID: protocol.Identifier(sessionID), RuntimeRef: protocol.Identifier(a.session.runtimeRef), Versions: a.causal.base.wireVersions(), Checkpoint: opaque}
	raw, err := json.Marshal(request)
	if err != nil {
		return empty, ErrPrivateControl
	}
	raw, err = a.session.request(ctx, raw, 5*time.Second)
	if err != nil {
		return empty, err
	}
	var response protocol.SessionCheckpointDescribed
	if json.Unmarshal(raw, &response) != nil || response.Type != "session_checkpoint_described" || response.RequestID != id || response.Scope != request.Scope || response.SessionID != request.SessionID || response.RuntimeRef != request.RuntimeRef || response.Versions != request.Versions || response.InputDigest != opaque.Digest || response.Description.Versions != request.Versions {
		a.session.fail(ErrPrivateControl)
		return empty, ErrPrivateControl
	}
	description := BatchCheckpointDescription{FormatVersion: response.Description.FormatVersion, Versions: response.Description.Versions, Assets: response.Description.Assets}
	if _, err = canonicalCheckpointDescription(description, sessionOutputBytes); err != nil {
		a.session.fail(err)
		return empty, err
	}
	return description, nil
}
