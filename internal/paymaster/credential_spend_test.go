package paymaster

import (
	"context"
	"testing"
	"time"
)

func TestSpendByCredential(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ins := func(id, ws, cred string, ts time.Time, cost float64, billing string) {
		t.Helper()
		var credArg any
		if cred != "" {
			credArg = cred
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO cost_ledger
			(id, workspace_id, ts, provider, model, input_tokens, output_tokens, cost_usd, billing_mode, credential_id)
			VALUES (?, ?, ?, 'anthropic', 'm', 10, 5, ?, ?, ?)`,
			id, ws, ts.UTC().Format(tsLayout), cost, billing, credArg); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	ins("a1", "ws1", "key-1", month.Add(time.Hour), 1.25, "metered")
	ins("a2", "ws1", "key-1", month.Add(48*time.Hour), 2.0, "metered")
	ins("a3", "ws1", "key-2", month.Add(time.Hour), 0.5, "metered")
	ins("a4", "ws1", "seat-1", month.Add(time.Hour), 0, "flat_rate") // subscriptions have no $ — excluded
	ins("a5", "ws1", "key-1", month.Add(-time.Hour), 99, "metered")  // last month
	ins("a6", "ws1", "", month.Add(time.Hour), 7, "metered")         // no credential attribution
	ins("a7", "ws2", "key-1", month.Add(time.Hour), 50, "metered")   // another workspace

	got, err := SpendByCredential(ctx, db, "ws1", month, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want key-1 and key-2 only", got)
	}
	if got[0].CredentialID != "key-1" || got[0].CostUSD != 3.25 || got[0].CallCount != 2 || got[0].InTokens != 20 {
		t.Errorf("row0 = %+v, want key-1 $3.25 over 2 calls", got[0])
	}
	if got[1].CredentialID != "key-2" || got[1].CostUSD != 0.5 {
		t.Errorf("row1 = %+v, want key-2 $0.50", got[1])
	}
	if _, err := SpendByCredential(ctx, db, "", month, time.Time{}); err == nil {
		t.Error("empty workspace id accepted")
	}
}
