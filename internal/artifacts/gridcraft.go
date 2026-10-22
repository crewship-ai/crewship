package artifacts

import (
	"context"
	"github.com/crewship-ai/crewship/internal/appengine"
)

type GridCraftOperations = Operations[appengine.Key, appengine.Request, appengine.Request, UnsupportedExportRequest, UnsupportedExportRequest, UnsupportedExportRequest, appengine.State, appengine.Receipt, []map[string]any, UnsupportedExportResult, UnsupportedExportResult, appengine.Authorize]
type GridCraftAdapter struct {
	store   *appengine.Store
	engine  appengine.Engine
	enabled bool
}

func NewGridCraftAdapter(store *appengine.Store, engine appengine.Engine, enabled bool) *GridCraftAdapter {
	return &GridCraftAdapter{store, engine, enabled}
}
func (a *GridCraftAdapter) check(op Operation) error {
	if a == nil || a.store == nil || a.engine == nil {
		return ErrUnavailable
	}
	return Check("gridcraft-workbook", 1, EnabledTypes{"gridcraft-workbook": a.enabled}, op)
}
func (a *GridCraftAdapter) Create(ctx context.Context, k appengine.Key, r appengine.Request, auth appengine.Authorize) (appengine.State, error) {
	if err := a.check(Create); err != nil {
		return appengine.State{}, err
	}
	if r.Command.Kind != "import" || r.BaseRev != 0 {
		return appengine.State{}, appengine.ErrInvalid
	}
	if _, err := a.store.Execute(ctx, k, r, auth, a.engine); err != nil {
		return appengine.State{}, err
	}
	return a.store.Read(ctx, k, auth)
}
func (a *GridCraftAdapter) Read(ctx context.Context, k appengine.Key, auth appengine.Authorize) (appengine.State, error) {
	if err := a.check(Read); err != nil {
		return appengine.State{}, err
	}
	return a.store.Read(ctx, k, auth)
}
func (a *GridCraftAdapter) Apply(ctx context.Context, k appengine.Key, r appengine.Request, auth appengine.Authorize) (appengine.Receipt, error) {
	if err := a.check(Apply); err != nil {
		return appengine.Receipt{}, err
	}
	return a.store.Execute(ctx, k, r, auth, a.engine)
}
func (a *GridCraftAdapter) Versions(context.Context, appengine.Key, appengine.Authorize) ([]map[string]any, error) {
	return nil, ErrUnsupported
}
func (a *GridCraftAdapter) Restore(context.Context, appengine.Key, UnsupportedExportRequest, appengine.Authorize) (appengine.Receipt, error) {
	return appengine.Receipt{}, ErrUnsupported
}
func (a *GridCraftAdapter) Snapshot(context.Context, appengine.Key, UnsupportedExportRequest, appengine.Authorize) (UnsupportedExportResult, error) {
	return UnsupportedExportResult{}, ErrUnsupported
}
func (a *GridCraftAdapter) Export(context.Context, appengine.Key, UnsupportedExportRequest, appengine.Authorize) (UnsupportedExportResult, error) {
	return UnsupportedExportResult{}, ErrUnsupported
}

var _ GridCraftOperations = (*GridCraftAdapter)(nil)
