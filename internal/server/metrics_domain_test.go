package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedDomainMetricsFixtures inserts a deterministic cross-section of
// domain rows: assignments in every status (queue split across two
// crews), pipeline runs in several statuses, run.* journal events, and
// a small cost ledger. Returns the applied migration version so the
// test doesn't hard-code it.
func seedDomainMetricsFixtures(t *testing.T, s *Server) int {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	exec(`INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','C1','crew-1',?,?)`, now, now)
	exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c2','w1','C2','crew-2',?,?)`, now, now)
	exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag0','w1','Dispatcher','dispatcher')`)
	exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1','c1','w1','One','one')`)
	exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2','c2','w1','Two','two')`)
	exec(`INSERT INTO chats (id, agent_id, workspace_id) VALUES ('ch1','ag0','w1')`)

	// Assignments: 1 PENDING, 3 QUEUED (2 in crew c1, 1 in crew c2),
	// 2 RUNNING, 1 COMPLETED, 1 FAILED, 1 CANCELLED.
	insAssignment := func(id, toAgent, status string, queued bool) {
		queuedAt := any(nil)
		if queued {
			queuedAt = now
		}
		exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, queued_at)
		      VALUES (?,'w1','ch1','ag0',?,?,?,?)`, id, toAgent, "task "+id, status, queuedAt)
	}
	insAssignment("as-p1", "ag1", "PENDING", false)
	insAssignment("as-q1", "ag1", "QUEUED", true)
	insAssignment("as-q2", "ag1", "QUEUED", true)
	insAssignment("as-q3", "ag2", "QUEUED", true)
	insAssignment("as-r1", "ag1", "RUNNING", false)
	insAssignment("as-r2", "ag1", "RUNNING", false)
	insAssignment("as-c1", "ag1", "COMPLETED", false)
	insAssignment("as-f1", "ag1", "FAILED", false)
	insAssignment("as-x1", "ag1", "CANCELLED", false)

	// Pipeline runs: 1 queued, 2 running, 1 completed, 1 failed, 1 interrupted.
	exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
	      VALUES ('p1','w1','pipe-1','Pipe 1','{}','h1')`)
	insRun := func(id, status string) {
		exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at)
		      VALUES (?,'w1','p1','pipe-1',?,?)`, id, status, now)
	}
	insRun("prn-q1", "queued")
	insRun("prn-r1", "running")
	insRun("prn-r2", "running")
	insRun("prn-c1", "completed")
	insRun("prn-f1", "failed")
	insRun("prn-i1", "interrupted")

	// Agent run lifecycle events in the unified journal: 4 started,
	// 2 completed, 1 failed, 1 cancelled, 0 timeout.
	jid := 0
	insEvent := func(entryType string) {
		jid++
		exec(`INSERT INTO journal_entries (id, workspace_id, entry_type, actor_type, summary)
		      VALUES (?,'w1',?,'orchestrator',?)`, fmt.Sprintf("je-%d", jid), entryType, entryType)
	}
	for i := 0; i < 4; i++ {
		insEvent("run.started")
	}
	insEvent("run.completed")
	insEvent("run.completed")
	insEvent("run.failed")
	insEvent("run.cancelled")

	// Paymaster cost ledger: 2 ANTHROPIC calls totalling $0.75, 1 OPENAI call at $1.
	exec(`INSERT INTO cost_ledger (id, workspace_id, provider, model, cost_usd) VALUES ('cl1','w1','ANTHROPIC','claude-x',0.5)`)
	exec(`INSERT INTO cost_ledger (id, workspace_id, provider, model, cost_usd) VALUES ('cl2','w1','ANTHROPIC','claude-x',0.25)`)
	exec(`INSERT INTO cost_ledger (id, workspace_id, provider, model, cost_usd) VALUES ('cl3','w1','OPENAI','gpt-x',1.0)`)

	var version int
	require.NoError(t, s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM _migrations`).Scan(&version))
	require.Greater(t, version, 0)
	return version
}

