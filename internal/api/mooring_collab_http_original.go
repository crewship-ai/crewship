package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Original native witness DATA. Parsing proves neither the application delta
// nor authorship; only the actual connection's sealed operation and qualified
// session prepare path can establish those boundaries.
type collaborationHumanOriginalPayload struct {
	Kind                          string   `json:"kind"`
	OperationID                   string   `json:"operation_id"`
	Origin                        string   `json:"origin"`
	Update                        string   `json:"update"`
	AuthoredFrontier              string   `json:"authored_frontier"`
	Targets                       []string `json:"targets"`
	AuthoredCheckpointStateDigest string   `json:"authored_checkpoint_state_digest"`
}

func (c *collaborationHTTPConnection) parseOriginalHumanChange(change mooring.BatchChange) (collaborationHumanOriginalPayload, error) {
	var empty collaborationHumanOriginalPayload
	if c == nil || change.TaskID != "" || len(change.Operations) != 1 {
		return empty, errCollaborationHTTP
	}
	op := change.Operations[0]
	if op.Participant != c.admission.participant || len(op.Payload) == 0 || len(op.Payload) > collaborationHTTPFrameLimit {
		return empty, errCollaborationHTTP
	}
	// Canonicalization rejects duplicate keys before typed Go decoding. The
	// exact key set also rejects case variants that encoding/json would accept.
	raw, err := mooring.CanonicalJSON(op.Payload)
	if err != nil {
		return empty, errCollaborationHTTP
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
		return empty, errCollaborationHTTP
	}
	for _, key := range []string{"kind", "operation_id", "origin", "update", "authored_frontier", "targets", "authored_checkpoint_state_digest"} {
		value, ok := fields[key]
		if !ok || len(value) == 0 || key == "targets" && value[0] != '[' || key != "targets" && value[0] != '"' {
			return empty, errCollaborationHTTP
		}
	}
	var payload collaborationHumanOriginalPayload
	if json.Unmarshal(raw, &payload) != nil || payload.Kind != "humancrdt.update" || payload.OperationID != op.ID || payload.Origin != c.origin || !collaborationHumanOrigin(payload.Origin) || payload.Targets == nil || len(payload.Targets) > 4096 || !collaborationStateDigest(payload.AuthoredCheckpointStateDigest) {
		return empty, errCollaborationHTTP
	}
	if !collaborationOriginalBase64(payload.Update) || !collaborationOriginalBase64(payload.AuthoredFrontier) {
		return empty, errCollaborationHTTP
	}
	seen := make(map[string]bool, len(payload.Targets))
	for _, target := range payload.Targets {
		if !mooring.ValidWireIdentifier(target) || seen[target] {
			return empty, errCollaborationHTTP
		}
		seen[target] = true
	}
	return payload, nil
}

func collaborationHumanOrigin(origin string) bool {
	n, err := strconv.ParseUint(origin, 10, 53)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == origin
}

func collaborationStateDigest(digest string) bool {
	if len(digest) != 71 || digest[:7] != "sha256:" {
		return false
	}
	for _, c := range digest[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func collaborationOriginalBase64(encoded string) bool {
	if len(encoded) > collaborationHTTPFrameLimit {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	return err == nil && base64.StdEncoding.EncodeToString(raw) == encoded
}

func (c *collaborationHTTPConnection) recordObservedCheckpoint(checkpoint protocol.Checkpoint) error {
	if c == nil || c.document == nil || c.document.originals == nil || checkpoint.Scope != c.document.wireScope() || checkpoint.Versions != c.document.wireVersions() || len(checkpoint.State) > collaborationHTTPFrameLimit {
		return errCollaborationHTTP
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(checkpoint.State)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != checkpoint.State {
		return errCollaborationHTTP
	}
	hash := sha256.Sum256(raw)
	return c.document.originals.observe(c, collaborationObservedCut{revision: string(checkpoint.Revision), stateDigest: "sha256:" + hex.EncodeToString(hash[:]), fullDigest: string(checkpoint.Digest)})
}
