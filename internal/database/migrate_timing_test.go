package database

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Migrations run before the server serves anything, so their cost is upgrade
// downtime. This measures the whole chain and names the slow ones, so a
// migration that turns a 3-second upgrade into a 3-minute one is visible in
// CI output rather than discovered by whoever upgrades first.
//
// The ceiling is deliberately generous: CI hardware varies, and a tight
// budget here would be a flaky test rather than a useful one. It is a
// catastrophe detector, not a benchmark — the per-migration table below it is
// what a human reads.
const migrationChainBudget = 90 * time.Second

type timedMigration struct {
	version  int
	name     string
	duration time.Duration
}

// durationLogHandler collects the "applied migration" lines Migrate emits.
type durationLogHandler struct {
	slog.Handler
	seen *[]timedMigration
}

func (h durationLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message != "applied migration" {
		return nil
	}
	var t timedMigration
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "version":
			t.version, _ = strconv.Atoi(a.Value.String())
		case "name":
			t.name = a.Value.String()
		case "duration":
			t.duration = parseLoggedDuration(a.Value.String())
		}
		return true
	})
	*h.seen = append(*h.seen, t)
	return nil
}

func (h durationLogHandler) Enabled(context.Context, slog.Level) bool { return true }

var durationRE = regexp.MustCompile(`^([0-9.]+)(ns|µs|us|ms|s|m)$`)

// parseLoggedDuration reads the rounded form Migrate logs. time.ParseDuration
// handles most of it; the compound "1m2.5s" form needs no special case, but a
// bare unit-suffixed number does.
func parseLoggedDuration(v string) time.Duration {
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if m := durationRE.FindStringSubmatch(v); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			unit := map[string]time.Duration{
				"ns": time.Nanosecond, "µs": time.Microsecond, "us": time.Microsecond,
				"ms": time.Millisecond, "s": time.Second, "m": time.Minute,
			}[m[2]]
			return time.Duration(f * float64(unit))
		}
	}
	return 0
}

func TestMigrationChainStaysWithinBudget(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var seen []timedMigration
	logger := slog.New(durationLogHandler{Handler: slog.NewTextHandler(nil, nil), seen: &seen})

	start := time.Now()
	if err := Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	total := time.Since(start)

	if len(seen) != len(migrations) {
		t.Errorf("timed %d migrations, expected %d — Migrate has an exit path that "+
			"does not report a duration, so a slow migration there would be invisible",
			len(seen), len(migrations))
	}

	sort.Slice(seen, func(i, j int) bool { return seen[i].duration > seen[j].duration })
	t.Logf("chain: %d migrations in %s", len(seen), total.Round(time.Millisecond))
	t.Log("slowest ten:")
	for i, m := range seen {
		if i >= 10 {
			break
		}
		t.Logf("  %-14d %-46s %s", m.version, m.name, m.duration.Round(time.Millisecond))
	}

	if total > migrationChainBudget {
		t.Errorf("full migration chain took %s, over the %s budget — an upgrade is downtime, "+
			"so check the slowest entries above before raising this",
			total.Round(time.Millisecond), migrationChainBudget)
	}
}
