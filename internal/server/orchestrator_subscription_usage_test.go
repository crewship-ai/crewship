package server

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

func TestSubscriptionUsageRecorderPersistsPayer(t *testing.T) {
	db := siDB(t)
	record := newSubscriptionUsageRecorder(db, nil)
	err := record(context.Background(), orchestrator.SubscriptionUsage{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", RunID: "run1",
		CredentialID: "login1", Provider: "openai", Model: "gpt-6-astra", Plan: "ChatGPT Plus",
		InputTokens: 100, OutputTokens: 20, CachedInputTokens: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := paymaster.SubscriptionUsageByPlan(context.Background(), db, "ws1", time.Time{}, time.Time{})
	if err != nil || len(rows) != 1 || rows[0].CredentialID != "login1" || rows[0].InTokens != 100 || rows[0].OutTokens != 20 {
		t.Fatalf("CLI observation did not reach per-login Paymaster rollup: %+v %v", rows, err)
	}
	var cost float64
	var source, runID string
	if err := db.QueryRow(`SELECT cost_usd, json_extract(tags, '$.source'), json_extract(tags, '$.run_id') FROM cost_ledger`).Scan(&cost, &source, &runID); err != nil {
		t.Fatal(err)
	}
	if cost != 0 || source != "subscription_cli" || runID != "run1" {
		t.Fatal("subscription usage was priced as metered or lost its provenance")
	}
}
