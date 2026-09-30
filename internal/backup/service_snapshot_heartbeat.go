package backup

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

// serviceFenceKeeper only renews the exact opaque tokens this capture owns.
// Losing any token cancels work and retains maintenance; it never auto-resumes.
type serviceFenceKeeper struct {
	db     *sql.DB
	mu     sync.Mutex
	fences []serviceBackupFence
}

func (k *serviceFenceKeeper) add(f serviceBackupFence) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.fences = append(k.fences, f)
}
func (k *serviceFenceKeeper) run(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			k.mu.Lock()
			fences := append([]serviceBackupFence(nil), k.fences...)
			k.mu.Unlock()
			for _, fence := range fences {
				renewCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err := servicelifecycle.RenewBackupFence(renewCtx, k.db, fence.crew, fence.token)
				stop()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					cancel()
					return
				}
			}
		}
	}
}
