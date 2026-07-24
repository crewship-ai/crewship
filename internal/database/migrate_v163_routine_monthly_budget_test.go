package database

import "testing"

// TestMigrate_V163_MonthlyBudgetColumn asserts pipelines gained
// monthly_budget_usd (default 0 = no budget set) — issue #1422 item 3.
func TestMigrate_V163_MonthlyBudgetColumn(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(pipelines)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	found := false
	var defaultVal *string
	for rows.Next() {
		var (
			cid   int
			name  string
			ctype string
			nn    int
			d     *string
			pk    int
		)
		if err := rows.Scan(&cid, &name, &ctype, &nn, &d, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "monthly_budget_usd" {
			found = true
			defaultVal = d
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !found {
		t.Fatal("pipelines missing monthly_budget_usd column after v163")
	}
	if defaultVal == nil || *defaultVal != "0" {
		t.Errorf("monthly_budget_usd default = %v, want 0", defaultVal)
	}
}
