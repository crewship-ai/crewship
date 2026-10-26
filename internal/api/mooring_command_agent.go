package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// The factory supplies an actual confirmed chat attempt, never request JSON or
// a human admission. Logical history identity is stable across tasks; the
// original mandate is a separate transport/SQL identity for every outcome.
type collaborationCommandAgent struct {
	store     *collaborationCommandStore
	confirmed collaborationAgentConfirmed
	actor     string
}

func (s *collaborationCommandStore) bindAgent(confirmed collaborationAgentConfirmed, actor string) (*collaborationCommandAgent, error) {
	if s == nil || s.humans == nil || s.pages == nil || s.port == nil || confirmed.store == nil || confirmed.store.pages != s.pages || confirmed.store.fence != s.humans.fence || confirmed.key != s.owner.Key || actor != "agent:"+confirmed.agentID || !mooring.ValidWireIdentifier(actor) {
		return nil, errCollaborationAgentAccess
	}
	a := &collaborationCommandAgent{store: s, confirmed: confirmed, actor: actor}
	if _, err := a.entry(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *collaborationCommandAgent) entry() (collaborationAgentMandateEntry, error) {
	if a == nil || a.store == nil || a.confirmed.store == nil || a.confirmed.execID == "" {
		return collaborationAgentMandateEntry{}, errCollaborationAgentAccess
	}
	s := a.confirmed.store
	s.mu.Lock()
	entry, ok := s.attached[a.confirmed.mandate]
	s.mu.Unlock()
	attempt := entry.binding.attempt
	if !ok || entry.binding.task.key != a.store.owner.Key || attempt.workID != a.confirmed.workID || attempt.runID != a.confirmed.runID || attempt.agentID != a.confirmed.agentID || attempt.crewID != a.confirmed.crewID || attempt.attempt != int64(a.confirmed.attempt) || attempt.generation != a.confirmed.generation || attempt.attempt < 1 || attempt.generation < 1 {
		return collaborationAgentMandateEntry{}, errCollaborationAgentAccess
	}
	return entry, nil
}

// Each call captures one original proof in its first FULL SQLite writer. Later
// stages recheck that exact proof; a new bearer, grant or attempt cannot refresh
// its horizon. No raw bearer or request survives the operation.
func (a *collaborationCommandAgent) operation(r *http.Request, intent string) (commandOperationSource, error) {
	if a == nil || r == nil {
		return commandOperationSource{}, errCollaborationAgentAccess
	}
	entry, err := a.entry()
	if err != nil {
		return commandOperationSource{}, err
	}
	s, mandates := a.store, a.confirmed.store
	var captured *collaborationAgentPageAccess
	return commandOperationSource{
		participant: a.confirmed.mandate,
		run: func(call func(context.Context) error) error {
			unlock, err := s.humans.acquireRuntimeOperation()
			if err != nil {
				return err
			}
			defer unlock()
			if err = s.live(); err != nil {
				return err
			}
			stamp, err := mandates.fence.capture(r.Context())
			if err != nil || stamp != s.humans.lifecycle || stamp != entry.binding.task.ingress {
				return errCollaborationAgentAccess
			}
			release, err := mandates.fence.acquire(r.Context(), entry.binding.task.ingress)
			if err != nil {
				return err
			}
			defer release()
			return call(r.Context())
		},
		admit: func(ctx context.Context, tx *sql.Tx, writer bool) (commandHostAdmission, error) {
			if writer != (intent == "task") {
				return commandHostAdmission{}, errCollaborationAgentAccess
			}
			if captured == nil {
				proof, err := mandates.Check(r.WithContext(ctx), tx, a.confirmed.mandate, s.owner.Key, intent)
				if err != nil {
					return commandHostAdmission{}, err
				}
				if proof.workID != a.confirmed.workID || proof.runID != a.confirmed.runID || proof.agentID != a.confirmed.agentID || proof.crewID != a.confirmed.crewID || proof.attempt != int64(a.confirmed.attempt) || proof.generation != a.confirmed.generation {
					return commandHostAdmission{}, errCollaborationAgentAccess
				}
				captured = &proof
			}
			decision, err := mandates.DecideCaptured(ctx, tx, *captured, s.owner.Key, intent)
			if err != nil {
				return commandHostAdmission{}, err
			}
			if decision.kind != collaborationAgentAllowed {
				return commandHostAdmission{}, errCollaborationAgentAccess
			}
			return commandHostAdmission{TransportParticipant: a.confirmed.mandate, Author: a.actor, Epoch: uint64(captured.generation), Writer: writer}, nil
		},
		lookup: func(ctx context.Context, tx *sql.Tx, op string) (*commandSQLOutcome, error) {
			var participant string
			err := tx.QueryRowContext(ctx, `SELECT participant_ref FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, s.owner.Key.ArtifactID, op).Scan(&participant)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil && participant != a.confirmed.mandate {
				return nil, errCommandConflict
			}
			if errors.Is(err, sql.ErrNoRows) && intent == "task" && !entry.binding.task.allowPartial {
				// Default consent covers one atomic task result, not multiple
				// independently committed commands. Only the ORIGINAL task flag
				// can permit partial results; request data cannot opt into it.
				var committed bool
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_operations WHERE page_id=? AND participant_ref=?)`, s.owner.Key.ArtifactID, a.confirmed.mandate).Scan(&committed); err != nil {
					return nil, err
				}
				if committed {
					return nil, errCommandConflict
				}
			}
			return s.lookup(ctx, tx, op)
		},
	}, nil
}

func (a *collaborationCommandAgent) Submit(r *http.Request, raw []byte) (commandSQLOutcome, error) {
	source, err := a.operation(r, "task")
	if err != nil {
		return commandSQLOutcome{}, err
	}
	return a.store.submit(source, a.actor, raw)
}

func (a *collaborationCommandAgent) ReadCheckpoint(r *http.Request) (commandSQLSnapshot, error) {
	source, err := a.operation(r, "read")
	if err != nil {
		return commandSQLSnapshot{}, err
	}
	return a.store.readCheckpoint(source)
}

func (a *collaborationCommandAgent) Reconcile(r *http.Request, op, fingerprint string) (*commandSQLOutcome, error) {
	source, err := a.operation(r, "outcome")
	if err != nil {
		return nil, err
	}
	return a.store.reconcile(source, op, fingerprint, "")
}

func (a *collaborationCommandAgent) ReconcileOriginal(r *http.Request, raw []byte) (*commandSQLOutcome, error) {
	source, err := a.operation(r, "outcome")
	if err != nil {
		return nil, err
	}
	op, _, canonical, err := commandInput(raw, a.actor)
	if err != nil {
		return nil, err
	}
	return a.store.reconcile(source, op, "", commandDigest(canonical))
}
