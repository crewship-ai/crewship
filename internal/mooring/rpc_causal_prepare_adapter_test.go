package mooring

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

var causalPrepareVersions = RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}

// Synthetic transport exercises hostile response binding, not real Rust or
// authority. Actual process/SQL acceptance is separately explicitly selected.
func causalPrepareAdapterPeer(t *testing.T, respond func([]byte) []byte) (*CausalRPCPrepareAdapter, SessionKey) {
	t.Helper()
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	r := &Runtime{RPC: &RPC{conn: parent, maxFrame: MaxPrivatePrepareRPCFrame}, selectedVersions: causalPrepareVersions, selectedControlProfile: PrivateCausalPrepareProfile}
	peer := &RPC{conn: child, maxFrame: MaxPrivatePrepareRPCFrame}
	go func() {
		raw, err := peer.Read()
		if err == nil {
			_ = peer.Write(respond(raw))
		}
	}()
	key := SessionKey{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}
	adapter, err := NewCausalRPCPrepareAdapter(r, key, causalPrepareVersions)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, key
}
func TestRPCCausalPrepareRequiresSuccessfulPrivateNegotiation(t *testing.T) {
	key := SessionKey{"workspace", "artifact", "wordcraft"}
	for _, runtime := range []*Runtime{nil, {RPC: &RPC{}}, {RPC: &RPC{}, selectedControlProfile: PrivateCausalPrepareProfile, selectedVersions: RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.1.0"}}} {
		if _, err := NewCausalRPCPrepareAdapter(runtime, key, causalPrepareVersions); err == nil {
			t.Fatal("unselected or wrong runtime accepted")
		}
	}
}
func TestRPCCausalPrepareCheckpointResponseBinding(t *testing.T) {
	for _, mutation := range []string{"valid", "type", "request_id", "scope", "selected", "input_checkpoint_digest", "inventory-duplicate", "duplicate-key"} {
		t.Run(mutation, func(t *testing.T) {
			adapter, _ := causalPrepareAdapterPeer(t, func(raw []byte) []byte {
				var request protocol.ValidateCheckpointCausal
				if json.Unmarshal(raw, &request) != nil {
					panic("test request")
				}
				response := map[string]any{"type": "checkpoint_validated_causal", "request_id": request.RequestID, "scope": request.Scope, "selected": request.Selected, "input_checkpoint_digest": request.Checkpoint.Digest, "inventory": []BatchTargetIdentity{{TargetID: "paragraph"}}, "control_profile": PrivateCausalPrepareProfile}
				switch mutation {
				case "type":
					response[mutation] = "atomic_prepared_causal"
				case "request_id":
					response[mutation] = "foreign"
				case "scope":
					response[mutation] = protocol.Scope{WorkspaceID: "workspace", ArtifactID: "foreign", ApplicationID: "wordcraft"}
				case "selected":
					response[mutation] = protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "9.9.9", EngineVersion: "0.10.0"}
				case "input_checkpoint_digest":
					response[mutation] = canonicalDigest([]byte("foreign"))
				case "inventory-duplicate":
					response["inventory"] = []BatchTargetIdentity{{TargetID: "paragraph"}, {TargetID: "paragraph"}}
				}
				out, _ := json.Marshal(response)
				if mutation == "duplicate-key" {
					out = append([]byte(`{"request_id":"foreign",`), out[1:]...)
				}
				return out
			})
			result, err := adapter.ValidateTargets(t.Context(), []byte("opaque"))
			if mutation == "valid" {
				if err != nil || len(result) != 1 {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("unbound response accepted", mutation)
			}
		})
	}
}
func TestRPCCausalPrepareCandidateAndFailureBinding(t *testing.T) {
	for _, mutation := range []string{"valid", "failure", "foreign-member", "unknown-outcome", "change_id", "membership_hash", "checkpoint-digest", "effect-member", "duplicate-effect", "inventory-effects", "deleted-reuse"} {
		t.Run(mutation, func(t *testing.T) {
			adapter, key := causalPrepareAdapterPeer(t, func(raw []byte) []byte {
				var request protocol.PrepareAtomicCausal
				if json.Unmarshal(raw, &request) != nil {
					panic("test request")
				}
				checkpoint, _ := encodePreparedCheckpoint([]byte("candidate"))
				effects := []BatchOperationEffects{{OperationID: "op", Targets: []BatchTargetEffect{{TargetID: "paragraph", ContentChanged: true}}}}
				inventory := []BatchTargetIdentity{{TargetID: "paragraph"}}
				if mutation == "effect-member" {
					effects[0].OperationID = "foreign"
				}
				if mutation == "duplicate-effect" {
					effects[0].Targets = append(effects[0].Targets, effects[0].Targets[0])
				}
				if mutation == "inventory-effects" {
					inventory = append(inventory, BatchTargetIdentity{TargetID: "forged"})
				}
				if mutation == "deleted-reuse" {
					effects[0].Targets[0].Created = true
					effects[0].Targets[0].StructureChanged = true
				}
				if mutation == "checkpoint-digest" {
					checkpoint.Digest = protocol.Digest(canonicalDigest([]byte("foreign")))
				}
				outcome := map[string]any{"kind": "candidate", "checkpoint": checkpoint, "effects": effects, "inventory": inventory}
				if mutation == "failure" || mutation == "foreign-member" || mutation == "unknown-outcome" {
					id, code := "op", "invalid_request"
					if mutation == "foreign-member" {
						id = "foreign"
					}
					if mutation == "unknown-outcome" {
						code = "unknown_outcome"
					}
					outcome = map[string]any{"kind": "failure", "op_id": id, "reason": BatchRejection{Code: code, Message: "direct cause", Retry: "new_operation_after_refresh"}}
				}
				response := map[string]any{"type": "atomic_prepared_causal", "request_id": request.RequestID, "scope": request.Scope, "selected": request.Selected, "input_checkpoint_digest": request.Checkpoint.Digest, "change_id": request.Change.ChangeID, "membership_hash": request.Change.MembershipHash, "outcome": outcome, "control_profile": PrivateCausalPrepareProfile}
				if mutation == "change_id" {
					response[mutation] = "foreign"
				}
				if mutation == "membership_hash" {
					response[mutation] = canonicalDigest([]byte("foreign"))
				}
				out, _ := json.Marshal(response)
				return out
			})
			op := testBatchOp("op")
			op.Reads = []BatchReadPrecondition{{TargetID: "paragraph", Revision: "0"}}
			change := testBatchChange(t, key, "change", "", op)
			labels := BatchCausalPrepareContext{QualifiedReads: []BatchQualifiedRead{{OperationID: "op", ReadIndex: "0", TargetID: "paragraph", ObservedRevision: "0", Entry: &BatchCausalEntry{Revision: "0", StructureRevision: "0"}, Covered: true}}, DocumentRevision: "0", Targets: []BatchTargetVersion{{TargetID: "paragraph", Revision: "0", StructureRevision: "0"}}, OperationRevisions: []BatchOperationRevision{{OperationID: "op", Revision: "1"}}}
			candidate, failure, err := adapter.PrepareCausalTargets(context.Background(), []byte("opaque"), change, labels)
			switch mutation {
			case "valid":
				if err != nil || failure != nil || string(candidate.Checkpoint) != "candidate" {
					t.Fatal(candidate, failure, err)
				}
			case "failure":
				if err != nil || failure == nil || failure.OperationID != "op" || candidate.Checkpoint != nil {
					t.Fatal(candidate, failure, err)
				}
			default:
				if err == nil || failure != nil || candidate.Checkpoint != nil {
					t.Fatal("unbound candidate/failure accepted", mutation, candidate, failure, err)
				}
			}
		})
	}
}

func TestRPCCausalPrepareCannotCrossNegotiatedCapability(t *testing.T) {
	key := SessionKey{"workspace", "artifact", "wordcraft"}
	strict := &Runtime{RPC: &RPC{}, selectedVersions: causalPrepareVersions, selectedControlProfile: PrivatePrepareProfile}
	if _, err := NewCausalRPCPrepareAdapter(strict, key, causalPrepareVersions); err == nil {
		t.Fatal("strict Runtime became causal")
	}
	causal := &Runtime{RPC: &RPC{}, selectedVersions: causalPrepareVersions, selectedControlProfile: PrivateCausalPrepareProfile}
	if _, err := NewRPCPrepareAdapter(causal, key, causalPrepareVersions); err == nil {
		t.Fatal("causal Runtime became strict")
	}
	a, err := NewCausalRPCPrepareAdapter(causal, key, causalPrepareVersions)
	if err != nil {
		t.Fatal(err)
	}
	if candidate, failure, err := a.PrepareTargets(t.Context(), nil, BatchChange{}, BatchPrepareContext{}); err == nil || failure != nil || candidate.Checkpoint != nil {
		t.Fatal("causal adapter fell back to strict")
	}
}
