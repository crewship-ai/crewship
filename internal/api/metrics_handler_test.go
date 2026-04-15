package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// newMetricsHandlerForTest wires up a MetricsHandler against a fresh test DB
// and returns the handler plus a preseeded workspace id. The returned DB is
// kept alive by t.Cleanup inside setupTestDB, so tests don't need to close it.
func newMetricsHandlerForTest(t *testing.T) (*MetricsHandler, *sql.DB, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewMetricsHandler(db, logger), db, wsID
}

// doTimeseries issues a GET /api/v1/metrics/timeseries request with the given
// query string and workspace context injected, returning the recorder.
func doTimeseries(t *testing.T, h *MetricsHandler, wsID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/timeseries?"+query, nil)
	ctx := withUser(req.Context(), &AuthUser{ID: "test-user-id"})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Timeseries(rr, req)
	return rr
}

func TestMetricsTimeseries_ParamValidation(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)

	cases := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"missing metric", "window=24h&bucket=1h", http.StatusBadRequest},
		{"invalid metric", "metric=bogus&window=24h&bucket=1h", http.StatusBadRequest},
		{"invalid window", "metric=issues_closed&window=42h&bucket=1h", http.StatusBadRequest},
		{"invalid bucket", "metric=issues_closed&window=24h&bucket=5m", http.StatusBadRequest},
		{"invalid group_by", "metric=issues_closed&window=24h&bucket=1h&group_by=agent", http.StatusBadRequest},
		// 1d bucket with 24h window collapses to one bucket: rejected.
		{"bucket too large for window", "metric=issues_closed&window=24h&bucket=1d", http.StatusBadRequest},
		// 30d window with 15m bucket = 2880 buckets, over the 200-bucket cap.
		{"too many buckets", "metric=issues_closed&window=30d&bucket=15m", http.StatusBadRequest},
		// group_by=model is only valid for cost_usd.
		{"model group only valid for cost", "metric=runs_count&window=24h&bucket=1h&group_by=model", http.StatusBadRequest},
		// group_by=crew is only valid for issues_closed/runs_count.
		{"crew group not valid for cost", "metric=cost_usd&window=24h&bucket=1h&group_by=crew", http.StatusBadRequest},
		// Valid combos that should pass.
		{"valid issues_closed 24h/1h", "metric=issues_closed&window=24h&bucket=1h", http.StatusOK},
		{"valid runs_count 7d/1d", "metric=runs_count&window=7d&bucket=1d", http.StatusOK},
		{"valid cost_usd 7d/1h", "metric=cost_usd&window=7d&bucket=1h", http.StatusOK},
		{"valid active_missions 24h/1h", "metric=active_missions&window=24h&bucket=1h", http.StatusOK},
		{"valid cost_usd group_by=model", "metric=cost_usd&window=24h&bucket=1h&group_by=model", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doTimeseries(t, h, wsID, tc.query)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

// decodeTimeseries parses a successful timeseries response body.
func decodeTimeseries(t *testing.T, body []byte) metricsResponse {
	t.Helper()
	var resp metricsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, string(body))
	}
	return resp
}

func TestMetricsTimeseries_BucketCount(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)

	cases := []struct {
		name    string
		query   string
		wantLen int
	}{
		{"24h/1h = 24", "metric=issues_closed&window=24h&bucket=1h", 24},
		{"24h/15m = 96", "metric=issues_closed&window=24h&bucket=15m", 96},
		{"7d/1d = 7", "metric=issues_closed&window=7d&bucket=1d", 7},
		{"7d/1h = 168", "metric=issues_closed&window=7d&bucket=1h", 168},
		{"30d/1d = 30", "metric=issues_closed&window=30d&bucket=1d", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doTimeseries(t, h, wsID, tc.query)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
			}
			resp := decodeTimeseries(t, rr.Body.Bytes())
			if len(resp.Buckets) != tc.wantLen {
				t.Fatalf("buckets = %d, want %d", len(resp.Buckets), tc.wantLen)
			}
		})
	}
}

