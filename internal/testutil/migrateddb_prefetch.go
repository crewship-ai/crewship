package testutil

// migrateddb_prefetch.go takes the remaining per-test cost of MigratedDB off
// the test's critical path, and lets a re-executed test binary reuse its
// parent's template instead of migrating a second one.
//
// Where the per-test cost is
// --------------------------
// Copying the template is ~1.5 ms. Opening the copy is not: SQLite parses the
// entire schema (every CREATE TABLE/INDEX/TRIGGER in sqlite_master) the first
// time a connection touches the file, and database.Open pings, so that parse
// happens inside every MigratedDB call. modernc.org/sqlite is transpiled C, so
// under -race every array access in its parser is instrumented; measured on
// crewship-dev, copy+open+close costs ~0.2 s per call under -race, which is
// the median test time of the whole internal/api package (~0.27 s). A CPU
// profile of 520 internal/api tests put 78% of the test goroutines' CPU in
// setupTestDB, and two thirds of that in this one parse.
//
// The parse cannot be skipped — a connection that has not read the schema
// cannot run a query — but it does not have to happen while the test waits.
// PrefetchMigratedDBs opens copies ahead of demand on otherwise idle cores
// (CI runners have four; a package's tests mostly run one at a time), and
// MigratedDB hands one out if it is ready.
//
// Why this is not a semantic change
// ---------------------------------
// A prefetched database is produced by exactly the same code as the
// synchronous path (openMigratedCopy: template copy, database.Open with the
// same options, the same ping), into its own fresh directory, and it is handed
// to exactly one test, which registers exactly the same cleanups. The only
// difference is the moment the open happened. Nothing is shared between tests
// and no connection outlives the test that received it.
//
// If no prefetched copy is ready, MigratedDB opens one synchronously, as it
// always did — it never waits for a worker — so a test is never slower than
// before, and a failing open still fails the test that asked for it with the
// same error.
//
// Prefetching is opt-in per package (a TestMain call), because a package with a
// handful of DB tests would mostly pay for copies nobody takes.

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/crewship-ai/crewship/internal/database"
)

type prefetchedItem struct {
	db   *database.DB
	dir  string
	path string
}

// prefetcher is one enablement's state. A fresh one per PrefetchMigratedDBs
// call keeps enable/stop cycles (the helper's own tests do several) from
// sharing a sync.Once or a closed channel.
type prefetcher struct {
	workers int
	start   sync.Once
	ready   chan prefetchedItem
	stop    chan struct{}
	wg      sync.WaitGroup
	// mu orders worker start against shutdown: once closed is set no worker
	// may be added, so shutdown's Wait cannot run before a late Add.
	mu     sync.Mutex
	closed bool
}

var (
	prefetchMu     sync.Mutex
	activePrefetch *prefetcher
)

// PrefetchMigratedDBs enables background preparation of MigratedDB copies for
// the rest of the test process and returns the function that stops it and
// removes the copies no test took. Call it from TestMain:
//
//	stop := testutil.PrefetchMigratedDBs(2, 4)
//	code := m.Run()
//	stop()
//	os.Exit(code)
//
// workers is the number of copies opened concurrently, depth how many may wait
// ready. Workers start on the first MigratedDB call, after the template exists,
// so a run that touches no database pays nothing. With GOMAXPROCS(0) == 1 there
// is no idle core to overlap with, and prefetching stays off.
func PrefetchMigratedDBs(workers, depth int) (stop func()) {
	if workers < 1 || depth < 1 || runtime.GOMAXPROCS(0) < 2 {
		return func() {}
	}
	p := &prefetcher{
		workers: workers,
		ready:   make(chan prefetchedItem, depth),
		stop:    make(chan struct{}),
	}
	prefetchMu.Lock()
	defer prefetchMu.Unlock()
	if activePrefetch != nil {
		panic("testutil: PrefetchMigratedDBs is already enabled")
	}
	activePrefetch = p
	var once sync.Once
	return func() { once.Do(p.shutdown) }
}

// shutdown disables p, waits for its workers and discards every copy no test
// took. Copies already handed out belong to their tests' cleanups.
func (p *prefetcher) shutdown() {
	prefetchMu.Lock()
	if activePrefetch == p {
		activePrefetch = nil
	}
	prefetchMu.Unlock()

	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	close(p.stop)
	p.wg.Wait()
	for {
		select {
		case it := <-p.ready:
			discardPrefetched(it)
		default:
			return
		}
	}
}

// takePrefetched returns a ready copy if one is waiting. It never blocks.
func takePrefetched() (prefetchedItem, bool) {
	prefetchMu.Lock()
	p := activePrefetch
	prefetchMu.Unlock()
	if p == nil {
		return prefetchedItem{}, false
	}
	p.start.Do(p.startWorkers)
	select {
	case it := <-p.ready:
		return it, true
	default:
		return prefetchedItem{}, false
	}
}

func (p *prefetcher) startWorkers() {
	// The template is built (or inherited) here, in the calling test, exactly
	// as the synchronous path would build it. If that fails, no worker starts
	// and every caller falls through to the synchronous path, which reports
	// the error against the test that asked.
	if _, err := templatePath(); err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.work()
	}
}

func (p *prefetcher) work() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		it, err := openPrefetched()
		if err != nil {
			// Stop producing; the synchronous path will hit the same error and
			// attribute it to a test.
			return
		}
		select {
		case p.ready <- it:
		case <-p.stop:
			discardPrefetched(it)
			return
		}
	}
}

func openPrefetched() (prefetchedItem, error) {
	dir, err := newMigratedTestDir()
	if err != nil {
		return prefetchedItem{}, err
	}
	path := filepath.Join(dir, "test.db")
	db, err := openMigratedCopy(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return prefetchedItem{}, err
	}
	return prefetchedItem{db: db, dir: dir, path: path}, nil
}

func discardPrefetched(it prefetchedItem) {
	quiesce(it.db, it.path)
	_ = os.RemoveAll(it.dir)
}