func scrapeMetrics(t *testing.T, s *Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// TestHandleMetrics_DomainSeries pins the W10 contract: /metrics
// exposes the operator-facing domain series (assignments, queue depth,
// pipeline runs, run events, LLM cost, containers, migration version)
// with correct values from a seeded store, in the same hand-rolled
// Prometheus text format as the process gauges.
func TestHandleMetrics_DomainSeries(t *testing.T) {
	s := newTestServerWithDeps(t)
	migrationVersion := seedDomainMetricsFixtures(t, s)

	// Two tracked crew containers, one of which has a live stats sample.
	s.statsCollector.Register("cid1", "c1", "w1")
	s.statsCollector.Register("cid2", "c2", "w1")
	s.statsCollector.latestMu.Lock()
	s.statsCollector.latest["cid1"] = &provider.ContainerMetrics{CPUPercent: 1, Timestamp: time.Now()}
	s.statsCollector.latestMu.Unlock()

	body := scrapeMetrics(t, s)
	hostname, _ := os.Hostname()
	line := func(format string, args ...any) string {
		return fmt.Sprintf(format, args...)
	}

	// The pre-existing process gauges must survive untouched.
	assert.Contains(t, body, "crewshipd_uptime_seconds")
	assert.Contains(t, body, "crewshipd_ws_connections")

	// Assignments by status (bounded label set, zero-filled).
	for status, want := range map[string]int{
		"pending": 1, "queued": 3, "running": 2,
		"completed": 1, "failed": 1, "cancelled": 1, "other": 0,
	} {
		assert.Contains(t, body,
			line("crewshipd_assignments{hostname=%q,status=%q} %d", hostname, status, want))
	}

	// Queue depth is aggregated, never per-crew labels.
	assert.Contains(t, body, line("crewshipd_assignment_queue_depth{hostname=%q} 3", hostname))
	assert.Contains(t, body, line("crewshipd_assignment_queue_crews{hostname=%q} 2", hostname))
	assert.Contains(t, body, line("crewshipd_assignment_queue_depth_max{hostname=%q} 2", hostname))
	assert.NotContains(t, body, `crew_id=`, "queue metrics must not emit per-crew labels")

	// Pipeline runs by status.
	for status, want := range map[string]int{
		"queued": 1, "running": 2, "completed": 1, "failed": 1,
		"cancelled": 0, "dry_run": 0, "interrupted": 1, "other": 0,
	} {
		assert.Contains(t, body,
			line("crewshipd_pipeline_runs{hostname=%q,status=%q} %d", hostname, status, want))
	}

	// Agent run lifecycle counters from the unified journal.
	for event, want := range map[string]int{
		"started": 4, "completed": 2, "failed": 1, "cancelled": 1, "timeout": 0,
	} {
		assert.Contains(t, body,
			line("crewshipd_agent_run_events_total{event=%q,hostname=%q} %d", event, hostname, want))
	}

	// Paymaster LLM cost counters, per provider.
	assert.Contains(t, body, line("crewshipd_llm_calls_total{hostname=%q,provider=\"ANTHROPIC\"} 2", hostname))
	assert.Contains(t, body, line("crewshipd_llm_calls_total{hostname=%q,provider=\"OPENAI\"} 1", hostname))
	assert.Contains(t, body, line("crewshipd_llm_cost_usd_total{hostname=%q,provider=\"ANTHROPIC\"} 0.75", hostname))
	assert.Contains(t, body, line("crewshipd_llm_cost_usd_total{hostname=%q,provider=\"OPENAI\"} 1", hostname))

	// Container health: tracked vs actually reporting stats.
	assert.Contains(t, body, line("crewshipd_containers_tracked{hostname=%q} 2", hostname))
	assert.Contains(t, body, line("crewshipd_containers_reporting{hostname=%q} 1", hostname))

	// DB migration version.
	assert.Contains(t, body,
		line("crewshipd_db_migration_version{hostname=%q} %d", hostname, migrationVersion))
}

// TestHandleMetrics_DomainSeriesCached pins the short-TTL cache: the
// DB-derived block is recomputed at most once per TTL window so a
// tight scrape loop (or a scraper retry storm) cannot turn /metrics
// into a query amplifier.
func TestHandleMetrics_DomainSeriesCached(t *testing.T) {
	s := newTestServerWithDeps(t)
	seedDomainMetricsFixtures(t, s)
	hostname, _ := os.Hostname()

	body := scrapeMetrics(t, s)
	assert.Contains(t, body, fmt.Sprintf("crewshipd_assignment_queue_depth{hostname=%q} 3", hostname))

	// New queued row within the TTL window: the cached value must win.
	_, err := s.db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, queued_at)
	                     VALUES ('as-q4','w1','ch1','ag0','ag1','late','QUEUED',datetime('now'))`)
	require.NoError(t, err)

	body = scrapeMetrics(t, s)
	assert.Contains(t, body, fmt.Sprintf("crewshipd_assignment_queue_depth{hostname=%q} 3", hostname),
		"second scrape inside the TTL must serve the cached snapshot")

	// Expire the cache: the fresh row becomes visible.
	s.domainMetrics.mu.Lock()
	s.domainMetrics.at = time.Time{}
	s.domainMetrics.mu.Unlock()

	body = scrapeMetrics(t, s)
	assert.Contains(t, body, fmt.Sprintf("crewshipd_assignment_queue_depth{hostname=%q} 4", hostname))
}

// TestHandleMetrics_DomainSeriesEmptyStore guards the cold-boot shape:
// a migrated-but-empty store still emits every series (zero-filled), so
// dashboards and absent() alerts have a stable series set from minute one.
func TestHandleMetrics_DomainSeriesEmptyStore(t *testing.T) {
	s := newTestServerWithDeps(t)
	body := scrapeMetrics(t, s)
	hostname, _ := os.Hostname()

	assert.Contains(t, body, fmt.Sprintf("crewshipd_assignments{hostname=%q,status=\"queued\"} 0", hostname))
	assert.Contains(t, body, fmt.Sprintf("crewshipd_assignment_queue_depth{hostname=%q} 0", hostname))
	assert.Contains(t, body, fmt.Sprintf("crewshipd_pipeline_runs{hostname=%q,status=\"running\"} 0", hostname))
	assert.Contains(t, body, fmt.Sprintf("crewshipd_agent_run_events_total{event=\"failed\",hostname=%q} 0", hostname))
	assert.Contains(t, body, fmt.Sprintf("crewshipd_containers_tracked{hostname=%q} 0", hostname))
	assert.Contains(t, body, fmt.Sprintf("crewshipd_containers_reporting{hostname=%q} 0", hostname))
	// No cost rows → no provider series, but the HELP/TYPE header still appears.
	assert.Contains(t, body, "# TYPE crewshipd_llm_cost_usd_total counter")
}
