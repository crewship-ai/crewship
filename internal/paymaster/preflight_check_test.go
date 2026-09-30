package paymaster

import (
	"github.com/crewship-ai/crewship/internal/testutil"
	"testing"
)

func TestPreflightBudgetTransactionCarriesOldPendingDebit(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('human','human@preflight.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','preflight-budget')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','ws','Crew','crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('agent','ws','crew','Agent','agent')`,
		`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('cap','ws','workspace','ws','month',100,'hard')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Reserve(t.Context(), db, reservationRequest())
	if err != nil {
		t.Fatal(err)
	}
	var debit float64
	if err = db.QueryRowContext(t.Context(), `SELECT cost_usd FROM cost_ledger WHERE id=?`, r.ID).Scan(&debit); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE cost_ledger SET ts='2020-01-01T00:00:00Z' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE budget_limits SET limit_usd=? WHERE id='cap'`, debit/2); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statuses, err := CheckTx(t.Context(), tx, reservationRequest().Scope)
	if err != nil || len(statuses) != 1 || statuses[0].SpentUSD != debit || statuses[0].State != StateExceeded {
		t.Fatalf("pending midnight debit became free: %+v %v", statuses, err)
	}
	tx.Rollback()
	if _, err = Reserve(t.Context(), db, reservationRequest()); err == nil {
		t.Fatal("paid request gate differed from preflight projection")
	}
	var count int
	if err = db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM journal_entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("private budget preflight emitted broad journal")
	}
}
