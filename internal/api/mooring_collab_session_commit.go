package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// One concrete host connection from original sealed submissions to durable SQL.
// It admits no participant, assigns no origin and grants no delivery authority.
// A trusted session pump may call submit only after bounding/validating the
// original private control frame. Structured arguments are not raw ingress.
type collaborationSessionCommitter struct {
	cell        *collaborationSessionStore
	submissions *collaborationSubmissionRegistry
	binding     collaborationHumanStoreBinding
	owner       mooring.OwnerLease
	scope       protocol.Scope
	session     protocol.Identifier
	runtime     protocol.Identifier
	versions    protocol.Versions
	fence       protocol.Fence
	originals   *collaborationLiveOriginals
	native      bool
}

func newCollaborationSessionCommitter(ctx context.Context, cell *collaborationSessionStore, submissions *collaborationSubmissionRegistry, owner mooring.OwnerLease) (*collaborationSessionCommitter, error) {
	return newCollaborationSessionCommitterWithOriginals(ctx, cell, submissions, owner, nil)
}

func newCollaborationSessionCommitterWithOriginals(ctx context.Context, cell *collaborationSessionStore, submissions *collaborationSubmissionRegistry, owner mooring.OwnerLease, originals *collaborationLiveOriginals) (*collaborationSessionCommitter, error) {
	if ctx == nil || cell == nil || submissions == nil || submissions.humans == nil || submissions.humans.sessionStore != cell || owner.Key != cell.scope || owner.Generation < 1 {
		return nil, errCollaborationSubmission
	}
	if originals != nil && (originals.document == nil || originals.document.originals != originals || originals.document.cell != cell || originals.document.humans != submissions.humans || originals.document.submissions != submissions || originals.document.owner != owner) {
		return nil, errCollaborationSubmission
	}
	runtime, err := cell.live()
	if err != nil || owner.OwnerID != runtime.RuntimeRef() {
		return nil, errCollaborationSubmission
	}
	binding, err := submissions.humans.storeBinding()
	if err != nil || binding.runtime != runtime || binding.adapter != cell.adapter || submissions.humans.ownerGeneration != owner.Generation {
		return nil, errCollaborationSubmission
	}
	cell.mu.Lock()
	activation := cell.activation
	cell.mu.Unlock()
	if activation == nil || activation.SessionID() != submissions.humans.hostSessionID() {
		return nil, errCollaborationSubmission
	}
	err = submissions.run(ctx, binding, func(bound context.Context) error {
		return submissions.withWriter(bound, func(tx *sql.Tx) error {
			if err := cell.check(bound, tx); err != nil {
				return err
			}
			return cell.owners.Check(bound, tx, owner)
		})
	})
	if err != nil {
		return nil, err
	}
	versions := activation.Versions()
	native := activation.RequireHumanNativePrepare() == nil
	if native && originals == nil {
		return nil, errCollaborationSubmission
	}
	return &collaborationSessionCommitter{cell: cell, submissions: submissions, binding: binding, owner: owner, originals: originals, native: native,
		scope:   protocol.Scope{WorkspaceID: protocol.Identifier(cell.scope.WorkspaceID), ArtifactID: protocol.Identifier(cell.scope.ArtifactID), ApplicationID: protocol.Identifier(cell.scope.ApplicationID)},
		session: protocol.Identifier(activation.SessionID()), runtime: protocol.Identifier(runtime.RuntimeRef()),
		versions: protocol.Versions{ProtocolVersion: versions.Protocol, ModelVersion: versions.Model, EngineVersion: versions.Engine},
		fence:    protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}}, nil
}

func (s *collaborationSessionCommitter) submit(ctx context.Context, request protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
	result, _, err := s.submitWithCuts(ctx, request)
	return result, err
}

