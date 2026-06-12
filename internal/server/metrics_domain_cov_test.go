package server

// Coverage tests for metrics_domain.go: foldStatus normalization, the
// degraded-but-zero-filled rendering when every DB query fails (closed
// DB), and the LLM-provider cardinality cap fold.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestFoldStatus_Normalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want string
	}{
		{"running", "running"},
		{"RUNNING", "running"},
		{"  Queued ", "queued"},
		{"weird-status", "other"},
		{"", "other"},
	}
	for _, tc := range cases {
		if got := foldStatus(assignmentStatusSet, tc.raw); got != tc.want {
			t.Errorf("foldStatus(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestCollectDomainMetrics_ClosedDBDegradesToZeroSeries(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close() // every QueryContext now fails

	s := &Server{db: db, logger: logging.New("error", "json", nil)}
	var b strings.Builder
	s.collectDomainMetrics(context.Background(), &b, "covhost")
	out := b.String()

	// Closed label sets are zero-filled even when the queries fail —
	// dashboards keep a stable series set.
	wantLines := []string{
		`crewshipd_assignments{hostname="covhost",status="pending"} 0`,
		`crewshipd_assignments{hostname="covhost",status="other"} 0`,
		`crewshipd_assignment_queue_depth{hostname="covhost"} 0`,
		`crewshipd_assignment_queue_crews{hostname="covhost"} 0`,
		`crewshipd_assignment_queue_depth_max{hostname="covhost"} 0`,
		`crewshipd_pipeline_runs{hostname="covhost",status="dry_run"} 0`,
		`crewshipd_agent_run_events_total{event="started",hostname="covhost"} 0`,
		`crewshipd_containers_tracked{hostname="covhost"} 0`,
		`crewshipd_containers_reporting{hostname="covhost"} 0`,
		`crewshipd_db_migration_version{hostname="covhost"} 0`,
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("missing zero-filled series %q in degraded output", line)
		}
	}
	// LLM counters have no closed set — but the families must still be
	// declared so absent() alerts can hold.
	if !strings.Contains(out, "# TYPE crewshipd_llm_calls_total counter") {
		t.Error("missing crewshipd_llm_calls_total family header")
	}
}

func TestCollectLLMCostMetrics_FoldsOverflowIntoOther(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_llm','LLM','ws-llm')`)
	// llmProviderSeriesCap + 2 distinct providers, one row each: the
	// two beyond the cap must fold into provider="other" with their
	// calls/cost summed.
	for i := 0; i < llmProviderSeriesCap+2; i++ {
		mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, provider, model, cost_usd)
		                 VALUES (?, 'ws_llm', ?, 'm', 0.5)`,
			fmt.Sprintf("cl_%02d", i), fmt.Sprintf("prov_%02d", i))
	}

	s := &Server{db: db, logger: logging.New("error", "json", nil)}
	var b strings.Builder
	s.collectLLMCostMetrics(context.Background(), &b, "covhost")
	out := b.String()

	if !strings.Contains(out, `crewshipd_llm_calls_total{hostname="covhost",provider="other"} 2`) {
		t.Errorf("overflow providers not folded into other; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_llm_cost_usd_total{hostname="covhost",provider="other"} 1`) {
		t.Errorf("overflow cost not summed into other; output:\n%s", out)
	}
	// Exactly cap+1 call series: cap kept providers + the "other" bucket.
	gotSeries := strings.Count(out, "crewshipd_llm_calls_total{")
	if gotSeries != llmProviderSeriesCap+1 {
		t.Errorf("llm_calls_total series = %d, want %d (cap + other)", gotSeries, llmProviderSeriesCap+1)
	}
}
