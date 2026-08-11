package api

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// port_expose_registry.go — purgeOnce must bound each UPDATE statement.
//
// Regression cover for the sweeper that caused the 2026-05-25 login lockout
// incident (see internal/database/database.go, busy_timeout comment): a single
// unbounded `UPDATE port_exposures SET status='EXPIRED' WHERE status='ACTIVE'
// AND expires_at < ?` holds SQLite's one and only writer lock for as long as
// the backlog takes (measured: 41ms @ 5k rows, 486ms @ 50k rows), and every
// live writer in the process queues behind it.
//
// The properties asserted here:
//   - a backlog LARGER than one batch is fully drained by ONE purgeOnce call
//     (bounding must not turn a 50k backlog into a multi-hour drain),
//   - no single UPDATE touches more than `batch` rows — observed via the
//     statement count purgeOnce reports, not by mocking the driver,
//   - ACTIVE-but-unexpired rows are never flipped,
//   - REVOKED rows are never clobbered back to EXPIRED. purgeOnce's own
//     comment calls this out: a concurrent revoke already moved the row and
//     the sweeper must not overwrite that terminal state.
//
// Test seam: purgeOnce's batch size and iteration cap are struct fields
// (defaulted from package constants in NewPortExposeRegistry). Overriding two
// ints on the instance under test is the least invasive seam available —
// a package-level var would be shared mutable state across the package's
// tests (this package already runs registry tests alongside a background
// purger goroutine), and wrapping database/sql with a counting driver would
// be mocking the layer whose behaviour we actually care about.
// ---------------------------------------------------------------------------

// portPurgeBatchRow describes one seeded port_exposures row and the status it
// must have once purgeOnce has run.
type portPurgeBatchRow struct {
	id      string
	token   string
	status  string        // seeded status: ACTIVE / REVOKED
	expires time.Duration // relative to now; negative == already expired
	want    string        // expected status after purgeOnce
}

// portPurgeBatchInsert seeds a row at an arbitrary status. The shared
// insertActiveRow helper only writes ACTIVE rows, and the REVOKED-clobber
// case needs a terminal-state row.
func portPurgeBatchInsert(t *testing.T, db *sql.DB, r portPurgeBatchRow, now time.Time) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES (?, 'ws', 'crew', 'agent', ?, 'ct', '10.0.0.1', 8000, ?, ?)
`, r.id, r.token, r.status, now.Add(r.expires).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed row %s: %v", r.id, err)
	}
}

// portPurgeBatchStatuses reads back every row's status keyed by id.
func portPurgeBatchStatuses(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, status FROM port_exposures`)
	if err != nil {
		t.Fatalf("read back statuses: %v", err)
	}
	defer rows.Close()
	got := make(map[string]string)
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatalf("scan status: %v", err)
		}
		got[id] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate statuses: %v", err)
	}
	return got
}

// portPurgeBatchExpiredRows builds n already-expired ACTIVE rows, staggered so
// the oldest-first drain order is observable.
func portPurgeBatchExpiredRows(n int, prefix string) []portPurgeBatchRow {
	out := make([]portPurgeBatchRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, portPurgeBatchRow{
			id:      fmt.Sprintf("%s-%03d", prefix, i),
			token:   fmt.Sprintf("tok-%s-%03d", prefix, i),
			status:  "ACTIVE",
			expires: -time.Duration(n-i) * time.Minute,
			want:    "EXPIRED",
		})
	}
	return out
}

