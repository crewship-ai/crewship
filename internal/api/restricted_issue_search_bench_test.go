package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// Synthetic authenticated handler benchmark; setup/migration/insertion is untimed.
// Run with -benchtime=20x. The limiter clock advances to measure admitted SQL,
// not fast 429 responses; auth and handler work remain inside the timing.
func BenchmarkSharedIssueSearch(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000} {
		for _, projects := range []int{1, 10, 32, 256} {
			for _, hidden := range []int{0, 100000} {
				b.Run(fmt.Sprintf("issues%d/projects%d/hidden%d", n, projects, hidden), func(b *testing.B) {
					fixture := testutil.MigratedDB(b)
					token := seedSearchBenchmark(b, fixture.DB, n, projects, hidden)
					trace := &searchTraceConnector{path: fixture.Path()}
					db := sql.OpenDB(trace)
					b.Cleanup(func() { db.Close() })
					router, err := NewRouter(db, "synthetic-search-benchmark-secret", newTestLogger())
					if err != nil {
						b.Fatal(err)
					}
					b.Cleanup(router.Shutdown)
					clock := time.Now()
					router.sharedSearch.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
					queries := []string{"q=absent", "q=common", "q=ISSUE-42", "q=" + url.QueryEscape(strings.Repeat("Ž", 256)), "", "q=common&limit=1", "q=common&limit=500", "q=common&offset=1000000"}
					for qi, q := range queries {
						for _, explicit := range []bool{false, true} {
							for _, clients := range []int{1, 4} {
								for _, fresh := range []bool{false, true} {
									b.Run(fmt.Sprintf("q%d/project%t/clients%d/fresh%t", qi, explicit, clients, fresh), func(b *testing.B) {
										if fresh {
											db.SetMaxIdleConns(0)
										} else {
											db.SetMaxIdleConns(16)
										}
										if explicit {
											q += "&project_id=p0"
										}
										path := "/api/v1/workspaces/search-bench-ws/restricted-issues?" + q
										trace.queries.Store(0)
										trace.execs.Store(0)
										trace.issues.Store(0)
										samples := make([]time.Duration, 0, b.N*clients)
										var mu sync.Mutex
										var rowCount atomic.Int64
										b.ResetTimer()
										for i := 0; i < b.N; i++ {
											var wg sync.WaitGroup
											for c := 0; c < clients; c++ {
												wg.Add(1)
												go func() {
													defer wg.Done()
													req := httptest.NewRequest("GET", path, nil)
													req.Header.Set("Authorization", "Bearer "+token)
													req.Header.Set("X-Workspace-ID", "search-bench-ws")
													rr := httptest.NewRecorder()
													start := time.Now()
													router.ServeHTTP(rr, req)
													elapsed := time.Since(start)
													if rr.Code != 200 {
														b.Errorf("status%d: %s", rr.Code, rr.Body.String())
														return
													}
													var items []json.RawMessage
													if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
														b.Error(err)
													}
													rowCount.Add(int64(len(items)))
													mu.Lock()
													samples = append(samples, elapsed)
													mu.Unlock()
												}()
											}
											wg.Wait()
										}
										b.StopTimer()
										sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
										if len(samples) > 0 {
											b.ReportMetric(float64(samples[len(samples)/2].Microseconds())/1000, "median-ms")
											b.ReportMetric(float64(samples[(len(samples)-1)*95/100].Microseconds())/1000, "p95-ms")
											b.ReportMetric(float64(samples[len(samples)-1].Microseconds())/1000, "max-ms")
										}
										b.ReportMetric(float64(rowCount.Load())/float64(b.N*clients), "rows/req")
										b.ReportMetric(float64(trace.issues.Load())/float64(b.N*clients), "issue-queries/req")
										b.ReportMetric(float64(trace.queries.Load()+trace.execs.Load())/float64(b.N*clients), "SQL/req")
									})
								}
							}
						}
					}
				})
			}
		}
	}
}

func seedSearchBenchmark(b testing.TB, db *sql.DB, n, projects, hidden int) string {
	b.Helper()
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		if _, err := tx.Exec(q, args...); err != nil {
			b.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES('search-bench-owner','owner@example.test'),('search-bench-user','guest@example.test')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('search-bench-ws','Benchmark','search-bench-ws')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('search-bench-admin','search-bench-ws','search-bench-owner','OWNER','trusted'),('search-bench-member','search-bench-ws','search-bench-user','VIEWER','restricted')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('search-bench-crew','search-bench-ws','Crew','bench')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('search-bench-agent','search-bench-ws','search-bench-crew','Worker','worker','AGENT','IDLE','CLAUDE_CODE','CODING',1800,0)`)
	for i := 0; i <= projects; i++ {
		id := fmt.Sprintf("p%d", i)
		exec(`INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,'search-bench-ws',?,?)`, id, id, id)
		if i < projects {
			exec(`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES(?,'search-bench-member','project',?,'list','search-bench-owner','2026-10-16T00:00:00Z')`, id, id)
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,identifier,title,status,mission_type,project_id,created_at) VALUES(?,'search-bench-ws','search-bench-crew','search-bench-agent',?,?,'Common synthetic title','TODO','issue',?,'2026-10-16T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < n+hidden; i++ {
		project := i % projects
		if i >= n {
			project = projects
		}
		id := fmt.Sprintf("issue-%d", i)
		if _, err := stmt.Exec(id, id, fmt.Sprintf("ISSUE-%d", i), fmt.Sprintf("p%d", project)); err != nil {
			b.Fatal(err)
		}
	}
	token := "crewship_cli_" + strings.Repeat("b", 48)
	exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('search-bench-token','search-bench-user','Benchmark',?)`, sha256Hex(token))
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return token
}
