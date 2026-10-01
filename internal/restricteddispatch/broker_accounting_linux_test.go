//go:build linux

package restricteddispatch

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestProviderAccountingPayerBindingsAndRevokedSettlement(t *testing.T) {
	a := providerFixture(t)
	permitProviderDelegation(t, a)
	if _, err := a.Store.DB.Exec(`UPDATE agents SET llm_model='gpt-5-mini' WHERE id='a'; INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('restricted-cap','w','workspace','w','month',.01,'hard')`); err != nil {
		t.Fatal(err)
	}
	h1, first := prepareProvider(t, a, "h1", "c1", "", 1000)
	h2, _ := prepareProvider(t, a, "h2", "c2", "", 1000)
	if _, err := a.BrokerReserve(t.Context(), h1, "foreign", "gpt-5-mini", 1000, 1000); err == nil {
		t.Fatal("foreign credential accepted")
	}
	id, err := a.BrokerReserve(t.Context(), h1, "key", "gpt-5-mini", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BrokerReserve(t.Context(), h2, "key", "gpt-5-mini", 1000, 1000); err == nil {
		t.Fatal("second human reset shared budget")
	}
	if err = a.BrokerSettle(t.Context(), h2, id, restrictedruntime.BrokerUsage{Known: true, InputTokens: 1, OutputTokens: 1}); err == nil {
		t.Fatal("other handle refunded payer")
	}
	var principal, attempt, credential, agent, workspace string
	if err = a.Store.DB.QueryRow(`SELECT r.principal_id,r.attempt_id,r.credential_id,l.agent_id,l.workspace_id FROM restricted_cost_reservations r JOIN cost_ledger l ON l.id=r.ledger_id WHERE r.id=?`, id).Scan(&principal, &attempt, &credential, &agent, &workspace); err != nil || principal != "h1" || attempt != first.ID || credential != "key" || agent != "a" || workspace != "w" {
		t.Fatalf("payer binding %s %s %s %s %s %v", principal, attempt, credential, agent, workspace, err)
	}
	if err = a.Store.RevokeAttempt(t.Context(), h1); err != nil {
		t.Fatal(err)
	}
	if err = a.BrokerSettle(t.Context(), h1, id, restrictedruntime.BrokerUsage{Known: true, InputTokens: 1, OutputTokens: 1}); err != nil {
		t.Fatal("revocation lost already incurred accounting", err)
	}
	if _, err = a.BrokerReserve(t.Context(), h1, "key", "gpt-5-mini", 1000, 1000); err == nil {
		t.Fatal("revoked handle launched provider")
	}
	statuses, err := paymaster.Check(t.Context(), a.Store.DB, paymaster.Scope{WorkspaceID: "w", AgentID: "a"})
	if err != nil || len(statuses) != 1 || statuses[0].SpentUSD >= .001 {
		t.Fatalf("known usage did not settle %v %v", statuses, err)
	}
}
