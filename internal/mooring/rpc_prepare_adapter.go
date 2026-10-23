package mooring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// RPCPrepareAdapter is an exclusive host-initiated client for a verified child.
// It never installs a candidate or issues Saved; BatchStore owns the SQL commit.
// Scope and selected versions are immutable host configuration, not user input.
type RPCPrepareAdapter struct {
	runtime   *Runtime
	scope     SessionKey
	versions  RuntimeVersions
	validator *privateControlValidator
}

func NewRPCPrepareAdapter(r *Runtime, scope SessionKey, versions RuntimeVersions) (*RPCPrepareAdapter, error) {
	if r == nil || r.RPC == nil || r.selectedControlProfile != PrivatePrepareProfile || r.selectedVersions != versions || !batchID(scope.WorkspaceID) || !batchID(scope.ArtifactID) || !batchID(scope.ApplicationID) {
		return nil, ErrPrivateControl
	}
	v, err := newPrivateControlValidator()
	if err != nil {
		return nil, err
	}
	return &RPCPrepareAdapter{r, scope, versions, v}, nil
}

func (a *RPCPrepareAdapter) wireScope() protocol.Scope {
	return protocol.Scope{WorkspaceID: protocol.Identifier(a.scope.WorkspaceID), ArtifactID: protocol.Identifier(a.scope.ArtifactID), ApplicationID: protocol.Identifier(a.scope.ApplicationID)}
}
func (a *RPCPrepareAdapter) wireVersions() protocol.Versions {
	return protocol.Versions{ProtocolVersion: a.versions.Protocol, ModelVersion: a.versions.Model, EngineVersion: a.versions.Engine}
}
func prepareRequestID() (protocol.Identifier, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return protocol.Identifier(hex.EncodeToString(b[:])), nil
}
func (a *RPCPrepareAdapter) exchange(ctx context.Context, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, ErrPrivateControl
	}
	raw, err = a.validator.validate(raw, false)
	if err != nil {
		return nil, err
	}
	response, err := a.runtime.Exchange(ctx, raw)
	if err != nil {
		return nil, err
	}
	response, err = a.validator.validate(response, true)
	if err != nil {
		_ = a.runtime.RPC.Close()
		return nil, err
	}
	return response, nil
}
func (a *RPCPrepareAdapter) invalid() error { _ = a.runtime.RPC.Close(); return ErrPrivateControl }

func (a *RPCPrepareAdapter) ValidateTargets(ctx context.Context, checkpoint []byte) ([]BatchTargetIdentity, error) {
	c, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		return nil, err
	}
	id, err := prepareRequestID()
	if err != nil {
		return nil, err
	}
	request := protocol.ValidateCheckpoint{Type: "validate_checkpoint", RequestID: id, Scope: a.wireScope(), Selected: a.wireVersions(), Checkpoint: c, ControlProfile: PrivatePrepareProfile}
	raw, err := a.exchange(ctx, request)
	if err != nil {
		return nil, err
	}
	var response protocol.CheckpointValidated
	if json.Unmarshal(raw, &response) != nil || response.Type != "checkpoint_validated" || response.RequestID != id || response.Scope != request.Scope || response.Selected != request.Selected || response.InputCheckpointDigest != c.Digest {
		return nil, a.invalid()
	}
	var result []BatchTargetIdentity
	if convertPrepareValue(response.Inventory, &result) != nil || !validPrepareInventory(result) {
		return nil, a.invalid()
	}
	return result, nil
}

// All structure conversion retains normalized opaque command JSON. The schema
// checks closed wire objects before any response value reaches this conversion.
func convertPrepareValue(value, destination any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, destination)
}
func validPrepareInventory(inventory []BatchTargetIdentity) bool {
	if inventory == nil || len(inventory) > 65536 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range inventory {
		if !batchID(id.TargetID) || seen[id.TargetID] {
			return false
		}
		seen[id.TargetID] = true
	}
	return true
}

