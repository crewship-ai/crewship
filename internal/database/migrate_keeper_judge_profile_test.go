package database

import (
	"context"
	"testing"
)

// The judge-profile columns have to exist on the real migrated schema, not only
// in the store's hand-written test DDL (internal/keepercfg/keepercfg_test.go).
// keepercfg reads and writes every one of them by name in a single statement, so
// a column missing here is not a degraded feature — it is a SQL error on every
// read of the Keeper judge configuration, which is on the credential-access path.
func TestKeeperJudgeProfileColumns(t *testing.T) {
	db := openMigratedDB(t)
	ctx := context.Background()

	cases := map[string][]string{
		"keeper_runtime_settings": {
			"judge_profile",
			"judge_evidence",
			"judge_evidence_facts",
			"judge_hard_gate",
			"judge_precedent",
			"judge_precedent_n",
			"judge_consistency_samples",
			"judge_prompt_budget_tokens",
		},
		// The decision record: which profile judged this request. Two decisions
		// taken under different capabilities are not comparable, and the eval
		// harness (PRD P4) replays this table.
		"keeper_requests": {"judge_profile"},
	}
	for table, columns := range cases {
		for _, col := range columns {
			var n int
			if err := db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
				t.Fatalf("probe %s.%s: %v", table, col, err)
			}
			if n != 1 {
				t.Errorf("%s.%s missing from the migrated schema", table, col)
			}
		}
	}
}

// The bounds live in the schema as well as in Go, because the store is not the
// only writer a database ever sees — a hand-edited row that says "sample the
// judge 400 times" must be refused by the table itself.
func TestKeeperJudgeProfileBounds(t *testing.T) {
	db := openMigratedDB(t)
	ctx := context.Background()

	rejected := []struct {
		name string
		sql  string
	}{
		{"even-but-in-range sample counts are the store's job, out-of-range is the table's",
			`INSERT INTO keeper_runtime_settings (id, judge_consistency_samples) VALUES ('singleton', 400)`},
		{"precedent examples above the ceiling",
			`INSERT INTO keeper_runtime_settings (id, judge_precedent_n) VALUES ('singleton', 99)`},
		{"a prompt budget too small to hold the policy",
			`INSERT INTO keeper_runtime_settings (id, judge_prompt_budget_tokens) VALUES ('singleton', 8)`},
		{"a toggle that is neither on nor off",
			`INSERT INTO keeper_runtime_settings (id, judge_hard_gate) VALUES ('singleton', 7)`},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, tc.sql); err == nil {
				t.Error("the table accepted the row")
			}
			// Each case inserts the same singleton id, so a case that WAS
			// (wrongly) accepted would otherwise make the next one fail on the
			// primary key instead of on its own constraint.
			if _, err := db.ExecContext(ctx, `DELETE FROM keeper_runtime_settings`); err != nil {
				t.Fatalf("clean up: %v", err)
			}
		})
	}
}
