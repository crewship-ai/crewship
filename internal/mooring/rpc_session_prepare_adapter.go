package mooring

import (
	"context"
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
	return a.causal.PrepareCausalTargets(ctx, checkpoint, change, labels)
}