func (a *RPCPrepareAdapter) PrepareTargets(ctx context.Context, checkpoint []byte, change BatchChange, labels BatchPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	var empty BatchTargetCandidate
	// Independently bind the immutable change to this adapter's exact scope.
	normalized, err := normalizeBatch(BatchRequest{Lease: OwnerLease{Key: a.scope}, ExpectedRevision: labels.DocumentRevision, Changes: []BatchChange{change}})
	if err != nil || len(normalized) != 1 {
		return empty, nil, ErrPrivateControl
	}
	c, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		return empty, nil, err
	}
	id, err := prepareRequestID()
	if err != nil {
		return empty, nil, err
	}
	request := protocol.PrepareAtomic{Type: "prepare_atomic", RequestID: id, Scope: a.wireScope(), Selected: a.wireVersions(), Checkpoint: c, ControlProfile: PrivatePrepareProfile}
	if convertPrepareValue(normalized[0], &request.Change) != nil || convertPrepareValue(labels, &request.Context) != nil {
		return empty, nil, ErrPrivateControl
	}
	raw, err := a.exchange(ctx, request)
	if err != nil {
		return empty, nil, err
	}
	var response protocol.AtomicPrepared
	if json.Unmarshal(raw, &response) != nil || response.Type != "atomic_prepared" || response.RequestID != id || response.Scope != request.Scope || response.Selected != request.Selected || response.InputCheckpointDigest != c.Digest || response.ChangeID != request.Change.ChangeID || response.MembershipHash != request.Change.MembershipHash {
		return empty, nil, a.invalid()
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(response.Outcome, &kind) != nil {
		return empty, nil, a.invalid()
	}
	if kind.Kind == "failure" {
		var failed protocol.PreparedDirectFailure
		var reason BatchRejection
		if json.Unmarshal(response.Outcome, &failed) != nil || convertPrepareValue(failed.Reason, &reason) != nil {
			return empty, nil, a.invalid()
		}
		failure := &PreparedFailure{OperationID: string(failed.OpID), Reason: &reason}
		if !validPreparedFailure(normalized[0], failure) {
			return empty, nil, a.invalid()
		}
		return empty, failure, nil
	}
	if kind.Kind != "candidate" {
		return empty, nil, a.invalid()
	}
	var candidate protocol.PreparedCandidate
	if json.Unmarshal(response.Outcome, &candidate) != nil {
		return empty, nil, a.invalid()
	}
	decoded, err := decodePreparedCheckpoint(candidate.Checkpoint)
	if err != nil {
		return empty, nil, a.invalid()
	}
	result := BatchTargetCandidate{Checkpoint: decoded}
	if convertPrepareValue(candidate.Inventory, &result.Inventory) != nil || convertPrepareValue(candidate.Effects, &result.Effects) != nil || !validPrepareInventory(result.Inventory) || len(result.Effects) != len(change.Operations) {
		return empty, nil, a.invalid()
	}
	for i, effect := range result.Effects {
		if effect.OperationID != change.Operations[i].ID || effect.Targets == nil || len(effect.Targets) > 4096 {
			return empty, nil, a.invalid()
		}
		seen := map[string]bool{}
		for _, target := range effect.Targets {
			if !batchID(target.TargetID) || seen[target.TargetID] || !(target.ContentChanged || target.StructureChanged) || (target.Created && target.Deleted) || ((target.Created || target.Deleted) && !target.StructureChanged) {
				return empty, nil, a.invalid()
			}
			seen[target.TargetID] = true
		}
	}
	// Reconstruct effects against the qualified original host inventory before
	// returning any candidate. BatchStore must independently repeat at commit.
	if !consistentPrepareEffects(labels, result) {
		return empty, nil, a.invalid()
	}
	return result, nil, nil
}

func consistentPrepareEffects(context BatchPrepareContext, candidate BatchTargetCandidate) bool {
	working := map[string]bool{}
	for _, target := range context.Targets {
		if _, exists := working[target.TargetID]; exists {
			return false
		}
		working[target.TargetID] = target.Deleted
	}
	for _, op := range candidate.Effects {
		for _, effect := range op.Targets {
			deleted, exists := working[effect.TargetID]
			if effect.Created {
				if exists {
					return false
				}
			} else if !exists || deleted {
				return false
			}
			working[effect.TargetID] = effect.Deleted
		}
	}
	actual := map[string]bool{}
	for _, target := range candidate.Inventory {
		actual[target.TargetID] = target.Deleted
	}
	return reflect.DeepEqual(working, actual)
}
