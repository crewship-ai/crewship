package artifacts

import (
	"context"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type GridCraftOperations = Operations[mooring.Key, mooring.Request, mooring.Request, UnsupportedExportRequest, UnsupportedExportRequest, UnsupportedExportRequest, mooring.State, mooring.Receipt, []map[string]any, UnsupportedExportResult, UnsupportedExportResult, mooring.Authorize]
type GridCraftAdapter struct {
	store   *mooring.Store
	engine  mooring.Engine
	enabled bool
}

func NewGridCraftAdapter(store *mooring.Store, engine mooring.Engine, enabled bool) *GridCraftAdapter {
	return &GridCraftAdapter{store, engine, enabled}
}
func (a *GridCraftAdapter) check(op Operation) error {
	if a == nil || a.store == nil || a.engine == nil {
		return ErrUnavailable
	}
	return Check("gridcraft-workbook", 1, EnabledTypes{"gridcraft-workbook": a.enabled}, op)
}
func (a *GridCraftAdapter) Create(ctx context.Context, k mooring.Key, r mooring.Request, auth mooring.Authorize) (mooring.State, error) {
	if err := a.check(Create); err != nil {
		return mooring.State{}, err
	}
	if r.Command.Kind != "import" || r.BaseRev != 0 {
		return mooring.State{}, mooring.ErrInvalid
	}
	if _, err := a.store.Execute(ctx, k, r, auth, a.engine); err != nil {
		return mooring.State{}, err
	}
	return a.store.Read(ctx, k, auth)
}
func (a *GridCraftAdapter) Read(ctx context.Context, k mooring.Key, auth mooring.Authorize) (mooring.State, error) {
	if err := a.check(Read); err != nil {
		return mooring.State{}, err
	}
	return a.store.Read(ctx, k, auth)
}
func (a *GridCraftAdapter) Apply(ctx context.Context, k mooring.Key, r mooring.Request, auth mooring.Authorize) (mooring.Receipt, error) {
	if err := a.check(Apply); err != nil {
		return mooring.Receipt{}, err
	}
	return a.store.Execute(ctx, k, r, auth, a.engine)
}
func (a *GridCraftAdapter) Versions(context.Context, mooring.Key, mooring.Authorize) ([]map[string]any, error) {
	return nil, ErrUnsupported
}
func (a *GridCraftAdapter) Restore(context.Context, mooring.Key, UnsupportedExportRequest, mooring.Authorize) (mooring.Receipt, error) {
	return mooring.Receipt{}, ErrUnsupported
}
func (a *GridCraftAdapter) Snapshot(context.Context, mooring.Key, UnsupportedExportRequest, mooring.Authorize) (UnsupportedExportResult, error) {
	return UnsupportedExportResult{}, ErrUnsupported
}
func (a *GridCraftAdapter) Export(context.Context, mooring.Key, UnsupportedExportRequest, mooring.Authorize) (UnsupportedExportResult, error) {
	return UnsupportedExportResult{}, ErrUnsupported
}

var _ GridCraftOperations = (*GridCraftAdapter)(nil)
