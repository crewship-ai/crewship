package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCollaborationSubmission = errors.New("original collaboration submission unavailable")

const collaborationSubmissionOperations = 4096
const collaborationSubmissionBytes = 4 << 20

type collaborationSubmissionAuthor struct {
	human *collaborationHumanAdmission
	agent *collaborationAgentPageAccess
}
type collaborationSubmissionEntry struct {
	change, membership string
	task               string
	operation          mooring.BatchOperation
	author             collaborationSubmissionAuthor
}

// Process-local original ingress only. No raw network route installs this
// registry. All entries survive a Rust-only rebind; none survive a host restart
// or restore. Capacity never silently evicts pending original proof.
type collaborationSubmissionRegistry struct {
	humans  *collaborationHumanRegistry
	mu      sync.Mutex
	entries map[string]collaborationSubmissionEntry
	bytes   int
	agents  map[string]*collaborationAgentSubmissionParticipant
}
type collaborationSubmissionContextKey struct{}
type collaborationSubmissionOperation struct {
	registry *collaborationSubmissionRegistry
	binding  collaborationHumanStoreBinding
	active   atomic.Bool
}

func newCollaborationSubmissionRegistry(humans *collaborationHumanRegistry) (*collaborationSubmissionRegistry, error) {
	if humans == nil {
		return nil, errCollaborationSubmission
	}
	if _, err := humans.storeBinding(); err != nil {
		return nil, err
	}
	return &collaborationSubmissionRegistry{humans: humans, entries: make(map[string]collaborationSubmissionEntry), agents: make(map[string]*collaborationAgentSubmissionParticipant)}, nil
}

// run holds the original host incarnation and runtime lease BEFORE acquiring a
// writer, through COMMIT and the bounded response decision. No historical human
// reader context is required to process a mixed-author sealed batch.
func (s *collaborationSubmissionRegistry) run(ctx context.Context, binding collaborationHumanStoreBinding, fn func(context.Context) error) error {
	if s == nil || fn == nil {
		return errCollaborationSubmission
	}
	unlock, err := s.humans.acquireRuntimeOperation()
	if err != nil {
		return err
	}
	defer unlock()
	if !s.humans.matchesRuntimeBinding(binding.runtime, binding.id) {
		return errCollaborationSubmission
	}
	release, err := s.humans.fence.acquireOriginal(s.humans.lifecycle)
	if err != nil {
		return err
	}
	defer release()
	operation := &collaborationSubmissionOperation{registry: s, binding: binding}
	operation.active.Store(true)
	defer operation.active.Store(false)
	return fn(context.WithValue(ctx, collaborationSubmissionContextKey{}, operation))
}
func (s *collaborationSubmissionRegistry) active(ctx context.Context) (*collaborationSubmissionOperation, error) {
	operation, _ := ctx.Value(collaborationSubmissionContextKey{}).(*collaborationSubmissionOperation)
	if s == nil || operation == nil || operation.registry != s || !operation.active.Load() || !s.humans.matchesRuntimeBinding(operation.binding.runtime, operation.binding.id) {
		return nil, errCollaborationSubmission
	}
	return operation, nil
}

