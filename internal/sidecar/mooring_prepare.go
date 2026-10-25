package sidecar

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Untrusted client DATA, not a checkpoint/admission proof. The host ignores
// these labels as authority and independently checks the original live task,
// every read precondition and full atomic membership before application.
type mooringToolView struct {
	Scope        protocol.Scope      `json:"scope"`
	SessionID    protocol.Identifier `json:"session_id"`
	Versions     protocol.Versions   `json:"versions"`
	TaskID       string              `json:"task_id"`
	AllowPartial bool                `json:"allow_partial,omitempty"`
	Participant  string              `json:"participant_ref"`
	Revision     string              `json:"revision"`
	Native       json.RawMessage     `json:"native,omitempty"`
	BlockIDs     []string            `json:"block_ids,omitempty"`
	Targets      json.RawMessage     `json:"targets,omitempty"`
}

func prepareMooringToolPacket(raw []byte) (json.RawMessage, error) {
	var args struct {
		View     mooringToolView `json:"view"`
		Mutation string          `json:"mutation_id"`
		Changes  []struct {
			Operations []struct {
				Payload      json.RawMessage                 `json:"payload"`
				Reads        []mooring.BatchReadPrecondition `json:"read_preconditions"`
				Dependencies []string                        `json:"dependencies"`
			} `json:"operations"`
		} `json:"changes"`
	}
	invalid := errors.New("bounded original view, mutation ID and typed atomic operations required")
	if len(raw) > 256<<10 || decodeMooringMCP(raw, &args) != nil || !mooring.ValidWireIdentifier(args.Mutation) || len(args.Mutation) > 64 || args.View.Scope.ApplicationID != "wordcraft" || !mooring.ValidWireIdentifier(string(args.View.Scope.WorkspaceID)) || !mooring.ValidWireIdentifier(string(args.View.Scope.ArtifactID)) || !mooring.ValidWireIdentifier(string(args.View.SessionID)) || !mooring.ValidWireIdentifier(args.View.TaskID) || !mooring.ValidWireIdentifier(args.View.Participant) || len(args.Changes) == 0 || len(args.Changes) > mooring.MaxAtomicOperations {
		return nil, invalid
	}
	revision, err := strconv.ParseUint(args.View.Revision, 10, 64)
	if err != nil || strconv.FormatUint(revision, 10) != args.View.Revision {
		return nil, invalid
	}
	changes := make([]mooring.BatchChange, len(args.Changes))
	count := 0
	for i, input := range args.Changes {
		if len(input.Operations) == 0 || len(input.Operations) > mooring.MaxAtomicOperations-count {
			return nil, invalid
		}
		count += len(input.Operations)
		change := mooring.BatchChange{ID: args.Mutation + "-change-" + strconv.Itoa(i), TaskID: args.View.TaskID, Operations: make([]mooring.BatchOperation, len(input.Operations))}
		// Exactly the existing protocol preimage, omitting only fingerprints.
		operations := make([]map[string]any, len(input.Operations))
		for j, command := range input.Operations {
			var shape struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(command.Payload, &shape) != nil || (shape.Kind != "text.insert" && shape.Kind != "text.delete" && shape.Kind != "text.format") {
				return nil, invalid
			}
			reads, deps := command.Reads, command.Dependencies
			if reads == nil || deps == nil {
				return nil, invalid
			}
			id := args.Mutation + "-op-" + strconv.Itoa(i) + "-" + strconv.Itoa(j)
			change.Operations[j] = mooring.BatchOperation{ID: id, Participant: args.View.Participant, BaseRevision: args.View.Revision, Dependencies: deps, Reads: reads, Payload: command.Payload}
			operations[j] = map[string]any{"op_id": id, "participant_ref": args.View.Participant, "base_revision": args.View.Revision, "dependencies": deps, "read_preconditions": reads, "payload": command.Payload}
		}
		preimage, err := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": args.View.Scope, "change_id": change.ID, "task_id": change.TaskID, "operations_without_fingerprints": operations})
		if err != nil {
			return nil, invalid
		}
		fingerprints, err := mooring.FingerprintAtomicJSON(preimage)
		if err != nil {
			return nil, invalid
		}
		change.MembershipHash = fingerprints.MembershipHash
		for j := range change.Operations {
			change.Operations[j].Fingerprint = fingerprints.Operations[change.Operations[j].ID]
		}
		changes[i] = change
	}
	key := mooring.SessionKey{WorkspaceID: string(args.View.Scope.WorkspaceID), ArtifactID: string(args.View.Scope.ArtifactID), ApplicationID: "wordcraft"}
	changes, err = mooring.NormalizeSessionIngress(key, changes)
	if err != nil {
		return nil, invalid
	}
	encoded, err := json.Marshal(changes)
	if err != nil {
		return nil, invalid
	}
	var atomic []protocol.AtomicChange
	if json.Unmarshal(encoded, &atomic) != nil {
		return nil, invalid
	}
	task := protocol.Identifier(args.View.TaskID)
	packet, err := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: protocol.Identifier(args.Mutation + "-submit"), Scope: args.View.Scope, SessionID: args.View.SessionID, Versions: args.View.Versions, TaskID: &task, Changes: atomic})
	if err != nil || len(packet) > 256<<10 {
		return nil, invalid
	}
	return packet, nil
}

// Both branches produce the same existing immutable public submission shape.
// The explicit view branch performs no host read and issues no authority.
var mooringSubmitToolSchema = json.RawMessage(`{"oneOf":[{"type":"object","properties":{"submission":{"type":"object"}},"required":["submission"],"additionalProperties":false},{"type":"object","properties":{"view":{"type":"object"},"mutation_id":{"type":"string","maxLength":64},"changes":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"object","properties":{"operations":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"object","properties":{"payload":{"type":"object"},"read_preconditions":{"type":"array"},"dependencies":{"type":"array","items":{"type":"string"}}},"required":["payload","read_preconditions","dependencies"],"additionalProperties":false}}},"required":["operations"],"additionalProperties":false}}},"required":["view","mutation_id","changes"],"additionalProperties":false}]}`)