// portPurgeBatchLogger captures log output so the "stopped early with backlog
// remaining" warning can be asserted — the project's rule is no silent caps.
func portPurgeBatchLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestPortExposeRegistry_PurgeOnceBatchesWrites(t *testing.T) {
	tests := []struct {
		name           string
		batch          int
		rows           []portPurgeBatchRow
		wantStatements int
		reason         string
	}{
		{
			name:  "backlog larger than one batch drains in a single tick",
			batch: 10,
			rows:  portPurgeBatchExpiredRows(25, "big"),
			// 10 + 10 + 5: the third statement is short, so the loop stops.
			wantStatements: 3,
			reason:         "25 expired rows must all be EXPIRED after one purgeOnce",
		},
		{
			name:  "backlog that is an exact multiple of batch needs a trailing empty statement",
			batch: 5,
			rows:  portPurgeBatchExpiredRows(10, "exact"),
			// 5 + 5 + 0: the loop cannot know it is done until a statement
			// comes back short.
			wantStatements: 3,
			reason:         "exact-multiple boundary must still terminate and drain",
		},
		{
			name:  "unexpired ACTIVE rows are never flipped",
			batch: 10,
			rows: append(
				portPurgeBatchExpiredRows(12, "mix"),
				portPurgeBatchRow{id: "live-1", token: "tok-live-1", status: "ACTIVE", expires: time.Hour, want: "ACTIVE"},
				portPurgeBatchRow{id: "live-2", token: "tok-live-2", status: "ACTIVE", expires: 24 * time.Hour, want: "ACTIVE"},
				portPurgeBatchRow{id: "live-3", token: "tok-live-3", status: "ACTIVE", expires: time.Minute, want: "ACTIVE"},
			),
			// 10 + 2: live rows must not pad the batches.
			wantStatements: 2,
			reason:         "LIMIT must not widen the predicate to unexpired rows",
		},
		{
			name:  "REVOKED rows are never clobbered back to EXPIRED",
			batch: 10,
			rows: append(
				portPurgeBatchExpiredRows(3, "rev"),
				portPurgeBatchRow{id: "revoked-1", token: "tok-revoked-1", status: "REVOKED", expires: -time.Hour, want: "REVOKED"},
				portPurgeBatchRow{id: "revoked-2", token: "tok-revoked-2", status: "REVOKED", expires: -2 * time.Hour, want: "REVOKED"},
				portPurgeBatchRow{id: "revoked-3", token: "tok-revoked-3", status: "REVOKED", expires: time.Hour, want: "REVOKED"},
			),
			wantStatements: 1,
			reason:         "a concurrent revoke already moved the row to a terminal state",
		},
		{
			name:  "empty backlog costs exactly one statement",
			batch: 10,
			rows: []portPurgeBatchRow{
				{id: "idle-1", token: "tok-idle-1", status: "ACTIVE", expires: time.Hour, want: "ACTIVE"},
				{id: "idle-2", token: "tok-idle-2", status: "ACTIVE", expires: time.Hour, want: "ACTIVE"},
			},
			wantStatements: 1,
			reason:         "the common case must not get more expensive",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db := newRegistryTestDB(t)
			r := NewPortExposeRegistry(db, portExposeTestLogger())
			r.purgeBatch = tc.batch

			now := time.Now().UTC()
			for _, row := range tc.rows {
				portPurgeBatchInsert(t, db, row, now)
				// Mirror every row into the in-memory registry so the
				// sweepInMemory half of purgeOnce is exercised too.
				r.Add(&ExposeEntry{
					ID:            row.id,
					Token:         row.token,
					ContainerID:   "ct",
					ContainerIP:   "10.0.0.1",
					ContainerPort: 8000,
					ExpiresAt:     now.Add(row.expires),
				})
			}

			statements := r.purgeOnce(context.Background())

			if statements != tc.wantStatements {
				t.Errorf("purgeOnce issued %d UPDATE statements, want %d — %s\n"+
					"a single unbounded UPDATE would report 1 regardless of backlog size, "+
					"which is exactly the writer-lock stall this test guards",
					statements, tc.wantStatements, tc.reason)
			}

			got := portPurgeBatchStatuses(t, db)
			for _, row := range tc.rows {
				if got[row.id] != row.want {
					t.Errorf("row %s status = %q, want %q (%s)", row.id, got[row.id], row.want, tc.reason)
				}
			}

			// In-memory invariant: expired entries gone, live entries kept.
			for _, row := range tc.rows {
				_, present := r.Lookup(row.token)
				wantPresent := row.expires > 0
				if present != wantPresent {
					t.Errorf("registry Lookup(%s) present = %v, want %v", row.token, present, wantPresent)
				}
			}
		})
	}
}

