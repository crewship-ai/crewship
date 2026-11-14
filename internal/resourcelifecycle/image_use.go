package resourcelifecycle

import (
	"context"
	"database/sql"
	"sync"
)

// ImageUse coordinates local build/publication, restore and collection. It is
// process-local exclusion, not persistent reservations or an artifact registry.
// Collection never waits on a build: it retries on its next ordinary tick.
// All users of one live database share this gate, including backup replay.
type ImageUse struct {
	mu    sync.RWMutex
	check func(context.Context, string) (bool, error)
}

var imageUses sync.Map // *sql.DB -> *ImageUse; one entry per opened database

func Images(db *sql.DB) *ImageUse {
	g, _ := imageUses.LoadOrStore(db, &ImageUse{})
	return g.(*ImageUse)
}

func (g *ImageUse) Use() func() { g.mu.RLock(); return g.mu.RUnlock }
func (g *ImageUse) TryCollect() (func(), bool) {
	if !g.mu.TryLock() {
		return nil, false
	}
	return g.mu.Unlock, true
}

// SetSelectionCheck is startup-only wiring. Unlabelled legacy selections stay
// compatible; a foreign managed selection must be rebuilt in this installation.
func (g *ImageUse) SetSelectionCheck(check func(context.Context, string) (bool, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.check = check
}

// CheckSelection must be called while holding Use or TryCollect.
func (g *ImageUse) CheckSelection(ctx context.Context, ref string) (bool, error) {
	if ref == "" || g.check == nil {
		return true, nil
	}
	return g.check(ctx, ref)
}