// submitWithCuts releases only actual committed atomic cut DATA. It does not
// qualify a client history witness, assign a source or publish to participants.
func (s *collaborationSessionCommitter) submitWithCuts(ctx context.Context, request protocol.SessionSubmitBatch) (protocol.SessionCommitResult, []mooring.SessionSavedCut, error) {
	var empty protocol.SessionCommitResult
	if ctx == nil || s == nil || s.cell == nil || s.submissions == nil ||
		request.Type != "session_submit_batch" || string(request.ControlProfile) != mooring.PrivateSessionProfile || !mooring.ValidWireIdentifier(string(request.RequestID)) ||
		request.Scope != s.scope || request.SessionID != s.session || request.RuntimeRef != s.runtime || request.Versions != s.versions || request.Fence != s.fence {
		return empty, nil, errCollaborationSubmission
	}
	// Freeze owned structured input, including nested immutable operation bytes.
	// This secondary budget cannot replace the pump's original raw-frame check.
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > mooring.MaxPrivatePrepareRPCFrame {
		return empty, nil, errCollaborationSubmission
	}
	var frozen protocol.SessionSubmitBatch
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return empty, nil, errCollaborationSubmission
	}
	changesRaw, err := json.Marshal(frozen.Changes)
	if err != nil {
		return empty, nil, err
	}
	var changes []mooring.BatchChange
	if err = json.Unmarshal(changesRaw, &changes); err != nil {
		return empty, nil, err
	}
	changes, err = mooring.NormalizeSessionIngress(s.owner.Key, changes)
	if err != nil {
		return empty, nil, err
	}
	var group *collaborationOriginalGroup
	if s.native {
		group, err = s.originals.boundPublicGroup(changes)
		if err != nil {
			return empty, nil, err
		}
	}
	var result protocol.SessionCommitResult
	var cuts []mooring.SessionSavedCut
	err = s.submissions.run(ctx, s.binding, func(bound context.Context) error {
		originalAuthority := s.submissions.bind(s.owner, s.binding)
		authority := func(ctx context.Context, tx *sql.Tx, access mooring.SessionBatchAccess) error {
			if err := originalAuthority(ctx, tx, access); err != nil {
				return err
			}
			// Raw publication is checked before ANY outcome lookup/early reason,
			// not merely before application preparation. Retired sources retain
			// their already captured immutable processing proof.
			if s.originals != nil && access.Purpose == mooring.SessionProcessSealedOutcome && access.Access.TaskID == "" {
				return s.originals.authorizeOriginal(access)
			}
			return nil
		}
		committer, err := mooring.NewSessionCommitterWithSavedRecorder(s.cell.store, s.owner, authority, func(ctx context.Context, tx *sql.Tx, pending mooring.SessionPendingOutput) ([]byte, error) {
			if pending.Lease != s.owner || pending.FullCheckpoint == nil || pending.Targets == nil {
				return nil, errCollaborationSubmission
			}
			if err := s.cell.check(ctx, tx); err != nil {
				return nil, err
			}
			// The generic committer validates this closed candidate against actual SQL,
			// then runs final original-author/owner fences before its COMMIT.
			changeBytes, err := json.Marshal(pending.Result.Changes)
			if err != nil {
				return nil, err
			}
			var outcomes []protocol.ChangeResult
			if err = json.Unmarshal(changeBytes, &outcomes); err != nil {
				return nil, err
			}
			targetBytes, err := json.Marshal(pending.Targets)
			if err != nil {
				return nil, err
			}
			var targets []protocol.TargetVersion
			if err = json.Unmarshal(targetBytes, &targets); err != nil {
				return nil, err
			}
			candidate := protocol.SessionCommitResult{Type: "session_commit_result", RequestID: frozen.RequestID, ControlProfile: mooring.PrivateSessionProfile,
				Scope: s.scope, SessionID: s.session, RuntimeRef: s.runtime, Fence: s.fence, Versions: s.versions,
				Checkpoint: *pending.FullCheckpoint, Targets: targets,
				Receipt: protocol.CommitResult{Type: "commit_result", RequestID: frozen.RequestID, Scope: s.scope, Generation: s.fence.Generation,
					Revision: protocol.Counter(pending.Result.Revision), Issuer: "crewship", Durability: "node", Changes: outcomes}}
			if s.native {
				candidate.PublicReceipt, err = s.originals.preparePublicReceipt(group, candidate.Receipt)
				if err != nil {
					return nil, err
				}
			}
			return json.Marshal(candidate)
		}, func(ctx context.Context, tx *sql.Tx, change mooring.BatchChange, saved mooring.BatchChangeResult) error {
			return s.submissions.recordSavedAttribution(ctx, tx, s.owner.Key, string(s.session), change, saved)
		})
		if err != nil {
			return err
		}
		committed, err := committer.CommitFull(bound, mooring.SessionFullBatchRequest{Lease: s.owner,
			Basis: mooring.FullCheckpointBasis{Revision: string(frozen.ConfirmedBasis.Revision), Digest: string(frozen.ConfirmedBasis.Digest)}, Changes: changes})
		if err != nil {
			return err
		}
		// Only the bytes released by successful COMMIT become a response. Errors or
		// uncertainty return zero output; they are never terminal rejection receipts.
		if err := json.Unmarshal(committed.EncodedOutput, &result); err != nil {
			return err
		}
		if s.native {
			if err := s.originals.publishCommitted(group, changes, result, committed.SavedCuts, committed.NativeDeltas); err != nil {
				return err
			}
		}
		cuts = append([]mooring.SessionSavedCut(nil), committed.SavedCuts...)
		return nil
	})
	if err != nil {
		return empty, nil, err
	}
	return result, cuts, nil
}