func TestPortExposeRegistry_PurgeOnceIterationCapIsLoud(t *testing.T) {
	// The safety cap exists so one tick cannot monopolise the writer lock
	// forever if the backlog is pathological. When it fires, the remaining
	// backlog must be reported — a silent cap would hide an unbounded queue.
	var buf bytes.Buffer
	db := newRegistryTestDB(t)
	r := NewPortExposeRegistry(db, portPurgeBatchLogger(&buf))
	r.purgeBatch = 5
	r.purgeMaxIters = 2

	now := time.Now().UTC()
	rows := portPurgeBatchExpiredRows(20, "cap")
	for _, row := range rows {
		portPurgeBatchInsert(t, db, row, now)
	}

	statements := r.purgeOnce(context.Background())
	if statements != 2 {
		t.Errorf("purgeOnce issued %d statements, want 2 (iteration cap)", statements)
	}

	got := portPurgeBatchStatuses(t, db)
	expired := 0
	for _, status := range got {
		if status == "EXPIRED" {
			expired++
		}
	}
	if expired != 10 {
		t.Errorf("expired %d rows under a 2×5 cap, want 10", expired)
	}
	// Oldest-first: the drain order must be deterministic so a capped tick
	// makes progress on the oldest backlog rather than re-picking randomly.
	for i := 0; i < 10; i++ {
		if got[rows[i].id] != "EXPIRED" {
			t.Errorf("row %s (oldest tranche) = %q, want EXPIRED", rows[i].id, got[rows[i].id])
		}
	}

	logged := buf.String()
	if !strings.Contains(logged, "remaining") {
		t.Errorf("iteration cap fired without logging the remaining backlog; log was:\n%s", logged)
	}
}

func TestPortExposeRegistry_PurgeOnceHonoursContextCancellation(t *testing.T) {
	// On shutdown the purger must not keep grinding through the backlog.
	// Honesty note: this case also passed pre-fix (the unbounded statement's
	// single ExecContext failed on the cancelled context anyway). It is a
	// guard against the batching loop regressing into an uncancellable spin,
	// not a discriminator for the original bug.
	db := newRegistryTestDB(t)
	r := NewPortExposeRegistry(db, portExposeTestLogger())
	r.purgeBatch = 5

	now := time.Now().UTC()
	for _, row := range portPurgeBatchExpiredRows(20, "cancel") {
		portPurgeBatchInsert(t, db, row, now)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if statements := r.purgeOnce(ctx); statements != 0 {
		t.Errorf("purgeOnce issued %d statements on a cancelled context, want 0", statements)
	}
	for id, status := range portPurgeBatchStatuses(t, db) {
		if status != "ACTIVE" {
			t.Errorf("row %s = %q on a cancelled purge, want ACTIVE", id, status)
		}
	}
}

func TestPortExposeRegistry_PurgeBatchDefaultsAreSane(t *testing.T) {
	// The defaults are the whole point: they must be wired by the
	// constructor, and together they must cover the worst backlog that was
	// actually measured (50k rows) inside a single 30s tick.
	r := NewPortExposeRegistry(newRegistryTestDB(t), portExposeTestLogger())
	if r.purgeBatch != portExposePurgeBatchSize {
		t.Errorf("purgeBatch = %d, want %d", r.purgeBatch, portExposePurgeBatchSize)
	}
	if r.purgeMaxIters != portExposePurgeMaxIterations {
		t.Errorf("purgeMaxIters = %d, want %d", r.purgeMaxIters, portExposePurgeMaxIterations)
	}
	if drain := portExposePurgeBatchSize * portExposePurgeMaxIterations; drain < 50_000 {
		t.Errorf("one tick drains at most %d rows, below the 50k backlog measured in the incident", drain)
	}
}
