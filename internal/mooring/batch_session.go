package mooring

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// These are fixed initial session-profile caps, not a change to generic PRD
// checkpoint limits. No runtime/profile is admitted by this unused primitive.
const sessionOutputBytes = 2 * 1024 * 1024

// SessionBasis names a retained canonical checkpoint at an atomic boundary.
// It is transport context, never a rewrite of an operation's immutable reads.
type SessionBasis struct{ Revision, Digest string }
type SessionBatchRequest struct {
	Lease   OwnerLease
	Basis   SessionBasis
	Changes []BatchChange
}
type SessionBatchPurpose uint8

const (
	SessionProcessSealedOutcome SessionBatchPurpose = iota + 1
	SessionCurrentWrite
	SessionCurrentTask
)

// Purpose is host processing of ORIGINAL sealed ingress, not external status
// permission. The callback MUST match complete operation/membership/source to
// its bounded host-owned submission registry before any outcome lookup. The
// authenticated child and participant_ref alone are not authorship authority.
type SessionBatchAccess struct {
	Purpose        SessionBatchPurpose
	Lease          OwnerLease
	Access         BatchAccess
	ChangeID       string
	MembershipHash string
}
type SessionBatchAuthority func(context.Context, *sql.Tx, SessionBatchAccess) error

// SessionPendingOutput is an uncommitted candidate. The pure callback must bind
// and serialize the exact receipt/checkpoint/inventory within the negotiated
// output budget. It MUST NOT publish, install state or issue Saved. The returned
// bytes are released only after COMMIT; uncertain errors roll everything back.
type SessionPendingOutput struct {
	Lease      OwnerLease
	Result     BatchResult
	Checkpoint []byte
}
type SessionOutputPreparer func(context.Context, *sql.Tx, SessionPendingOutput) ([]byte, error)
type SessionBatchResult struct {
	Result        BatchResult
	EncodedOutput []byte
}
type SessionCommitter struct {
	store     *BatchStore
	scope     SessionKey
	authority SessionBatchAuthority
	output    SessionOutputPreparer
}

func NewSessionCommitter(store *BatchStore, scope SessionKey, authority SessionBatchAuthority, output SessionOutputPreparer) (*SessionCommitter, error) {
	if store == nil || store.ready() != nil || !store.targetsEnabled() || store.config.Targets.ReadPolicy != CausalTargetReadPolicy || !batchID(scope.WorkspaceID) || !batchID(scope.ArtifactID) || !batchID(scope.ApplicationID) || authority == nil || output == nil {
		return nil, ErrInvalid
	}
	// Copy immutable configuration; never replace the public store's current-reader
	// authority. Only the commit-only clone below can use processing authority.
	return &SessionCommitter{store: store, scope: scope, authority: authority, output: output}, nil
}

type sessionMembership struct{ changeID, membershipHash string }

type sessionCommitMode struct {
	members map[string]sessionMembership
	basis   SessionBasis
	output  SessionOutputPreparer
	encoded []byte
}

