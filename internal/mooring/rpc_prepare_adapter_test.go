package mooring

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

var prepareVersions = RuntimeVersions{Protocol: "0.4.0", Model: "0.1.0", Engine: "0.10.0"}

// Synthetic transport exercises hostile response binding, not real Rust or
// authority. Actual process/SQL acceptance is separately explicitly selected.
func prepareAdapterPeer(t *testing.T, respond func([]byte) []byte) (*RPCPrepareAdapter, SessionKey) {
	t.Helper()
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	r := &Runtime{RPC: &RPC{conn: parent, maxFrame: MaxPrivatePrepareRPCFrame}, selectedVersions: prepareVersions, selectedControlProfile: PrivatePrepareProfile}
	peer := &RPC{conn: child, maxFrame: MaxPrivatePrepareRPCFrame}
	go func() {
		raw, err := peer.Read()
		if err == nil {
			_ = peer.Write(respond(raw))
		}
	}()
	key := SessionKey{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}
	adapter, err := NewRPCPrepareAdapter(r, key, prepareVersions)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, key
}
func TestRPCPrepareRequiresSuccessfulPrivateNegotiation(t *testing.T) {
	key := SessionKey{"workspace", "artifact", "wordcraft"}
	for _, runtime := range []*Runtime{nil, {RPC: &RPC{}}, {RPC: &RPC{}, selectedControlProfile: PrivatePrepareProfile, selectedVersions: RuntimeVersions{Protocol: "0.4.0", Model: "0.1.0", Engine: "0.1.0"}}} {
		if _, err := NewRPCPrepareAdapter(runtime, key, prepareVersions); err == nil {
			t.Fatal("unselected or wrong runtime accepted")
		}
	}
}
func TestRPCPrepareCheckpointResponseBinding(t *testing.T) {
	for _, mutation := range []string{"valid", "type", "request_id", "scope", "selected", "input_checkpoint_digest", "inventory-duplicate", "duplicate-key"} {
		t.Run(mutation, func(t *testing.T) {
			adapter, _ := prepareAdapterPeer(t, func(raw []byte) []byte {
				var request protocol.ValidateCheckpoint
				if json.Unmarshal(raw, &request) != nil {
					panic("test request")
				}
				response := map[string]any{"type": "checkpoint_validated", "request_id": request.RequestID, "scope": request.Scope, "selected": request.Selected, "input_checkpoint_digest": request.Checkpoint.Digest, "inventory": []BatchTargetIdentity{{TargetID: "paragraph"}}, "control_profile": PrivatePrepareProfile}
				switch mutation {
				case "type":
					response[mutation] = "atomic_prepared"
				case "request_id":
					response[mutation] = "foreign"
				case "scope":
					response[mutation] = protocol.Scope{WorkspaceID: "workspace", ArtifactID: "foreign", ApplicationID: "wordcraft"}
				case "selected":
					response[mutation] = protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "0.2.0", EngineVersion: "0.10.0"}
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
func TestRPCPrepareCandidateAndFailureBinding(t *testing.T) {
	for _, mutation := range []string{"valid", "failure", "foreign-member", "unknown-outcome", "change_id", "membership_hash", "checkpoint-digest", "effect-member", "duplicate-effect", "inventory-effects", "deleted-reuse"} {
		t.Run(mutation, func(t *testing.T) {
			adapter, key := prepareAdapterPeer(t, func(raw []byte) []byte {
				var request protocol.PrepareAtomic
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
				response := map[string]any{"type": "atomic_prepared", "request_id": request.RequestID, "scope": request.Scope, "selected": request.Selected, "input_checkpoint_digest": request.Checkpoint.Digest, "change_id": request.Change.ChangeID, "membership_hash": request.Change.MembershipHash, "outcome": outcome, "control_profile": PrivatePrepareProfile}
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
			labels := BatchPrepareContext{ReadPolicy: "agent_confirmed_current", DocumentRevision: "0", Targets: []BatchTargetVersion{{TargetID: "paragraph", Revision: "0", StructureRevision: "0"}}, OperationRevisions: []BatchOperationRevision{{OperationID: "op", Revision: "1"}}}
			candidate, failure, err := adapter.PrepareTargets(context.Background(), []byte("opaque"), change, labels)
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
