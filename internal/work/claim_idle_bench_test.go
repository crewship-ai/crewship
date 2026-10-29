package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func idlePollFixture(b *testing.B, futureRows, liveRows, terminalRows int, runnable bool) (*Store, *sql.DB, string) {
	b.Helper()
	template := testutil.MigratedDB(b)
	path := template.Path()
	if err := template.Close(); err != nil {
		b.Fatal(err)
	}
	// MigratedDB deliberately uses NORMAL. Reopen this owned copy with the
	// production default instead of silently reporting test durability settings.
	db, err := database.Open("file:" + path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	var synchronous int
	var journal string
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		b.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		b.Fatal(err)
	}
	if synchronous != 2 || journal != "wal" {
		b.Fatalf("fixture pragmas: sync=%d journal=%s", synchronous, journal)
	}
	b.Logf("production fixture: synchronous=%d journal=%s GOMAXPROCS=%d", synchronous, journal, runtime.GOMAXPROCS(0))
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := NewStore(db.DB).WithClock(func() time.Time { return now })
	tx, err := db.BeginTx(b.Context(), nil)
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO work_items
  (id,workspace_id,source,class,state,agent_id,input_json,eligible_at,created_at,updated_at)
  VALUES (?,'owned-idle-poll-fixture','manual','background',?,?,'{}',?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()
	insert := func(id, state, agent string, eligible time.Time) {
		if _, err := stmt.Exec(id, state, agent, tsformat.Format(eligible), tsformat.Format(now), tsformat.Format(now)); err != nil {
			b.Fatal(err)
		}
	}
	// Synthetic schema-valid work rows in the full production-migrated database;
	// fixture construction is excluded from timing, not a production traffic sample.
	for i := 0; i < futureRows; i++ {
		insert(fmt.Sprintf("future-%06d", i), "queued", "future-agent", now.Add(time.Hour))
	}
	for i := 0; i < liveRows; i++ {
		insert(fmt.Sprintf("live-%02d", i), "running", fmt.Sprintf("live-agent-%02d", i), now)
	}

	for i := 0; i < terminalRows; i++ {
		insert(fmt.Sprintf("terminal-%06d", i), "succeeded", "terminal-agent", now.Add(-time.Hour))
	}
	if terminalRows != 0 {
		if _, err := tx.Exec(`UPDATE work_items SET deadline_at=?,terminal_at=? WHERE state='succeeded'`, tsformat.Format(now.Add(-time.Hour)), tsformat.Format(now)); err != nil {
			b.Fatal(err)
		}
	}
	if runnable {
		insert("ready-row", "queued", "ready-agent", now)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return s, db.DB, db.Path()
}

func BenchmarkClaimIdlePoll(b *testing.B) {
	fixtures := []struct {
		name                   string
		future, live, terminal int
		runnable               bool
	}{
		{name: "empty"},
		{name: "one-future", future: 1},
		{name: "many-future", future: 10000},
		{name: "old-terminal-deadlines", terminal: 10000},
		{name: "saturated-with-ready", live: 8, runnable: true},
		{name: "saturated-ready-old-deadlines", live: 8, runnable: true, terminal: 10000},
	}
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			for _, reference := range []bool{true, false} {
				name := "candidate"
				if reference {
					name = "reference"
				}
				b.Run(name, func(b *testing.B) {
					s, _, _ := idlePollFixture(b, f.future, f.live, f.terminal, f.runnable)
					claim := s.Claim
					if reference {
						claim = s.referenceClaimBeforeIdlePrecheck
					}

					rows, err := s.db.Query("EXPLAIN QUERY PLAN "+claimPendingQuery, tsformat.Format(s.now().UTC()), tsformat.Format(s.now().UTC()))
					if err != nil {
						b.Fatal(err)
					}
					for rows.Next() {
						var id, parent, unused int
						var detail string
						if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
							rows.Close()
							b.Fatal(err)
						}
						b.Logf("hint plan: %s", detail)
					}
					if err := rows.Err(); err != nil {
						rows.Close()
						b.Fatal(err)
					}
					if err := rows.Close(); err != nil {
						b.Fatal(err)
					}
					opts := ClaimOptions{LeaseOwner: "poll-probe"}
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						got, err := claim(b.Context(), opts)
						if got != nil || !errors.Is(err, ErrNoWork) {
							b.Fatalf("fixture changed: %#v %v", got, err)
						}
					}
				})
			}
		})
	}
}

// Each timed wave performs one changing-row FULL/WAL writer transaction and
// exactly one empty Claim per contender. The wave joins every contender before
// starting the next one, so faster Claims cannot create an unbounded extra load.
// ns/op and allocation counts describe the entire wave; writer-ns/op measures
// just its writer transaction. This is synthetic admission contention, not a
// production polling cadence or a crash-durability experiment.
func BenchmarkClaimIdleWriterAdmission(b *testing.B) {
	for _, pollers := range []int{0, 1, 4, 16} {
		b.Run(fmt.Sprintf("pollers%d", pollers), func(b *testing.B) {
			for _, reference := range []bool{true, false} {
				name := "candidate"
				if reference {
					name = "reference"
				}
				b.Run(name, func(b *testing.B) {
					s, db, path := idlePollFixture(b, 0, 1, 0, false)
					writer, err := database.Open("file:" + path)
					if err != nil {
						b.Fatal(err)
					}
					writer.SetMaxOpenConns(1)
					defer writer.Close()
					claim := s.Claim
					if reference {
						claim = s.referenceClaimBeforeIdlePrecheck
					}
					ctx, cancel := context.WithCancel(b.Context())
					var workers sync.WaitGroup
					ready := make(chan struct{}, pollers)
					completed := make(chan error, pollers)
					starts := make([]chan struct{}, pollers)
					for i := range starts {
						starts[i] = make(chan struct{}, 1)
						workers.Add(1)
						go func(start <-chan struct{}) {
							defer workers.Done()
							ready <- struct{}{}
							for {
								select {
								case <-ctx.Done():
									return
								case <-start:
									got, err := claim(ctx, ClaimOptions{LeaseOwner: "bounded-idle-contender"})
									if got != nil || !errors.Is(err, ErrNoWork) {
										if err == nil {
											err = fmt.Errorf("unexpected claim: %#v", got)
										}
										completed <- err
									} else {
										completed <- nil
									}
								}
							}
						}(starts[i])
					}
					defer func() { cancel(); workers.Wait() }()
					for range pollers {
						<-ready
					}
					var writerDuration time.Duration
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						for _, start := range starts {
							start <- struct{}{}
						}
						began := time.Now()
						tx, err := writer.BeginTx(b.Context(), nil)
						if err != nil {
							b.Fatal(err)
						}
						if _, err := tx.ExecContext(b.Context(), `UPDATE work_items SET priority=priority+1 WHERE id='live-00'`); err != nil {
							tx.Rollback()
							b.Fatal(err)
						}
						if err := tx.Commit(); err != nil {
							b.Fatal(err)
						}
						writerDuration += time.Since(began)
						for range pollers {
							if err := <-completed; err != nil {
								b.Fatal(err)
							}
						}
					}
					b.StopTimer()
					cancel()
					workers.Wait()
					b.ReportMetric(float64(writerDuration.Nanoseconds())/float64(b.N), "writer-ns/op")
					b.ReportMetric(float64(pollers), "polls/write")
					var attempts int
					if err := db.QueryRow(`SELECT COUNT(*) FROM work_attempts`).Scan(&attempts); err != nil {
						b.Fatal(err)
					}
					if attempts != 0 {
						b.Fatalf("started %d attempts", attempts)
					}
				})
			}
		})
	}
}
