package mooring

import (
	"context"
	"encoding/json"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// CausalRPCPrepareAdapter is an explicit causal-only capability. Its private
// base can only be constructed from actual successful Runtime negotiation.
// Configuration strings and deserialized RuntimeReady objects are not proof.
type CausalRPCPrepareAdapter struct {
	base *RPCPrepareAdapter
	// Only the concrete session factory installs this private correlated lane.
	// Nil preserves the legacy exclusive Runtime.Exchange path unchanged.
	roundTrip func(context.Context, []byte) ([]byte, error)
}

func NewCausalRPCPrepareAdapter(r *Runtime, scope SessionKey, versions RuntimeVersions) (*CausalRPCPrepareAdapter, error) {
	if r == nil || r.RPC == nil || r.selectedControlProfile != PrivateCausalPrepareProfile || r.selectedVersions != versions || !batchID(scope.WorkspaceID) || !batchID(scope.ArtifactID) || !batchID(scope.ApplicationID) {
		return nil, ErrPrivateControl
	}
	v, err := newPrivateControlValidator()
	if err != nil {
		return nil, err
	}
	return &CausalRPCPrepareAdapter{base: &RPCPrepareAdapter{r, scope, versions, v}}, nil
}

// A causal runtime cannot be silently used for the old strict store policy.
func (a *CausalRPCPrepareAdapter) PrepareTargets(context.Context, []byte, BatchChange, BatchPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	return BatchTargetCandidate{}, nil, ErrPrivateControl
}
func (a *CausalRPCPrepareAdapter) exchange(ctx context.Context, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, ErrPrivateControl
	}
	raw, err = a.base.validator.validateCausal(raw, privateCausalHost)
	if err != nil {
		return nil, err
	}
	var response []byte
	if a.roundTrip != nil {
		response, err = a.roundTrip(ctx, raw)
	} else {
		response, err = a.base.runtime.Exchange(ctx, raw)
	}
	if err != nil {
		return nil, err
	}
	response, err = a.base.validator.validateCausal(response, privateCausalRuntime)
	if err != nil {
		return nil, a.base.invalid()
	}
	return response, nil
}

func (a *CausalRPCPrepareAdapter) ValidateTargets(ctx context.Context, checkpoint []byte) ([]BatchTargetIdentity, error) {
	c, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		return nil, err
	}
	id, err := prepareRequestID()
	if err != nil {
		return nil, err
	}
	request := protocol.ValidateCheckpointCausal{Type: "validate_checkpoint_causal", RequestID: id, Scope: a.base.wireScope(), Selected: a.base.wireVersions(), Checkpoint: c, ControlProfile: PrivateCausalPrepareProfile}
	raw, err := a.exchange(ctx, request)
	if err != nil {
		return nil, err
	}
	var response protocol.CheckpointValidatedCausal
	if json.Unmarshal(raw, &response) != nil || response.Type != "checkpoint_validated_causal" || response.RequestID != id || response.Scope != request.Scope || response.Selected != request.Selected || response.InputCheckpointDigest != c.Digest {
		return nil, a.base.invalid()
	}
	var result []BatchTargetIdentity
	if convertPrepareValue(response.Inventory, &result) != nil || !validPrepareInventory(result) {
		return nil, a.base.invalid()
	}
	return result, nil
}

func (a *CausalRPCPrepareAdapter) PrepareCausalTargets(ctx context.Context, checkpoint []byte, change BatchChange, labels BatchCausalPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	var empty BatchTargetCandidate
	// Independently bind the immutable change to this adapter's exact scope.
	normalized, err := normalizeBatch(BatchRequest{Lease: OwnerLease{Key: a.base.scope}, ExpectedRevision: labels.DocumentRevision, Changes: []BatchChange{change}})
	if err != nil || len(normalized) != 1 {
		return empty, nil, ErrPrivateControl
	}
	initial := make([]BatchTargetIdentity, 0, len(labels.Targets))
	for _, target := range labels.Targets {
		initial = append(initial, BatchTargetIdentity{TargetID: target.TargetID, Deleted: target.Deleted})
	}
	if failure, bindingErr := causalTargetReadFailure(normalized[0], labels, initial); bindingErr != nil || failure != nil {
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
	request := protocol.PrepareAtomicCausal{Type: "prepare_atomic_causal", RequestID: id, Scope: a.base.wireScope(), Selected: a.base.wireVersions(), Checkpoint: c, ControlProfile: PrivateCausalPrepareProfile}
	if convertPrepareValue(normalized[0], &request.Change) != nil || convertPrepareValue(labels, &request.Context) != nil {
		return empty, nil, ErrPrivateControl
	}
	request.Context.ReadPolicy = CausalTargetReadPolicy
	raw, err := a.exchange(ctx, request)
	if err != nil {
		return empty, nil, err
	}
	var response protocol.AtomicPreparedCausal
	if json.Unmarshal(raw, &response) != nil || response.Type != "atomic_prepared_causal" || response.RequestID != id || response.Scope != request.Scope || response.Selected != request.Selected || response.InputCheckpointDigest != c.Digest || response.ChangeID != request.Change.ChangeID || response.MembershipHash != request.Change.MembershipHash {
		return empty, nil, a.base.invalid()
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(response.Outcome, &kind) != nil {
		return empty, nil, a.base.invalid()
	}
	if kind.Kind == "failure" {
		var failed protocol.PreparedDirectFailure
		var reason BatchRejection
		if json.Unmarshal(response.Outcome, &failed) != nil || convertPrepareValue(failed.Reason, &reason) != nil {
			return empty, nil, a.base.invalid()
		}
		failure := &PreparedFailure{OperationID: string(failed.OpID), Reason: &reason}
		if !validPreparedFailure(normalized[0], failure) {
			return empty, nil, a.base.invalid()
		}
		return empty, failure, nil
	}
	if kind.Kind != "candidate" {
		return empty, nil, a.base.invalid()
	}
	var candidate protocol.PreparedCandidate
	if json.Unmarshal(response.Outcome, &candidate) != nil {
		return empty, nil, a.base.invalid()
	}
	decoded, err := decodePreparedCheckpoint(candidate.Checkpoint)
	if err != nil {
		return empty, nil, a.base.invalid()
	}
	result := BatchTargetCandidate{Checkpoint: decoded}
	if convertPrepareValue(candidate.Inventory, &result.Inventory) != nil || convertPrepareValue(candidate.Effects, &result.Effects) != nil || !validPrepareInventory(result.Inventory) || len(result.Effects) != len(change.Operations) {
		return empty, nil, a.base.invalid()
	}
	for i, effect := range result.Effects {
		if effect.OperationID != change.Operations[i].ID || effect.Targets == nil || len(effect.Targets) > 4096 {
			return empty, nil, a.base.invalid()
		}
		seen := map[string]bool{}
		for _, target := range effect.Targets {
			if !batchID(target.TargetID) || seen[target.TargetID] || !(target.ContentChanged || target.StructureChanged) || (target.Created && target.Deleted) || ((target.Created || target.Deleted) && !target.StructureChanged) {
				return empty, nil, a.base.invalid()
			}
			seen[target.TargetID] = true
		}
	}
	// Reconstruct effects against the qualified original host inventory before
	// returning any candidate. BatchStore must independently repeat at commit.
	if !consistentPrepareEffects(BatchPrepareContext{Targets: labels.Targets}, result) {
		return empty, nil, a.base.invalid()
	}
	return result, nil, nil
}
