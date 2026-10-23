package mooring

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

func batchID(s string) bool { return ownerIDValid(s) && utf8.ValidString(s) }
func batchCounter(s string) (uint64, error) {
	if s == "" || len(s) > 20 || (len(s) > 1 && s[0] == '0') {
		return 0, ErrInvalid
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	return n, nil
}
func (s *BatchStore) ready() error {
	if !s.config.Enabled {
		return ErrBatchDisabled
	}
	l := s.config.Limits
	if s.db == nil || s.config.Owners == nil || s.config.Owners.db != s.db || s.config.Authority == nil || s.config.Adapter == nil || l.LedgerOperations < 1 || l.Changes < 1 || l.Tasks < 1 || l.AuditEvents < 1 || l.Checkpoints < 1 || l.CheckpointBytes < 1 || l.CheckpointBytes > 4<<20 || l.TotalCheckpointBytes < int64(l.CheckpointBytes) {
		return ErrInvalid
	}
	return nil
}

const maxBatchImmutableBytes = 262144

type batchScope struct {
	Application string `json:"application_id"`
	Artifact    string `json:"artifact_id"`
	Workspace   string `json:"workspace_id"`
}
type batchImmutableOperation struct {
	ID           string                  `json:"op_id"`
	Participant  string                  `json:"participant_ref"`
	BaseRevision string                  `json:"base_revision"`
	Dependencies []string                `json:"dependencies"`
	Reads        []BatchReadPrecondition `json:"read_preconditions"`
	Payload      json.RawMessage         `json:"payload"`
}
type batchPreimage struct {
	Domain     string                    `json:"domain"`
	Scope      batchScope                `json:"scope"`
	ID         string                    `json:"change_id"`
	TaskID     string                    `json:"task_id,omitempty"`
	Operations []batchImmutableOperation `json:"operations_without_fingerprints"`
}

func normalizeBatch(r BatchRequest) ([]BatchChange, error) {
	if _, err := batchCounter(r.ExpectedRevision); err != nil {
		return nil, err
	}
	k := r.Lease.Key
	if !batchID(k.ApplicationID) || !batchID(k.ArtifactID) || !batchID(k.WorkspaceID) || len(r.Changes) == 0 || len(r.Changes) > MaxAtomicOperations {
		return nil, ErrInvalid
	}
	count, bytes := 0, 0
	for _, c := range r.Changes {
		count += len(c.Operations)
		for _, op := range c.Operations {
			bytes += len(op.Payload)
		}
	}
	if count == 0 || count > MaxAtomicOperations || bytes > maxBatchImmutableBytes {
		return nil, ErrInvalid
	}
	ids, changes := map[string]bool{}, map[string]bool{}
	normalized := make([]BatchChange, 0, len(r.Changes))
	bytes = 0
	for _, c := range r.Changes {
		if !batchID(c.ID) || changes[c.ID] || len(c.Operations) == 0 || (c.TaskID != "" && !batchID(c.TaskID)) {
			return nil, ErrInvalid
		}
		changes[c.ID] = true
		p := batchPreimage{Domain: "mooring.atomic.v0.4", Scope: batchScope{k.ApplicationID, k.ArtifactID, k.WorkspaceID}, ID: c.ID, TaskID: c.TaskID, Operations: make([]batchImmutableOperation, 0, len(c.Operations))}
		for _, op := range c.Operations {
			if !batchID(op.ID) || ids[op.ID] || !batchID(op.Participant) || op.Dependencies == nil || op.Reads == nil || len(op.Dependencies) > 256 || len(op.Reads) > 256 {
				return nil, ErrInvalid
			}
			ids[op.ID] = true
			if _, err := batchCounter(op.BaseRevision); err != nil {
				return nil, err
			}
			deps := map[string]bool{}
			for _, d := range op.Dependencies {
				if !batchID(d) || deps[d] {
					return nil, ErrInvalid
				}
				deps[d] = true
			}
			for _, read := range op.Reads {
				if !batchID(read.TargetID) {
					return nil, ErrInvalid
				}
				if _, e := batchCounter(read.Revision); e != nil {
					return nil, e
				}
				if read.StructureRevision != nil {
					if _, e := batchCounter(*read.StructureRevision); e != nil {
						return nil, e
					}
				}
			}
			payload, err := CanonicalJSON(op.Payload)
			if err != nil {
				return nil, err
			}
			if len(payload) == 0 || payload[0] != '{' {
				return nil, ErrInvalid
			}
			p.Operations = append(p.Operations, batchImmutableOperation{op.ID, op.Participant, op.BaseRevision, op.Dependencies, op.Reads, payload})
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		bytes += len(raw)
		if bytes > maxBatchImmutableBytes {
			return nil, ErrInvalid
		}
		hashes, err := FingerprintAtomicJSON(raw)
		if err != nil {
			return nil, err
		}
		if hashes.MembershipHash != c.MembershipHash {
			return nil, ErrOperationConflict
		}
		var canonical batchPreimage
		if err = json.Unmarshal(hashes.CanonicalInput, &canonical); err != nil {
			return nil, err
		}
		n := BatchChange{ID: c.ID, TaskID: c.TaskID, MembershipHash: hashes.MembershipHash, Operations: make([]BatchOperation, 0, len(c.Operations))}
		for i, op := range canonical.Operations {
			if hashes.Operations[op.ID] != c.Operations[i].Fingerprint {
				return nil, ErrOperationConflict
			}
			n.Operations = append(n.Operations, BatchOperation{op.ID, hashes.Operations[op.ID], op.Participant, op.BaseRevision, op.Dependencies, op.Reads, op.Payload})
		}
		normalized = append(normalized, n)
	}
	return normalized, nil
}

var batchTerminalCodes = map[string]bool{"precondition_failed": true, "op_id_conflict": true, "target_deleted": true, "quota_exceeded": true, "permission_revoked": true, "forbidden": true, "stale_owner": true, "lease_expired": true, "version_mismatch": true, "invalid_request": true, "atomic_change_too_large": true, "capacity_exhausted": true, "unknown_dependency": true, "dependency_cycle": true, "task_cancelled": true}

func validBatchRejection(r *BatchRejection) bool {
	if r == nil || len(r.Message) > 1000 || !utf8.ValidString(r.Message) {
		return false
	}
	if r.Retry != "do_not_retry" && r.Retry != "query_status" && r.Retry != "reconnect_then_query_status" && r.Retry != "new_operation_after_refresh" {
		return false
	}
	if r.Code == "dependency_rejected" {
		return batchID(r.RootOperationID) && r.RootCause != nil && r.RootCause.OperationID == r.RootOperationID && batchTerminalCodes[r.RootCause.Code] && len(r.RootCause.Message) <= 1000 && utf8.ValidString(r.RootCause.Message)
	}
	return batchTerminalCodes[r.Code] && r.RootCause == nil && r.RootOperationID == ""
}
func rejectedBatchChange(c BatchChange, reason *BatchRejection, rootID string) BatchChangeResult {
	out := BatchChangeResult{ID: c.ID, Status: "rejected", Operations: make([]BatchOperationResult, 0, len(c.Operations))}
	for _, op := range c.Operations {
		projected := reason
		if reason.Code != "dependency_rejected" && op.ID != rootID {
			projected = dependencyReason(BatchOperationResult{ID: rootID, Status: "rejected", Reason: reason})
		}
		copy := *projected
		if copy.RootCause != nil {
			root := *copy.RootCause
			copy.RootCause = &root
		}
		out.Operations = append(out.Operations, BatchOperationResult{ID: op.ID, Status: "rejected", Reason: &copy})
	}
	return out
}
func validPreparedFailure(c BatchChange, p *PreparedFailure) bool {
	if p == nil || !validBatchRejection(p.Reason) || p.Reason.Code == "dependency_rejected" {
		return false
	}
	for _, op := range c.Operations {
		if op.ID == p.OperationID {
			return true
		}
	}
	return false
}

func batchReason(code string) *BatchRejection {
	return &BatchRejection{Code: code, Message: strings.ReplaceAll(code, "_", " "), Retry: "new_operation_after_refresh"}
}
func batchRange(rev uint64, n int) (string, string, error) {
	if n < 1 || uint64(n) > math.MaxUint64-rev {
		return "", "", ErrInvalid
	}
	return strconv.FormatUint(rev+1, 10), strconv.FormatUint(rev+uint64(n), 10), nil
}

func validStoredBatchOperation(op BatchOperationResult) bool {
	if !batchID(op.ID) {
		return false
	}
	if op.Status == "saved" {
		revision, err := batchCounter(op.Revision)
		return err == nil && revision > 0 && op.Reason == nil && op.Issuer == "crewship" && op.Durability == "node"
	}
	return op.Status == "rejected" && op.Revision == "" && op.Issuer == "" && op.Durability == "" && validBatchRejection(op.Reason)
}
func validStoredBatchChange(c BatchChange, r BatchChangeResult) bool {
	if r.ID != c.ID || len(r.Operations) != len(c.Operations) {
		return false
	}
	var first, last uint64
	switch r.Status {
	case "saved":
		var err error
		first, err = batchCounter(r.FirstRevision)
		if err != nil || first == 0 {
			return false
		}
		last, err = batchCounter(r.LastRevision)
		if err != nil || last < first || last-first != uint64(len(c.Operations)-1) {
			return false
		}
	case "rejected":
		if r.FirstRevision != "" || r.LastRevision != "" {
			return false
		}
	default:
		return false
	}
	for i, op := range r.Operations {
		if op.ID != c.Operations[i].ID || op.Status != r.Status || !validStoredBatchOperation(op) {
			return false
		}
		if r.Status == "saved" && op.Revision != strconv.FormatUint(first+uint64(i), 10) {
			return false
		}
	}
	return true
}
