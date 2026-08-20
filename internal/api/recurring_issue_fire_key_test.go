package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// legacyFireKey is the pre-#1883 in-package hashing implementation that
// recurring_issue_dispatcher.go carried: the same sha256 preimage as
// pipeline.ScheduledFireIdempotencyKey, rendered as the FULL 64-hex digest
// with no kind prefix. It survives here as a frozen reference so the
// key-format change #1883 makes is measured, not assumed — see
// TestRecurringIssueFireKey_SharedKeyIsNotByteIdenticalToLegacy.
func legacyFireKey(kind, id, bucket string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id + "\x00" + bucket))
	return hex.EncodeToString(sum[:])
}

// fireKeyRow is one fire in a scenario: which template fires, at which
// occurrence bucket.
type fireKeyRow struct {
	id     string
	bucket string
}

// The observable contract of the fire key, exercised end-to-end through
// fireOne rather than through the key function: repeated fires of the SAME
// occurrence create exactly one issue, and every axis that makes an
// occurrence distinct (template id, occurrence bucket) fires again.
//
// This pins BEHAVIOUR, not the key string, so it passes identically before
// and after #1883 swapped the in-package hash for the shared
// pipeline.ScheduledFireIdempotencyKey. The key format did change (see the
// sibling test) — this is what proves the change is invisible to dedup for
// any occurrence that has not already been reserved.
func TestRecurringIssueDispatcher_OccurrenceDedupBehaviour(t *testing.T) {
	const (
		bucketA = "2020-01-01T00:00:00Z"
		bucketB = "2020-01-02T00:00:00Z"
	)
	tests := []struct {
		name       string
		fires      []fireKeyRow
		wantIssues int
	}{
		{
			name:       "same occurrence twice dedupes to one issue",
			fires:      []fireKeyRow{{"ri-a", bucketA}, {"ri-a", bucketA}},
			wantIssues: 1,
		},
		{
			name:       "same occurrence many times still one issue",
			fires:      []fireKeyRow{{"ri-a", bucketA}, {"ri-a", bucketA}, {"ri-a", bucketA}, {"ri-a", bucketA}},
			wantIssues: 1,
		},
		{
			name:       "distinct occurrence buckets each fire",
			fires:      []fireKeyRow{{"ri-a", bucketA}, {"ri-a", bucketB}},
			wantIssues: 2,
		},
		{
			name:       "same bucket on distinct templates each fire",
			fires:      []fireKeyRow{{"ri-a", bucketA}, {"ri-b", bucketA}},
			wantIssues: 2,
		},
		{
			name:       "interleaved duplicates across two templates dedupe per occurrence",
			fires:      []fireKeyRow{{"ri-a", bucketA}, {"ri-b", bucketA}, {"ri-a", bucketA}, {"ri-b", bucketA}},
			wantIssues: 2,
		},
		{
			// An empty bucket means the row had no next_run to key on, so no
			// reservation is taken at all and every fire inserts. Unchanged by
			// #1883 — the key is never derived on this path.
			name:       "empty occurrence bucket takes no reservation and fires every time",
			fires:      []fireKeyRow{{"ri-a", ""}, {"ri-a", ""}},
			wantIssues: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			h, _, wsID, crewID := covRIFixture(t)
			seedAgentRow(t, h.db, "lead-fk", wsID, crewID, "Lead", "lead-fk", "LEAD")

			// Seed every template the scenario touches. The row must exist so
			// the same-tx schedule advance can UPDATE it.
			seeded := map[string]bool{}
			for _, f := range tt.fires {
				if seeded[f.id] {
					continue
				}
				seeded[f.id] = true
				execOrFatal(t, h.db, `INSERT INTO recurring_issues
					(id, workspace_id, crew_id, title, cron_expression, enabled, next_run, run_count, created_at)
					VALUES (?, ?, ?, 'FireKey', '*/5 * * * *', 1, ?, 0, datetime('now'))`,
					f.id, wsID, crewID, bucketA)
			}

			d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
			for _, f := range tt.fires {
				d.fireOne(ctx, recurringDueRow{
					id:            f.id,
					workspaceID:   wsID,
					crewID:        crewID,
					title:         "FireKey",
					cronExpr:      "*/5 * * * *",
					nextRunBucket: f.bucket,
				})
			}

			var got int
			if err := h.db.QueryRow(
				`SELECT COUNT(*) FROM missions WHERE crew_id=? AND authored_via='recurring'`,
				crewID).Scan(&got); err != nil {
				t.Fatalf("count fired issues: %v", err)
			}
			if got != tt.wantIssues {
				t.Errorf("fired %d issues, want %d", got, tt.wantIssues)
			}
		})
	}
}

