package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/slides"
)

// SlidesAdapter retains the existing Store, request IDs, CAS, receipts, quotas
// and revision hooks. Actor permission remains the caller's live Go authority.
// Schema 2/image operations require a separately qualified asset read policy.
type SlidesAdapter struct {
	store   *slides.Store
	enabled bool
}

func NewSlidesAdapter(store *slides.Store, enabled bool) *SlidesAdapter {
	return &SlidesAdapter{store: store, enabled: enabled}
}
func (a *SlidesAdapter) check(schema int, operation Operation) error {
	if a == nil || a.store == nil {
		return fmt.Errorf("%w: slides", ErrUnavailable)
	}
	return Check("slides", schema, EnabledTypes{"slides": a.enabled}, operation)
}
func slidesSchemaUnavailable() error {
	return fmt.Errorf("%w: Slides schema unavailable", ErrUnsupported)
}

// Capability constraints follow the original actor authorization in the SAME
// Store transaction, on both authority calls. No follow-up read or actor ACL is
// substituted. Queried head/target metadata is never included in an error.
func slidesSchemaAuthority(key slides.Key, original slides.Authorize, create bool, target int64) slides.Authorize {
	if original == nil {
		return nil
	}
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := original(ctx, tx)
		if err != nil {
			return "", err
		}
		if actor == "" {
			return "", errors.New("Slides authority returned no actor")
		}
		var schema int
		err = tx.QueryRowContext(ctx, `SELECT schema_version FROM slides_heads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&schema)
		if errors.Is(err, sql.ErrNoRows) {
			if create {
				return actor, nil
			}
			return "", slides.ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if schema != 1 {
			return "", slidesSchemaUnavailable()
		}
		if target > 0 {
			err = tx.QueryRowContext(ctx, `SELECT schema_version FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=?`, key.WorkspaceID, key.PageID, target).Scan(&schema)
			if errors.Is(err, sql.ErrNoRows) {
				return "", slides.ErrNotFound
			}
			if err != nil {
				return "", err
			}
			if schema != 1 {
				return "", slidesSchemaUnavailable()
			}
		}
		return actor, nil
	}
}
func (a *SlidesAdapter) Create(ctx context.Context, key slides.Key, request slides.InitializeRequest, authorize slides.Authorize) (slides.Receipt, error) {
	if err := a.check(request.SchemaVersion, Create); err != nil {
		return slides.Receipt{}, err
	}
	if request.Deck.SchemaVersion != 1 {
		return slides.Receipt{}, slidesSchemaUnavailable()
	}
	receipt, err := a.store.Initialize(ctx, key, request, slidesSchemaAuthority(key, authorize, true, 0))
	if err != nil {
		return slides.Receipt{}, err
	}
	return receipt, nil
}
func (a *SlidesAdapter) Read(ctx context.Context, key slides.Key, authorize slides.Authorize) (slides.State, error) {
	if err := a.check(1, Read); err != nil {
		return slides.State{}, err
	}
	state, err := a.store.Read(ctx, key, 0, slidesSchemaAuthority(key, authorize, false, 0))
	if err != nil {
		return slides.State{}, err
	}
	if state.SchemaVersion != 1 || state.Deck.SchemaVersion != 1 {
		return slides.State{}, slidesSchemaUnavailable()
	}
	return state, nil
}
func (a *SlidesAdapter) Apply(ctx context.Context, key slides.Key, request slides.PatchRequest, authorize slides.Authorize) (slides.Receipt, error) {
	if err := a.check(request.SchemaVersion, Apply); err != nil {
		return slides.Receipt{}, err
	}
	if request.TargetSchemaVersion != nil && *request.TargetSchemaVersion != 1 {
		return slides.Receipt{}, slidesSchemaUnavailable()
	}
	receipt, err := a.store.Patch(ctx, key, request, slidesSchemaAuthority(key, authorize, false, 0))
	if err != nil {
		return slides.Receipt{}, err
	}
	return receipt, nil
}
func (a *SlidesAdapter) Versions(ctx context.Context, key slides.Key, authorize slides.Authorize) ([]slides.Revision, error) {
	if err := a.check(1, Versions); err != nil {
		return nil, err
	}
	history, err := a.store.History(ctx, key, slidesSchemaAuthority(key, authorize, false, 0))
	if err != nil {
		return nil, err
	}
	for _, revision := range history {
		if revision.SchemaVersion != 1 {
			return nil, slidesSchemaUnavailable()
		}
	}
	return history, nil
}
func (a *SlidesAdapter) Restore(ctx context.Context, key slides.Key, request slides.RestoreRequest, authorize slides.Authorize) (slides.Receipt, error) {
	if err := a.check(request.SchemaVersion, Restore); err != nil {
		return slides.Receipt{}, err
	}
	receipt, err := a.store.Restore(ctx, key, request, slidesSchemaAuthority(key, authorize, false, request.Revision))
	if err != nil {
		return slides.Receipt{}, err
	}
	return receipt, nil
}

// Unsupported slots carry no executable payload or promised wire format.
type UnsupportedSnapshotRequest struct{}
type UnsupportedSnapshotResult struct{}

func (*SlidesAdapter) Snapshot(context.Context, slides.Key, UnsupportedSnapshotRequest, slides.Authorize) (UnsupportedSnapshotResult, error) {
	return UnsupportedSnapshotResult{}, fmt.Errorf("%w: Slides snapshot unavailable", ErrUnsupported)
}
func (*SlidesAdapter) Export(context.Context, slides.Key, UnsupportedExportRequest, slides.Authorize) (UnsupportedExportResult, error) {
	return UnsupportedExportResult{}, fmt.Errorf("%w: Slides export unavailable", ErrUnsupported)
}
