package devcontainer

import (
	"context"
	"path/filepath"
	"sync"
)

// All downloader instances in a server share cache paths. A cold resolution
// owns its path until metadata/provenance have been read, so a slower pull
// cannot replace files already returned to a build. Unrelated paths proceed
// independently; unused entries are removed rather than retained indefinitely.
var featureCacheLocks = struct {
	sync.Mutex
	entries map[string]*featureCacheLock
}{entries: make(map[string]*featureCacheLock)}

type featureCacheLock struct {
	token chan struct{}
	users int
}

func lockFeatureCache(ctx context.Context, path string) (func(), error) {
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	featureCacheLocks.Lock()
	entry := featureCacheLocks.entries[key]
	if entry == nil {
		entry = &featureCacheLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		featureCacheLocks.entries[key] = entry
	}
	entry.users++
	featureCacheLocks.Unlock()
	releaseUser := func() {
		featureCacheLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(featureCacheLocks.entries, key)
		}
		featureCacheLocks.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseUser()
		return nil, ctx.Err()
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			releaseUser()
			return nil, err
		}
		return func() { entry.token <- struct{}{}; releaseUser() }, nil
	}
}