// The reservation the dispatcher writes must carry the SHARED key, and that
// key is deliberately NOT byte-identical to the one the deleted in-package
// implementation produced: both hash the same preimage, but the shared form
// is kind-prefixed and truncated to the first 16 digest bytes
// ("recurring_issue-<32 hex>") where the old form was the bare 64-hex digest.
//
// Consequence, recorded here so it cannot be rediscovered the hard way: an
// occurrence reserved by a pre-#1883 binary is keyed under the old string. A
// post-#1883 binary re-deriving that same occurrence gets a different key and
// sees it as fresh. That only matters inside the reservation's 24h TTL and
// only for an occurrence reserved but not yet advanced past — in practice the
// mixed-version window of a rolling deploy, where an old and a new replica
// could each fire the same due row once.
func TestRecurringIssueFireKey_SharedKeyIsNotByteIdenticalToLegacy(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		bucket string
	}{
		{"typical occurrence", "ri-idem", "2020-01-01T00:00:00Z"},
		{"distinct bucket", "ri-idem", "2020-01-02T00:00:00Z"},
		{"distinct template", "ri-other", "2020-01-01T00:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shared := pipeline.ScheduledFireIdempotencyKey("recurring_issue", tt.id, tt.bucket)
			legacy := legacyFireKey("recurring_issue", tt.id, tt.bucket)
			if shared == legacy {
				t.Fatalf("shared and legacy keys are identical (%q) — the migration note on this test is stale", shared)
			}
			// The digest itself agrees; only the rendering differs. If this
			// ever fails, the two schemes have genuinely diverged and the
			// deploy-window reasoning above no longer holds.
			if want := "recurring_issue-" + legacy[:32]; shared != want {
				t.Errorf("shared key = %q, want %q (kind-prefixed 16-byte truncation of the same digest)", shared, want)
			}
		})
	}

	// End-to-end: the row the dispatcher actually reserves carries the shared
	// key, so every firing path in the process now agrees on one scheme.
	ctx := context.Background()
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-fk2", wsID, crewID, "Lead", "lead-fk2", "LEAD")

	row := recurringDueRow{
		id:            "ri-keyfmt",
		workspaceID:   wsID,
		crewID:        crewID,
		title:         "KeyFmt",
		cronExpr:      "*/5 * * * *",
		nextRunBucket: "2020-01-01T00:00:00Z",
	}
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, cron_expression, enabled, next_run, run_count, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, 0, datetime('now'))`,
		row.id, wsID, crewID, row.title, row.cronExpr, row.nextRunBucket)

	NewRecurringIssueDispatcher(h.db, nil, newTestLogger()).fireOne(ctx, row)

	var stored string
	if err := h.db.QueryRow(
		`SELECT idempotency_key FROM pipeline_run_idempotency WHERE workspace_id=? AND pipeline_id=?`,
		wsID, row.id).Scan(&stored); err != nil {
		t.Fatalf("load reservation: %v", err)
	}
	want := pipeline.ScheduledFireIdempotencyKey("recurring_issue", row.id, row.nextRunBucket)
	if stored != want {
		t.Errorf("reserved key = %q, want the shared %q", stored, want)
	}
}