func (s *SessionCommitter) Commit(ctx context.Context, request SessionBatchRequest) (SessionBatchResult, error) {
	if s == nil || s.store == nil || request.Lease.Key != s.scope || !validSessionBasis(request.Basis) {
		return SessionBatchResult{}, ErrInvalid
	}
	mode := &sessionCommitMode{basis: request.Basis, output: s.output}
	isolated := *s.store
	isolated.config.Authority = func(ctx context.Context, tx *sql.Tx, access BatchAccess) error {
		purpose := SessionProcessSealedOutcome
		switch access.Intent {
		case "outcome":
			if access.Operation == nil {
				return ErrInvalid
			}
		case "write":
			purpose = SessionCurrentWrite
		case "task":
			purpose = SessionCurrentTask
		default:
			return ErrInvalid
		}
		var member sessionMembership
		if access.Operation != nil {
			var ok bool
			member, ok = mode.members[access.Operation.ID]
			if !ok {
				return ErrInvalid
			}
		}
		return s.authority(ctx, tx, SessionBatchAccess{Purpose: purpose, Lease: request.Lease, Access: access, ChangeID: member.changeID, MembershipHash: member.membershipHash})
	}
	// The placeholder is used only for pure normalization. The actual transport
	// expected head is assigned inside the writer AFTER retained-basis validation.
	result, err := isolated.commit(ctx, BatchRequest{Lease: request.Lease, ExpectedRevision: "0", Changes: request.Changes}, mode)
	if err != nil {
		return SessionBatchResult{}, err
	}
	return SessionBatchResult{Result: result, EncodedOutput: mode.encoded}, nil
}
func validSessionBasis(b SessionBasis) bool {
	if _, err := batchCounter(b.Revision); err != nil || len(b.Digest) != len("sha256:")+64 || !strings.HasPrefix(b.Digest, "sha256:") {
		return false
	}
	for _, c := range b.Digest[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// SQL length/decoded metadata is checked BEFORE bringing checkpoint TEXT into
// Go or allocating decoded bytes. The writer prevents mutation between reads.
func sessionCheckpoint(ctx context.Context, tx *sql.Tx, key SessionKey, revision string, current bool) ([]byte, error) {
	var local bool
	var encodedBytes, decodedBytes int64
	query := `SELECT workspace_id=?,length(CAST(checkpoint_base64 AS BLOB)),decoded_bytes FROM mooring_collab_checkpoints WHERE page_id=? AND revision=?`
	if err := tx.QueryRowContext(ctx, query, key.WorkspaceID, key.ArtifactID, revision).Scan(&local, &encodedBytes, &decodedBytes); err != nil {
		return nil, err
	}
	if !local || decodedBytes < 0 || decodedBytes > maxPreparedCheckpoint || encodedBytes != int64(base64.StdEncoding.EncodedLen(int(decodedBytes))) {
		return nil, ErrInvalid
	}
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT checkpoint_base64 FROM mooring_collab_checkpoints WHERE page_id=? AND revision=?`, key.ArtifactID, revision).Scan(&encoded); err != nil {
		return nil, err
	}
	if int64(len(encoded)) != encodedBytes {
		return nil, ErrInvalid
	}
	padding := (3 - int(decodedBytes)%3) % 3
	for i := 0; i < len(encoded); i++ {
		if i >= len(encoded)-padding {
			if encoded[i] != '=' {
				return nil, ErrInvalid
			}
		} else if base64Sextet(encoded[i]) < 0 {
			return nil, ErrInvalid
		}
	}
	if padding == 1 && base64Sextet(encoded[len(encoded)-2])&3 != 0 || padding == 2 && base64Sextet(encoded[len(encoded)-3])&15 != 0 {
		return nil, ErrInvalid
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || int64(len(raw)) != decodedBytes {
		return nil, ErrInvalid
	}
	if current {
		// Compare within SQL; never scan a potentially corrupt unbounded document
		// payload. Both row scope and its exact current checkpoint must agree.
		var matches bool
		if err := tx.QueryRowContext(ctx, `SELECT workspace_id=? AND application_id=? AND revision=? AND checkpoint_base64=? FROM mooring_collab_documents WHERE page_id=?`, key.WorkspaceID, key.ApplicationID, revision, encoded, key.ArtifactID).Scan(&matches); err != nil {
			return nil, err
		}
		if !matches {
			return nil, ErrInvalid
		}
	}
	return raw, nil
}
func sessionScopeCheckpoint(ctx context.Context, tx *sql.Tx, key SessionKey, basis SessionBasis) (string, []byte, error) {
	if err := ownerScopeExists(ctx, tx, key); err != nil {
		return "", nil, err
	}
	var local bool
	var head string
	// A corrupt revision cannot force an unbounded TEXT allocation either.
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id=? AND application_id=?,CASE WHEN length(CAST(revision AS BLOB))<=20 THEN revision ELSE '' END FROM mooring_collab_documents WHERE page_id=?`, key.WorkspaceID, key.ApplicationID, key.ArtifactID).Scan(&local, &head); err != nil {
		return "", nil, err
	}
	headNumber, err := batchCounter(head)
	if err != nil || !local {
		return "", nil, ErrInvalid
	}
	basisNumber, err := batchCounter(basis.Revision)
	if err != nil || basisNumber > headNumber {
		return "", nil, ErrInvalid
	}
	retained, err := sessionCheckpoint(ctx, tx, key, basis.Revision, false)
	if err != nil || canonicalDigest(retained) != basis.Digest {
		return "", nil, ErrInvalid
	}
	if basis.Revision == head {
		current, err := sessionCheckpoint(ctx, tx, key, head, true)
		return head, current, err
	}
	current, err := sessionCheckpoint(ctx, tx, key, head, true)
	return head, current, err
}
func (m *sessionCommitMode) prepareOutput(ctx context.Context, tx *sql.Tx, lease OwnerLease, result BatchResult, checkpoint []byte) error {
	// Deep-copy before trusted callback so it cannot change persisted receipts or
	// the AfterCommit event through aliases.
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var isolated BatchResult
	if err = json.Unmarshal(raw, &isolated); err != nil {
		return err
	}
	output, err := m.output(ctx, tx, SessionPendingOutput{lease, isolated, append([]byte(nil), checkpoint...)})
	if err != nil {
		return err
	}
	if len(output) == 0 || len(output) > sessionOutputBytes {
		return errors.New("Mooring session output capacity")
	}
	m.encoded = append([]byte(nil), output...)
	return nil
}
