package pipeline

import (
	"database/sql"
	"sync"
)

// One process owns each SQLite queue. Coalesced pulses avoid blocking writers
// while allowing independently constructed producer stores to wake its loop.
var pendingWakeups sync.Map

func pendingWakeChannel(db *sql.DB) chan struct{} {
	channel, _ := pendingWakeups.LoadOrStore(db, make(chan struct{}, 1))
	return channel.(chan struct{})
}

// NotifyPendingRuns must be called after committing an enqueue transaction.
func NotifyPendingRuns(db *sql.DB) {
	select {
	case pendingWakeChannel(db) <- struct{}{}:
	default:
	}
}

func (s *PendingRunStore) notify() {
	if db, ok := s.db.(*sql.DB); ok {
		NotifyPendingRuns(db)
	}
}
