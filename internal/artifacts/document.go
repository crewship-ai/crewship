package artifacts

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/crewship-ai/crewship/internal/documents"
)

// DocumentAdapter preserves Document's typed protocol, writer fencing and
// receipts. It does not interpret CRDT payloads; the trusted Docs engine must
// validate them first. Every supported operation requires live Go authorization
// through documents.Authorize inside the store's fenced transaction.
type DocumentAdapter struct {
	store   *documents.Store
	enabled bool
}

func NewDocumentAdapter(store *documents.Store, enabled bool) *DocumentAdapter {
	return &DocumentAdapter{store: store, enabled: enabled}
}

func (a *DocumentAdapter) check(schema int, operation Operation) error {
	if a == nil || a.store == nil {
		return errors.New("document adapter requires a store")
	}
	return Check("doc", schema, EnabledTypes{"doc": a.enabled}, operation)
}

type CreateDocument struct {
	ID, Fingerprint string
	Schema          int
	Snapshot        []byte
	Projection      json.RawMessage
}

func (a *DocumentAdapter) Create(ctx context.Context, key documents.Key, request CreateDocument, authorize documents.Authorize) (documents.State, error) {
	if err := a.check(request.Schema, Create); err != nil {
		return documents.State{}, err
	}
	state, err := a.store.InitRequest(ctx, key, request.ID, request.Fingerprint, request.Schema, request.Snapshot, request.Projection, authorize)
	if err != nil {
		return documents.State{}, err
	}
	// An exact initialization retry returns the current head, which may have
	// advanced to a schema newer than the original request.
	if err := a.check(state.Schema, Create); err != nil {
		return documents.State{}, err
	}
	return state, nil
}

func (a *DocumentAdapter) Read(ctx context.Context, key documents.Key, authorize documents.Authorize) (documents.State, error) {
	if err := a.check(documents.LatestSchema, Read); err != nil {
		return documents.State{}, err
	}
	state, err := a.store.Read(ctx, key, authorize)
	if err != nil {
		return documents.State{}, err
	}
	if err := a.check(state.Schema, Read); err != nil {
		return documents.State{}, err
	}
	return state, nil
}

func (a *DocumentAdapter) Apply(ctx context.Context, key documents.Key, request documents.Commit, authorize documents.Authorize) (documents.Receipt, error) {
	if err := a.check(request.Schema, Apply); err != nil {
		return documents.Receipt{}, err
	}
	return a.store.Commit(ctx, key, request, authorize)
}

// Versions lists pinned checkpoints, not every individual CRDT update.
func (a *DocumentAdapter) Versions(ctx context.Context, key documents.Key, authorize documents.Authorize) ([]map[string]any, error) {
	if err := a.check(documents.LatestSchema, Versions); err != nil {
		return nil, err
	}
	return a.store.Checkpoints(ctx, key, authorize)
}

type SnapshotDocument struct {
	Lease           documents.Lease
	ID, Fingerprint string
	Snapshot        []byte
	IfRev           int64
	Generation      *int64
	Guard           documents.MutationGuard
}

func (a *DocumentAdapter) Snapshot(ctx context.Context, key documents.Key, request SnapshotDocument, authorize documents.Authorize) (documents.Checkpoint, error) {
	if err := a.check(documents.LatestSchema, Snapshot); err != nil {
		return documents.Checkpoint{}, err
	}
	return a.store.CheckpointRequestGuarded(ctx, key, request.Lease, request.ID, request.Fingerprint, request.Snapshot, request.IfRev, request.Generation, authorize, request.Guard)
}

type RestoreDocument struct {
	Lease                       documents.Lease
	Checkpoint, ID, Fingerprint string
	IfRev                       int64
	Generation                  *int64
}

func (a *DocumentAdapter) Restore(ctx context.Context, key documents.Key, request RestoreDocument, authorize documents.Authorize) (documents.Receipt, error) {
	if err := a.check(documents.LatestSchema, Restore); err != nil {
		return documents.Receipt{}, err
	}
	return a.store.RestoreRequestWithFingerprintAtGeneration(ctx, key, request.Lease, request.Checkpoint, request.ID, request.Fingerprint, request.IfRev, request.Generation, authorize)
}
