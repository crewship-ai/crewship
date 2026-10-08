package api

// #3012: the work ledger names what it holds. The list returned raw ids only
// (agent cmuxw95jn0…, session webhook-cm…), so the redesigned Work queue and
// Webhook deliveries could not say who did the work, for which event, how it
// went. Additive fields; access is the list's own.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkItemsList_NamesTheAgentCrewEventAndCost(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "crew-fin", ws, "Finance & Marketing", "finance")
	if _, err := db.Exec(`UPDATE crews SET color = 'emerald', icon = 'banknote' WHERE id = ?`, crew); err != nil {
		t.Fatal(err)
	}
	agent := seedAgentRow(t, db, "agent-casey", ws, crew, "Casey", "casey", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET avatar_seed = 'casey-seed', avatar_style = 'notionists' WHERE id = ?`, agent); err != nil {
		t.Fatal(err)
	}
	seedWorkItem(t, db, seededWork{ID: "wk-hook", WorkspaceID: ws, State: "succeeded", Source: "webhook", SourceRef: "dlv-1", AgentID: agent})
	seedDelivery(t, db, seededDelivery{ID: "dlv-1", WorkspaceID: ws, EndpointID: agent, SourceDeliveryID: "s-1", ContentKey: "k-1", EventType: "invoice.overdue", WorkID: "wk-hook"})
	if _, err := db.Exec(`UPDATE work_items SET crew_id = ? WHERE id = 'wk-hook'`, crew); err != nil {
		t.Fatal(err)
	}
	seedWorkAttempt(t, db, "wk-hook", "run-1", 1)
	if _, err := db.Exec(`UPDATE work_attempts SET started_at = '2026-10-08T10:00:00.000000000Z', ended_at = '2026-10-08T10:00:18.000000000Z', cost_usd = 0.012 WHERE run_id = 'run-1'`); err != nil {
		t.Fatal(err)
	}
	// Work whose agent was deleted keeps its row and says so.
	seedWorkItem(t, db, seededWork{ID: "wk-gone", WorkspaceID: ws, State: "failed", Source: "webhook", AgentID: "agent-deleted"})

	rr := httptest.NewRecorder()
	NewWorkItemsHandler(db, quietLogger()).List(rr, workReq(t, "GET", "/x", "", user, ws, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Agent *struct {
				Name        string `json:"name"`
				Slug        string `json:"slug"`
				AvatarSeed  string `json:"avatar_seed"`
				AvatarStyle string `json:"avatar_style"`
			} `json:"agent"`
			Crew *struct {
				Name  string `json:"name"`
				Color string `json:"color"`
				Icon  string `json:"icon"`
			} `json:"crew"`
			EventType  string   `json:"event_type"`
			DurationMS *int64   `json:"duration_ms"`
			CostUSD    *float64 `json:"cost_usd"`
		} `json:"items"`
	}
	decodeJSON(t, rr, &page)
	byID := map[string]int{}
	for i, it := range page.Items {
		byID[it.ID] = i
	}
	hook := page.Items[byID["wk-hook"]]
	if hook.Agent == nil || hook.Agent.Name != "Casey" || hook.Agent.AvatarSeed != "casey-seed" || hook.Agent.AvatarStyle != "notionists" {
		t.Errorf("agent = %+v, want Casey with her avatar", hook.Agent)
	}
	if hook.Crew == nil || hook.Crew.Name != "Finance & Marketing" || hook.Crew.Color != "emerald" || hook.Crew.Icon != "banknote" {
		t.Errorf("crew = %+v, want Finance & Marketing, emerald, banknote", hook.Crew)
	}
	if hook.EventType != "invoice.overdue" {
		t.Errorf("event_type = %q, want invoice.overdue", hook.EventType)
	}
	if hook.DurationMS == nil || *hook.DurationMS != 18000 || hook.CostUSD == nil || *hook.CostUSD != 0.012 {
		t.Errorf("duration/cost = %v/%v, want 18000/0.012", hook.DurationMS, hook.CostUSD)
	}
	if gone := page.Items[byID["wk-gone"]]; gone.Agent != nil {
		t.Errorf("a deleted agent is named %+v, want null", gone.Agent)
	}
}

func TestWebhookDeliveriesList_NamesTheAgentAndTheWorksState(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "crew-fin", ws, "Finance", "finance")
	agent := seedAgentRow(t, db, "agent-casey", ws, crew, "Casey", "casey", "AGENT")
	seedWorkItem(t, db, seededWork{ID: "wk-1", WorkspaceID: ws, State: "needs_reconciliation", Source: "webhook", AgentID: agent})
	seedDelivery(t, db, seededDelivery{ID: "dlv-1", WorkspaceID: ws, EndpointID: agent, SourceDeliveryID: "s-1", ContentKey: "k-1", EventType: "invoice.export", WorkID: "wk-1"})

	rr := httptest.NewRecorder()
	NewWebhookDeliveriesHandler(db, quietLogger()).List(rr, workReq(t, "GET", "/x", "", user, ws, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var page struct {
		Items []struct {
			Agent *struct {
				Name string `json:"name"`
			} `json:"agent"`
			WorkState *string `json:"work_state"`
		} `json:"items"`
	}
	decodeJSON(t, rr, &page)
	if len(page.Items) != 1 || page.Items[0].Agent == nil || page.Items[0].Agent.Name != "Casey" {
		t.Fatalf("items = %+v, want one delivery naming Casey", page.Items)
	}
	if page.Items[0].WorkState == nil || *page.Items[0].WorkState != "needs_reconciliation" {
		t.Errorf("work_state = %v, want needs_reconciliation", page.Items[0].WorkState)
	}
}
