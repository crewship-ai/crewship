package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

// Exercise the actual router startup wrapper against migrated SQLite. The
// driver barrier parks ResumePending while it owns a connection, before its
// SELECT is prepared. No poller or authorization verdict is substituted.
func TestProviderLoginStartupShutdownWaitsForRecoveryQuery(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "released startup query"
		if held {
			name = "held startup query"
		}
		t.Run(name, func(t *testing.T) {
			base := setupTestDB(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseQuery := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseQuery()
			hook := &reviewQueryHook{
				match: "FROM provider_device_logins WHERE status = 'pending' ORDER BY created_at",
				nth:   1,
				run: func() {
					close(entered)
					<-release
				},
			}
			reader := reviewHookedDB(t, reviewDBPath(t, base), hook)
			// One connection belongs to the parked startup SELECT; the other
			// lets synchronous router construction finish without a deadlock.
			reader.SetMaxOpenConns(2)
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				t.Fatal(err)
			}
			router, err := NewRouter(reader, hex.EncodeToString(key), newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual router startup did not reach the recovery SELECT")
			}
			if !hook.didFire() || reader.Stats().InUse == 0 {
				t.Fatal("recovery query did not own a real SQLite connection")
			}
			if !held {
				releaseQuery()
			}
			stopped := make(chan struct{})
			go func() {
				router.Shutdown()
				close(stopped)
			}()
			select {
			case <-router.providerLogins.ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("router shutdown did not reach provider-login cancellation")
			}
			if held {
				// Cancellation has happened, but the real startup query is
				// still parked. This bounded negative-completion assertion is
				// not a sleep/retry or evidence that cancellation ended work.
				select {
				case <-stopped:
					t.Error("router Shutdown returned while startup recovery still owned a SQLite connection")
				case <-time.After(100 * time.Millisecond):
				}
				releaseQuery()
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("router shutdown did not finish after the recovery query was released")
			}
		})
	}
}
