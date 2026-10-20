package api

import (
	"math"
	"net/http"
	"testing"
	"time"
)

// The Dashboard's Success and P95 tiles need a series to draw a sparkline.
// success_rate and p95_duration_ms are per-bucket, computed from the same
// run.* journal entries and the same rules as GET /api/v1/runs/insights:
// cancelled runs are neither a success nor a failure, running runs count in
// neither, and the duration covers every finished run.
func TestMetricsTimeseries_RunOutcomes(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	bucket := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	at := func(d time.Duration) string { return bucket.Add(5*time.Minute + d).Format(time.RFC3339Nano) }
	n := 0
	entry := func(trace, typ, ts string) {
		n++
		execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, entry_type, severity, actor_type, summary, trace_id, ts)
			VALUES (?, ?, ?, 'info', 'system', 'run', ?, ?)`, "je-"+trace+typ+string(rune('a'+n)), wsID, typ, trace, ts)
	}
	run := func(trace, terminal string, took time.Duration) {
		entry(trace, "run.started", at(0))
		if terminal != "" {
			entry(trace, terminal, at(took))
		}
	}
	run("t1", "run.completed", 10*time.Second)
	run("t2", "run.completed", 20*time.Second)
	run("t3", "run.failed", 40*time.Second)
	run("t4", "run.cancelled", 5*time.Second)
	run("t5", "", 0) // still running

	key := bucket.Format("2006-01-02T15:04:05Z")
	get := func(metric string) (metricsBucket, int) {
		rr := doTimeseries(t, h, wsID, "metric="+metric+"&window=24h&bucket=1h")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", metric, rr.Code, rr.Body.String())
		}
		resp := decodeTimeseries(t, rr.Body.Bytes())
		if len(resp.Buckets) != 24 {
			t.Fatalf("buckets = %d", len(resp.Buckets))
		}
		empty := 0
		var got metricsBucket
		for _, b := range resp.Buckets {
			if b.TS == key {
				got = b
			}
			if _, ok := b.Series["total"]; !ok {
				empty++
			}
		}
		return got, empty
	}

	t.Run("success_rate is succeeded over finished, as a percentage", func(t *testing.T) {
		b, empty := get("success_rate")
		if got := b.Series["total"]; math.Abs(got-66.67) > 0.01 {
			t.Errorf("success_rate = %v, want 66.67", got)
		}
		// A bucket with no finished runs has no value rather than a false 0 %.
		if empty != 23 {
			t.Errorf("buckets without a value = %d, want 23", empty)
		}
	})

	t.Run("p95_duration_ms is the nearest-rank p95 over every finished run", func(t *testing.T) {
		b, empty := get("p95_duration_ms")
		if got := b.Series["total"]; got != 40000 {
			t.Errorf("p95_duration_ms = %v, want 40000", got)
		}
		if empty != 23 {
			t.Errorf("buckets without a value = %d, want 23", empty)
		}
	})

	t.Run("another workspace's runs never count", func(t *testing.T) {
		execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('other-ws', 'O', 'o')`)
		execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, entry_type, severity, actor_type, summary, trace_id, ts) VALUES
			('x1', 'other-ws', 'run.started', 'info', 'system', 'r', 'tx', ?), ('x2', 'other-ws', 'run.failed', 'info', 'system', 'r', 'tx', ?)`, at(0), at(time.Second))
		b, _ := get("success_rate")
		if got := b.Series["total"]; math.Abs(got-66.67) > 0.01 {
			t.Errorf("success_rate = %v after a foreign failure", got)
		}
	})

	t.Run("grouping is not offered for them", func(t *testing.T) {
		rr := doTimeseries(t, h, wsID, "metric=success_rate&window=24h&bucket=1h&group_by=crew")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}
