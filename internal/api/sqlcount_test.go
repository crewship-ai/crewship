package api

// A statement counter for handler tests whose contract is about COST rather
// than content: "this listing runs a fixed number of statements however many
// rows it returns". Nothing in database/sql exposes that number, so the
// counter sits one layer down, as a driver.Conn wrapper around the real SQLite
// connection, and increments once per statement the pool hands to it.
//
// The wrapper is opened over the same file as the test's migrated database, so
// the handler under test reads and writes the fixture's data through it while
// the fixture's own handle keeps seeding. WAL mode makes the two handles
// ordinary concurrent connections.

import (
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// Keep the handler tests' local vocabulary while sharing real-driver
// instrumentation with the WebSocket query-count tests and benchmarks.
type sqlCounter struct{ *testutil.SQLCounter }

func (c *sqlCounter) reset()     { c.Reset() }
func (c *sqlCounter) count() int { return int(c.Count()) }
func newCountingSQLDB(t *testing.T, path string) (*sql.DB, *sqlCounter) {
	t.Helper()
	db, counter := testutil.CountingSQLDB(t, path, 1)
	return db, &sqlCounter{counter}
}

// newCountingPagesFixture is newPagesFixture with the handler's statements
// counted: the same workspace, OWNER user and crew, plus the second crew the
// two-panel page needs.
func newCountingPagesFixture(t *testing.T) (*PageHandler, *sqlCounter, string, string) {
	t.Helper()
	resetCapabilityCache()
	mdb := testutil.MigratedDB(t)
	drainBackgroundWork(t)
	userID := seedTestUser(t, mdb.DB)
	wsID := seedTestWorkspace(t, mdb.DB, userID)
	for _, crew := range [][2]string{{"crew-lookout", "lookout"}, {"crew-engine", "engine"}} {
		if _, err := mdb.DB.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
			crew[0], wsID, crew[1], crew[1]); err != nil {
			t.Fatalf("insert crew %s: %v", crew[1], err)
		}
	}
	counted, counter := newCountingSQLDB(t, mdb.Path())
	spy := &pagesJournalSpy{}
	clock := &pagesFakeClock{now: time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)}
	h := NewPageHandler(counted, nil, newTestLogger()).SetJournal(spy).SetClockForTesting(clock)
	return h, counter, wsID, userID
}