// withWriter owns the admission transaction. Publication happens only AFTER its
// COMMIT; a caller cannot roll back a provisional grant and retain its seal.
func (s *collaborationSubmissionRegistry) withWriter(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.humans.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// sealHuman requires the complete authenticated human operation and submission
// operation. The bridge must first qualify raw transport and application delta
// witness. This layer only binds full immutable membership to its original author.
func (s *collaborationSubmissionRegistry) sealHuman(ctx context.Context, changes []mooring.BatchChange) error {
	if _, err := s.active(ctx); err != nil {
		return err
	}
	binding, ok := ctx.Value(collaborationHumanBindingKey{}).(collaborationHumanBinding)
	if !ok || binding.registry != s.humans || binding.operation == nil || !binding.operation.active.Load() {
		return errCollaborationSubmission
	}
	s.humans.mu.Lock()
	admission, ok := s.humans.admissions[binding.participant]
	s.humans.mu.Unlock()
	if !ok {
		return errCollaborationSubmission
	}
	for _, c := range changes {
		if c.TaskID != "" {
			return errCollaborationSubmission
		}
	}
	if _, err := mooring.NormalizeSessionIngress(s.humans.scope, changes); err != nil {
		return err
	}
	if err := s.withWriter(ctx, func(tx *sql.Tx) error { _, err := s.humans.checkCaptured(ctx, tx, admission, true, 0); return err }); err != nil {
		return err
	}
	return s.seal(changes, admission.participant, collaborationSubmissionAuthor{human: &admission})
}

type collaborationAgentSubmissionParticipant struct {
	registry *collaborationSubmissionRegistry
	id       string
	captured collaborationAgentPageAccess
}

// An opaque host object, not a wire participant string or body-supplied proof.
// No raw bearer survives Check. This grants no content delivery gate by itself.
func (s *collaborationSubmissionRegistry) admitAgent(ctx context.Context, r *http.Request, store *collaborationAgentMandateStore, handle string) (*collaborationAgentSubmissionParticipant, error) {
	if _, err := s.active(ctx); err != nil {
		return nil, err
	}
	if r == nil || store == nil || store.fence != s.humans.fence || store.pages == nil || store.pages.db != s.humans.pages.db {
		return nil, errCollaborationSubmission
	}
	var captured collaborationAgentPageAccess
	err := s.withWriter(ctx, func(tx *sql.Tx) error {
		var err error
		captured, err = store.Check(r.WithContext(ctx), tx, handle, s.humans.scope, "task")
		return err
	})
	if err != nil {
		return nil, err
	}
	id, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, prior := range s.agents {
		if prior.captured.workID == captured.workID {
			if prior.captured.store != captured.store || prior.captured.runID != captured.runID || prior.captured.attempt != captured.attempt || prior.captured.generation != captured.generation || prior.captured.mandateRef != captured.mandateRef || prior.captured.credentialBinding != captured.credentialBinding {
				return nil, errCollaborationSubmission
			}
			// Repeated bridge admission cannot allocate a new participant identity for
			// the same fixed work attempt or refresh the original captured horizon.
			return prior, nil
		}
	}
	if len(s.agents) >= 100 {
		return nil, mooring.ErrBatchCapacity
	}
	participant := &collaborationAgentSubmissionParticipant{s, id, captured}
	s.agents[id] = participant
	return participant, nil
}

func (s *collaborationSubmissionRegistry) sealAgent(ctx context.Context, r *http.Request, participant *collaborationAgentSubmissionParticipant, changes []mooring.BatchChange) error {
	if _, err := s.active(ctx); err != nil {
		return err
	}
	if r == nil || participant == nil || participant.registry != s || participant.captured.store == nil {
		return errCollaborationSubmission
	}
	s.mu.Lock()
	issued := s.agents[participant.id] == participant
	s.mu.Unlock()
	if !issued {
		return errCollaborationSubmission
	}
	captured := participant.captured
	for _, c := range changes {
		if c.TaskID != captured.workID {
			return errCollaborationSubmission
		}
	}
	if _, err := mooring.NormalizeSessionIngress(s.humans.scope, changes); err != nil {
		return err
	}
	err := s.withWriter(ctx, func(tx *sql.Tx) error {
		current, err := captured.store.Check(r.WithContext(ctx), tx, captured.mandateRef, s.humans.scope, "task")
		if err != nil {
			return err
		}
		if current.store != captured.store || current.mandateRef != captured.mandateRef || current.credentialBinding != captured.credentialBinding || current.workID != captured.workID || current.runID != captured.runID || current.attempt != captured.attempt || current.generation != captured.generation || current.agentID != captured.agentID || current.crewID != captured.crewID || current.intent != captured.intent {
			return errCollaborationSubmission
		}
		decision, err := captured.store.DecideCaptured(ctx, tx, captured, s.humans.scope, "task")
		if err != nil {
			return err
		}
		if decision.kind != collaborationAgentAllowed {
			return errCollaborationSubmission
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.seal(changes, participant.id, collaborationSubmissionAuthor{agent: &captured})
}

func (s *collaborationSubmissionRegistry) seal(changes []mooring.BatchChange, participant string, author collaborationSubmissionAuthor) error {
	normalized, err := mooring.NormalizeSessionIngress(s.humans.scope, changes)
	if err != nil {
		return err
	}
	pending := make(map[string]collaborationSubmissionEntry)
	addedBytes := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range normalized {
		for _, op := range c.Operations {
			if op.Participant != participant {
				return errCollaborationSubmission
			}
			entry := collaborationSubmissionEntry{c.ID, c.MembershipHash, c.TaskID, op, author}
			if prior, ok := s.entries[op.ID]; ok {
				if !reflect.DeepEqual(prior, entry) {
					return mooring.ErrOperationConflict
				}
				continue
			}
			encoded, err := json.Marshal(op)
			if err != nil {
				return err
			}
			addedBytes += len(encoded) + len(c.ID) + len(c.MembershipHash) + len(c.TaskID)
			pending[op.ID] = entry
		}
	}
	if len(s.entries)+len(pending) > collaborationSubmissionOperations || s.bytes+addedBytes > collaborationSubmissionBytes {
		return mooring.ErrBatchCapacity
	}
	for id, entry := range pending {
		s.entries[id] = entry
	}
	s.bytes += addedBytes
	return nil
}

// bind captures one exact owner and immutable runtime binding. External status
// retains the ordinary current-reader authority; this callback is commit-only.
func (s *collaborationSubmissionRegistry) bind(owner mooring.OwnerLease, binding collaborationHumanStoreBinding) mooring.SessionBatchAuthority {
	return func(ctx context.Context, tx *sql.Tx, access mooring.SessionBatchAccess) error {
		operation, err := s.active(ctx)
		if err != nil || tx == nil || owner.Key != s.humans.scope || access.Lease != owner || access.Access.Scope != owner.Key || operation.binding.runtime != binding.runtime || operation.binding.id != binding.id {
			return errCollaborationSubmission
		}
		if c := s.humans.sessionStore; c != nil {
			if !access.Access.FromStore(c.store) {
				return errCollaborationSubmission
			}
			if err = c.check(ctx, tx); err != nil {
				return err
			}
		}
		s.mu.Lock()
		var entry collaborationSubmissionEntry
		found := false
		if access.Access.Operation != nil {
			entry, found = s.entries[access.Access.Operation.ID]
			if found && (entry.change != access.ChangeID || entry.membership != access.MembershipHash || !reflect.DeepEqual(entry.operation, *access.Access.Operation)) {
				found = false
			}
		} else if access.Purpose == mooring.SessionCurrentTask && access.Access.TaskID != "" {
			for _, candidate := range s.entries {
				if candidate.task == access.Access.TaskID {
					if found && !reflect.DeepEqual(entry.author, candidate.author) {
						s.mu.Unlock()
						return errCollaborationSubmission
					}
					entry, found = candidate, true
				}
			}
		}
		s.mu.Unlock()
		if !found || entry.task != access.Access.TaskID || (access.Access.Participant != "" && entry.operation.Participant != access.Access.Participant) {
			return errCollaborationSubmission
		}
		switch access.Purpose {
		case mooring.SessionProcessSealedOutcome:
			if access.Access.Intent != "outcome" || access.Access.Operation == nil {
				return errCollaborationSubmission
			}
			return nil
		case mooring.SessionCurrentWrite:
			if access.Access.Intent != "write" || access.Access.Operation == nil {
				return errCollaborationSubmission
			}
		case mooring.SessionCurrentTask:
			if access.Access.Intent != "task" || entry.author.agent == nil {
				return errCollaborationSubmission
			}
		default:
			return errCollaborationSubmission
		}
		if entry.author.agent != nil {
			captured := *entry.author.agent
			decision, err := captured.store.DecideCaptured(ctx, tx, captured, owner.Key, "task")
			if err != nil {
				return err
			}
			switch decision.kind {
			case collaborationAgentAllowed:
				return nil
			case collaborationAgentCanonicalPermissionChanged:
				return &mooring.BatchDenied{Code: "permission_revoked"}
			default:
				return errCollaborationSubmission
			}
		}
		if entry.author.human == nil {
			return errCollaborationSubmission
		}
		admission := *entry.author.human
		boot, tick, err := s.humans.clock.Now()
		expiry, expiryErr := admission.source.Expiry()
		if err != nil || expiryErr != nil || boot != admission.boot || tick < 0 || tick >= admission.deadline || !admission.write || (!expiry.IsZero() && !time.Now().Before(expiry)) {
			return errCollaborationSubmission
		}
		changed, err := collaborationHumanCapturedFenceChanged(ctx, tx, owner.Key, admission)
		if err != nil {
			return err
		}
		boot, tick, err = s.humans.clock.Now()
		if err != nil || boot != admission.boot || tick < 0 || tick >= admission.deadline || (!expiry.IsZero() && !time.Now().Before(expiry)) {
			return errCollaborationSubmission
		}
		if changed {
			return &mooring.BatchDenied{Code: "permission_revoked"}
		}
		_, err = s.humans.checkCaptured(ctx, tx, admission, true, 0)
		return err
	}
}

// Only successful canonical scalar reads (or known canonical absence) classify
// revocation. Loader/policy/storage errors remain nonterminal. This is not an ACL.
func collaborationHumanCapturedFenceChanged(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, admission collaborationHumanAdmission) (bool, error) {
	if !admission.authorVector.matches(key, admission.source.UserID, "", "") {
		return false, errCollaborationAuthorUnqualified
	}
	changed, err := collaborationAuthorVectorChanged(ctx, tx, admission.authorVector)
	if err != nil {
		return false, err
	}
	var workspaceRevision string
	var userRevision, memberID, workspaceDeleted, userSuspended sql.NullString
	var memberRevision sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT CAST(w.authority_revision AS TEXT),CAST(u.authority_revision AS TEXT),m.id,m.access_revision,w.deleted_at,u.suspended_at
 FROM workspaces w LEFT JOIN users u ON u.id=? LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=? WHERE w.id=?`, admission.source.UserID, admission.source.UserID, key.WorkspaceID).Scan(&workspaceRevision, &userRevision, &memberID, &memberRevision, &workspaceDeleted, &userSuspended)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !chataudience.ValidUserAuthorityRevision(workspaceRevision) || (userRevision.Valid && !chataudience.ValidUserAuthorityRevision(userRevision.String)) || (memberID.Valid && memberID.String == "") || (memberRevision.Valid && memberRevision.Int64 < 1) {
		return false, errCollaborationSubmission
	}
	return changed || workspaceDeleted.Valid || userSuspended.Valid || !userRevision.Valid || !memberID.Valid || !memberRevision.Valid || userRevision.String != admission.source.UserAuthorityRevision || memberID.String != admission.member || memberRevision.Int64 != admission.memberRevision, nil
}
