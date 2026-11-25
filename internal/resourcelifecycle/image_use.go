package resourcelifecycle

import (
	"context"
	"database/sql"
	"sync"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// ImageUse coordinates local build/publication, restore and collection. It is
// process-local exclusion, not persistent reservations or an artifact registry.
// Collection never waits on a build: it retries on its next ordinary tick.
// All users of one live database share this gate, including backup replay.
type ImageUse struct {
	protectorMu sync.RWMutex
	protector   ImageReferenceProtector
	mu          sync.RWMutex
	refsMu      sync.Mutex
	active      map[string]int
	deleting    map[string]chan struct{}
	allImages   chan struct{}
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

// UseImages protects only this build's base and output cache aliases. Other
// crews keep acquiring images while an unrelated image is being removed.
func (g *ImageUse) UseImages(ctx context.Context, refs ...string) (func(), error) {
	refs = canonicalLeaseKeys(refs)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.refsMu.Lock()
		wait := g.allImages
		for _, ref := range refs {
			if ch := g.deleting[ref]; ch != nil {
				wait = ch
				break
			}
		}
		if wait != nil {
			g.refsMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if g.active == nil {
			g.active = map[string]int{}
		}
		for _, ref := range refs {
			if ref != "" {
				g.active[ref]++
			}
		}
		g.refsMu.Unlock()
		var once sync.Once
		return func() {
			once.Do(func() {
				g.refsMu.Lock()
				defer g.refsMu.Unlock()
				for _, ref := range refs {
					if ref != "" {
						g.active[ref]--
						if g.active[ref] == 0 {
							delete(g.active, ref)
						}
					}
				}
			})
		}, nil
	}
}

// Equivalent Docker Hub spellings must reserve the same acquisition key.
func canonicalLeaseKeys(refs []string) []string {
	keys := make([]string, len(refs))
	for i, key := range refs {
		keys[i] = devcontainer.CanonicalCacheRef(key)
	}
	return keys
}

// TryCollectAllImages reserves every image key during a missing-image check.
// Without an image inventory record we cannot enumerate its former aliases.
func (g *ImageUse) TryCollectAllImages() (func(), bool) {
	g.refsMu.Lock()
	defer g.refsMu.Unlock()
	if g.allImages != nil || len(g.active) != 0 || len(g.deleting) != 0 {
		return nil, false
	}
	g.allImages = make(chan struct{})
	return sync.OnceFunc(func() {
		g.refsMu.Lock()
		defer g.refsMu.Unlock()
		close(g.allImages)
		g.allImages = nil
	}), true
}

func (g *ImageUse) TryCollectImages(refs ...string) (func(), bool) {
	refs = canonicalLeaseKeys(refs)
	g.refsMu.Lock()
	defer g.refsMu.Unlock()
	if g.allImages != nil {
		return nil, false
	}
	for _, ref := range refs {
		if g.active[ref] > 0 || g.deleting[ref] != nil {
			return nil, false
		}
	}
	if g.deleting == nil {
		g.deleting = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	for _, ref := range refs {
		if ref != "" {
			g.deleting[ref] = done
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.refsMu.Lock()
			defer g.refsMu.Unlock()
			for _, ref := range refs {
				if g.deleting[ref] == done {
					delete(g.deleting, ref)
				}
			}
			close(done)
		})
	}, true
}
