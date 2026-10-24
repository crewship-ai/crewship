package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"reflect"
	"strconv"
	"strings"
)

// These are fixed initial session-profile caps, not a change to generic PRD
// checkpoint limits. No runtime/profile is admitted by this unused primitive.
const sessionOutputBytes = 2 * 1024 * 1024

// SessionBasis names a retained canonical checkpoint at an atomic boundary.
// It is transport context, never a rewrite of an operation's immutable reads.
type SessionBasis struct{ Revision, Digest string }

// SessionFullBatchRequest retains the original wire identity until the authorized
// writer resolves it. It cannot be substituted with an opaque content digest.
type SessionFullBatchRequest struct {
	Lease   OwnerLease
	Basis   FullCheckpointBasis
	Changes []BatchChange
}
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
	// FullCheckpoint and Targets are populated only by CommitFull from retained SQL.
	FullCheckpoint *protocol.Checkpoint
	Targets        []BatchTargetVersion
}
type SessionOutputPreparer func(context.Context, *sql.Tx, SessionPendingOutput) ([]byte, error)
type SessionBatchResult struct {
	Result        BatchResult
	EncodedOutput []byte
}
type SessionCommitter struct {
	store         *BatchStore
	owner         OwnerLease
	authority     SessionBatchAuthority
	output        SessionOutputPreparer
	humanResolver SessionHumanPrepareResolver
}

func NewSessionCommitter(store *BatchStore, owner OwnerLease, authority SessionBatchAuthority, output SessionOutputPreparer) (*SessionCommitter, error) {
	if store == nil || store.ready() != nil || !store.targetsEnabled() || store.config.Targets.ReadPolicy != CausalTargetReadPolicy || !batchID(owner.Key.WorkspaceID) || !batchID(owner.Key.ArtifactID) || !batchID(owner.Key.ApplicationID) || !ownerIDValid(owner.OwnerID) || owner.Generation <= 0 || len(owner.Token) != 64 || authority == nil || output == nil {
		return nil, ErrInvalid
	}
	// Capture the exact host-owned lease, including generation and secret token.
	// A replacement runtime requires a new committer and captured authority; this
	// does not replace the transactional OwnerStore.Check below.
	// Copy immutable configuration; never replace the public store's current-reader
	// authority. Only the commit-only clone below can use processing authority.
	return &SessionCommitter{store: store, owner: owner, authority: authority, output: output, humanResolver: store.config.Targets.SessionHumanPrepare}, nil
}

type sessionMembership struct{ changeID, membershipHash string }

type sessionCommitMode struct {
	originalStore *BatchStore
	humanResolver SessionHumanPrepareResolver
	fullBasis     *FullCheckpointBasis
	members       map[string]sessionMembership
	basis         SessionBasis
	output        SessionOutputPreparer
	encoded       []byte
}

func (s *SessionCommitter) Commit(ctx context.Context, request SessionBatchRequest) (SessionBatchResult, error) {
	if s == nil || s.store == nil || s.store.checkpointIdentityEnabled() || request.Lease != s.owner || !validSessionBasis(request.Basis) {
		return SessionBatchResult{}, ErrInvalid
	}
	return s.commitMode(ctx, request, &sessionCommitMode{basis: request.Basis, output: s.output})
}

