package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Nine migrations rewrite every row of a table. On an empty database they
// cost nothing, so the plain chain timing says almost nothing about a real
// upgrade — which is exactly the trap: the migration that hurts is the one
// nobody measured against data.
//
// journal_entries is the highest-volume table in a running instance (every
// agent action lands there) and three migrations rewrite it, including v152's
// hash-chain backfill, which has to read and rehash every row IN ORDER. That
// is the migration most likely to turn an upgrade into an outage, so it is
// the one measured here.
//
// This is a measurement with a ceiling, not a benchmark. It answers one
// question: does the cost grow with row count in a way that would make a real
// install's upgrade unacceptable?
const (
	// Overridable so the same test can confirm linearity at a larger size
	// without editing code: CREWSHIP_TEST_JOURNAL_ROWS=200000 go test -run …
	defaultScalingJournalRows = 20000
	// Generous, because CI hardware varies. It exists to catch a migration
	// that goes quadratic, not to police milliseconds.
	scalingBudget = 60 * time.Second
)

func TestMigrationChain_ScalesWithAPopulatedJournal(t *testing.T) {
	if testing.Short() {
		t.Skip("populated-upgrade timing is slow by design")
	}
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))

	rows := defaultScalingJournalRows
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_TEST_JOURNAL_ROWS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("CREWSHIP_TEST_JOURNAL_ROWS=%q is not a positive integer", v)
		}
		rows = n
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "scaling.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Land the schema just before the hash-chain backfill, so the rows we
	// insert are rows it has to process.
	const beforeChain = 151
	if err := applyMigrationsUpTo(ctx, db, beforeChain, quiet); err != nil {
		t.Fatalf("land schema at v%d: %v", beforeChain, err)
	}
	seedJournalRows(t, ctx, db, rows)

	var seen []timedMigration
	logger := slog.New(durationLogHandler{Handler: slog.NewTextHandler(nil, nil), seen: &seen})

	start := time.Now()
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate v%d → HEAD with %d journal rows: %v", beforeChain, rows, err)
	}
	total := time.Since(start)

	sort.Slice(seen, func(i, j int) bool { return seen[i].duration > seen[j].duration })
	t.Logf("v%d → HEAD with %d journal rows: %s", beforeChain, rows, total.Round(time.Millisecond))
	t.Log("slowest five:")
	for i, m := range seen {
		if i >= 5 {
			break
		}
		perRow := m.duration / time.Duration(rows)
		t.Logf("  %-14d %-40s %-9s (%v/row)", m.version, m.name,
			m.duration.Round(time.Millisecond), perRow.Round(time.Microsecond))
	}

	if total > scalingBudget {
		t.Errorf("upgrading %d journal rows took %s, over the %s ceiling. An upgrade is "+
			"downtime: a table-rewriting migration this expensive belongs in the "+
			"post-deployment lane (see docs/guides/migrations.mdx), not at boot",
			rows, total.Round(time.Millisecond), scalingBudget)
	}
}

// seedJournalRows inserts believable journal entries, with the workspace and
// crew rows their foreign keys require.
func seedJournalRows(t *testing.T, ctx context.Context, db *sql.DB, n int) {
	t.Helper()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws-scale', 'Scale', 'scale')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, actor_id, summary, payload)
		 VALUES (?, 'ws-scale', ?, 'tool.call', 'info', 'agent', 'agent-1', ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z")
		if _, err := stmt.ExecContext(ctx,
			fmt.Sprintf("je-%06d", i), ts,
			fmt.Sprintf("entry %d", i),
			`{"tool":"bash","args":{"command":"echo hello world"}}`,
		); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("close stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}
