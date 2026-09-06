package paymaster

import (
	"context"
	"testing"
	"time"
)

func TestSubscriptionsWithSamePlanRemainSeparate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"login-a", "login-b", "", "login-a"} {
		_, err := Record(ctx, db, nil, Call{
			Scope: Scope{WorkspaceID: "ws1"}, Provider: "openai", Model: "gpt-6-astra",
			CredentialID: id, BillingMode: BillingFlatRate, SubscriptionPlan: "ChatGPT Plus",
			InputTokens: 10, OutputTokens: 5, CostUSD: 123,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := SubscriptionUsageByPlan(ctx, db, "ws1", time.Time{}, time.Time{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("want separate seats plus unknown historical attribution: %+v %v", rows, err)
	}
	got := map[string]int64{}
	for _, row := range rows {
		got[row.CredentialID] = row.CallCount
	}
	if got["login-a"] != 2 || got["login-b"] != 1 || got[""] != 1 {
		t.Fatalf("usage assigned to wrong login: %v", got)
	}
	var cost float64
	if err := db.QueryRowContext(ctx, `SELECT SUM(cost_usd) FROM cost_ledger`).Scan(&cost); err != nil || cost != 0 {
		t.Fatalf("subscription rows must not carry marginal dollar charges: %v %v", cost, err)
	}
	other, err := SubscriptionUsageByPlan(ctx, db, "ws-other", time.Time{}, time.Time{})
	if err != nil || len(other) != 0 {
		t.Fatal("payer rollup crossed the workspace boundary")
	}
}