func TestMetricsTimeseries_EmptyWorkspace_ZeroFilled(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)

	rr := doTimeseries(t, h, wsID, "metric=runs_count&window=24h&bucket=1h")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTimeseries(t, rr.Body.Bytes())
	if len(resp.Buckets) != 24 {
		t.Fatalf("buckets = %d, want 24", len(resp.Buckets))
	}
	if resp.SeriesLabels["total"] != "Total" {
		t.Errorf("series_labels[total] = %q, want %q", resp.SeriesLabels["total"], "Total")
	}
	for i, b := range resp.Buckets {
		if b.Series == nil {
			t.Errorf("bucket[%d].series is nil, want non-nil zero map", i)
			continue
		}
		got, ok := b.Series["total"]
		if !ok {
			t.Errorf("bucket[%d] missing 'total' key", i)
		}
		if got != 0 {
			t.Errorf("bucket[%d].series[total] = %v, want 0", i, got)
		}
	}
}

// seedMetricsIssue inserts a mission of type 'issue' with an explicit
// completed_at timestamp and DONE status. Labels are minimal to keep the test
// surface small.
func seedMetricsIssue(t *testing.T, db *sql.DB, id, wsID, crewID, leadID, completedAt string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, completed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Issue', 'DONE', 'issue', ?, ?, ?)`,
		id, wsID, crewID, leadID, "trace-"+id, completedAt, completedAt, completedAt)
	if err != nil {
		t.Fatalf("insert issue: %v", err)
	}
}

func TestMetricsTimeseries_IssuesClosed_GroupByCrew(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)

	// Seed two crews, one agent per crew, and two issues closed in the last hour.
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES
		('crew-eng', ?, 'Engineering', 'eng'),
		('crew-ops', ?, 'DevOps', 'ops')`, wsID, wsID)
	if err != nil {
		t.Fatalf("insert crews: %v", err)
	}
	seedMissionAgent(t, db, wsID, "crew-eng", "agent-eng", "LEAD")
	seedMissionAgent(t, db, wsID, "crew-ops", "agent-ops", "LEAD")

	// Close two issues "now" (well within a 24h window).
	recent := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	seedMetricsIssue(t, db, "issue-a", wsID, "crew-eng", "agent-eng", recent)
	seedMetricsIssue(t, db, "issue-b", wsID, "crew-eng", "agent-eng", recent)
	seedMetricsIssue(t, db, "issue-c", wsID, "crew-ops", "agent-ops", recent)

	rr := doTimeseries(t, h, wsID, "metric=issues_closed&window=24h&bucket=1h&group_by=crew")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTimeseries(t, rr.Body.Bytes())

	if len(resp.Buckets) != 24 {
		t.Fatalf("buckets = %d, want 24", len(resp.Buckets))
	}

	// series_labels must map each seen crew id to its human-readable name.
	if resp.SeriesLabels["crew-eng"] != "Engineering" {
		t.Errorf("series_labels[crew-eng] = %q, want %q", resp.SeriesLabels["crew-eng"], "Engineering")
	}
	if resp.SeriesLabels["crew-ops"] != "DevOps" {
		t.Errorf("series_labels[crew-ops] = %q, want %q", resp.SeriesLabels["crew-ops"], "DevOps")
	}

	// Totals across all buckets should match the three inserted issues split
	// 2/1 across the two crews.
	var engTotal, opsTotal float64
	for _, b := range resp.Buckets {
		engTotal += b.Series["crew-eng"]
		opsTotal += b.Series["crew-ops"]
	}
	if engTotal != 2 {
		t.Errorf("crew-eng total = %v, want 2", engTotal)
	}
	if opsTotal != 1 {
		t.Errorf("crew-ops total = %v, want 1", opsTotal)
	}
}

func TestMetricsTimeseries_BucketAlignment_24h1h(t *testing.T) {
	// Sanity-check that buckets are hour-aligned UTC strings matching the
	// sqlite strftime expression (so joins/lookups work).
	p := timeseriesParams{
		Window:    "24h",
		Bucket:    "1h",
		WindowDur: 24 * time.Hour,
		BucketDur: time.Hour,
		Now:       time.Date(2026, 4, 10, 14, 37, 0, 0, time.UTC),
	}
	starts := bucketStartsFor(p)
	if len(starts) != 24 {
		t.Fatalf("bucketStartsFor returned %d buckets, want 24", len(starts))
	}
	first := bucketKey(starts[0])
	last := bucketKey(starts[len(starts)-1])
	if first != "2026-04-09T15:00:00Z" {
		t.Errorf("first bucket = %q, want 2026-04-09T15:00:00Z", first)
	}
	if last != "2026-04-10T14:00:00Z" {
		t.Errorf("last bucket = %q, want 2026-04-10T14:00:00Z", last)
	}
}