// CommitFull qualifies the original wire identity under the same writer after
// exact sealed-authority checks. It never rewrites immutable operation reads.
func (s *SessionCommitter) CommitFull(ctx context.Context, request SessionFullBatchRequest) (SessionBatchResult, error) {
	if s == nil || s.store == nil || !s.store.checkpointIdentityEnabled() || request.Lease != s.owner || !validFullCheckpointBasis(request.Basis) {
		return SessionBatchResult{}, ErrInvalid
	}
	basis := request.Basis
	return s.commitMode(ctx, SessionBatchRequest{Lease: request.Lease, Changes: request.Changes}, &sessionCommitMode{fullBasis: &basis, output: s.output})
}
func (s *SessionCommitter) commitMode(ctx context.Context, request SessionBatchRequest, mode *sessionCommitMode) (SessionBatchResult, error) {
	mode.originalStore = s.store
	mode.humanResolver = s.humanResolver
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
		// This private copy changes only sealed-processing authority; its original
		// initialized store remains the verified runtime/store binding.
		access.store = s.store
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
func (m *sessionCommitMode) prepareOutput(ctx context.Context, tx *sql.Tx, store *BatchStore, lease OwnerLease, result BatchResult, checkpoint []byte) error {
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
	pending := SessionPendingOutput{Lease: lease, Result: isolated, Checkpoint: append([]byte(nil), checkpoint...)}
	var expected *protocol.Checkpoint
	var targets []BatchTargetVersion
	if m.fullBasis != nil {
		cp, identity, e := store.loadRetainedFullCheckpoint(ctx, tx, lease.Key, result.Revision, true)
		if e != nil {
			return e
		}
		if !bytes.Equal(identity.State, checkpoint) {
			return ErrCheckpointIdentity
		}
		head, e := batchCounter(result.Revision)
		if e != nil {
			return e
		}
		targets, e = initialSessionTargets(ctx, tx, store, lease.Key, head)
		if e != nil {
			return e
		}
		expected = &cp
		copied := cp
		copied.Assets = append([]protocol.Asset{}, cp.Assets...)
		pending.FullCheckpoint = &copied
		pending.Targets = append([]BatchTargetVersion{}, targets...)
	}
	output, err := m.output(ctx, tx, pending)
	if err != nil {
		return err
	}
	if len(output) == 0 || len(output) > sessionOutputBytes {
		return errors.New("Mooring session output capacity")
	}
	if m.fullBasis != nil {
		output, err = validateFullSessionOutput(output, lease, result, *expected, targets)
		if err != nil {
			return err
		}
	}
	m.encoded = append([]byte(nil), output...)
	return nil
}

// This validation binds committed SQL data to the closed output grammar; the
// concrete session manager additionally owns request/session/runtime correlation.
func validateFullSessionOutput(raw []byte, lease OwnerLease, result BatchResult, cp protocol.Checkpoint, targets []BatchTargetVersion) ([]byte, error) {
	canonical, err := canonicalPrivateControlJSON(raw)
	if err != nil {
		return nil, err
	}
	validator, err := newPrivateControlValidator()
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if json.Unmarshal(canonical, &value) != nil || value["type"] != "session_commit_result" || !validator.schema.matches(validator.schema.root, value, 0) {
		return nil, ErrPrivateControl
	}
	var message protocol.SessionCommitResult
	if json.Unmarshal(canonical, &message) != nil {
		return nil, ErrPrivateControl
	}
	if message.Scope != cp.Scope || message.Versions != cp.Versions || message.Fence.Generation != protocol.Counter(strconv.FormatInt(lease.Generation, 10)) || message.Fence.Token != lease.Token || !reflect.DeepEqual(message.Checkpoint, cp) || message.Receipt.RequestID != message.RequestID || message.Receipt.Issuer != "crewship" || message.Receipt.Scope != cp.Scope || message.Receipt.Generation != message.Fence.Generation || string(message.Receipt.Revision) != result.Revision {
		return nil, ErrCheckpointIdentity
	}
	changesRaw, err := json.Marshal(message.Receipt.Changes)
	if err != nil {
		return nil, err
	}
	var changes []BatchChangeResult
	if json.Unmarshal(changesRaw, &changes) != nil || !reflect.DeepEqual(changes, result.Changes) {
		return nil, ErrCheckpointIdentity
	}
	targetRaw, err := json.Marshal(message.Targets)
	if err != nil {
		return nil, err
	}
	var actual []BatchTargetVersion
	if json.Unmarshal(targetRaw, &actual) != nil || !reflect.DeepEqual(actual, targets) {
		return nil, ErrCheckpointIdentity
	}
	for _, change := range result.Changes {
		for _, op := range change.Operations {
			if op.Status == "saved" && (string(message.Receipt.Issuer) != op.Issuer || (message.Receipt.Durability == "replicated" && op.Durability != "replicated")) {
				return nil, ErrCheckpointIdentity
			}
		}
	}
	return canonical, nil
}
