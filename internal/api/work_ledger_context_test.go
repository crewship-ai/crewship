package api

// #3012: the work ledger names what it holds. The list returned raw ids only
// (agent cmuxw95jn0…, session webhook-cm…), so the redesigned Work queue and
// Webhook deliveries could not say who did the work, for which event, how it
// went. Additive fields; access is the list's own.

import (
	"encoding/json"
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

// An agent deleted from the workspace keeps its row (deleted_at) and its name:
// the ledger says it was deleted rather than presenting it as a live agent.
func TestWorkItemsList_SaysASoftDeletedAgentIsDeleted(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "crew-x", ws, "Shop", "shop")
	agent := seedAgentRow(t, db, "agent-robot", ws, crew, "Lab Robot", "lab-robot", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET deleted_at = datetime('now') WHERE id = ?`, agent); err != nil {
		t.Fatal(err)
	}
	seedWorkItem(t, db, seededWork{ID: "wk-robot", WorkspaceID: ws, State: "failed", Source: "webhook", AgentID: agent})

	rr := httptest.NewRecorder()
	NewWorkItemsHandler(db, quietLogger()).List(rr, workReq(t, "GET", "/x", "", user, ws, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var page struct {
		Items []struct {
			Agent *struct {
				Name    string `json:"name"`
				Deleted bool   `json:"deleted"`
			} `json:"agent"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Agent == nil {
		t.Fatalf("want one item with its agent, got %s", rr.Body.String())
	}
	if got := page.Items[0].Agent; got.Name != "Lab Robot" || !got.Deleted {
		t.Fatalf("a soft-deleted agent must keep its name and say deleted, got %+v", got)
	}
}

// The ledger draws an agent with the render stored for it, as every other
// list does. Without avatar_url the client takes the agent for one with no
// stored render and tries to backfill it on every page view, which the server
// refuses with 409.
func TestWorkItemsList_GivesTheAgentsStoredAvatar(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "crew-x", ws, "Shop", "shop")
	stored := seedAgentRow(t, db, "agent-stored", ws, crew, "Casey", "casey", "AGENT")
	plain := seedAgentRow(t, db, "agent-plain", ws, crew, "Robin", "robin", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET avatar_svg_hash = 'abc123' WHERE id = ?`, stored); err != nil {
		t.Fatal(err)
	}
	seedWorkItem(t, db, seededWork{ID: "wk-stored", WorkspaceID: ws, State: "succeeded", Source: "webhook", AgentID: stored})
	seedWorkItem(t, db, seededWork{ID: "wk-plain", WorkspaceID: ws, State: "succeeded", Source: "webhook", AgentID: plain})

	rr := httptest.NewRecorder()
	NewWorkItemsHandler(db, quietLogger()).List(rr, workReq(t, "GET", "/x", "", user, ws, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Agent *struct {
				AvatarURL *string `json:"avatar_url"`
			} `json:"agent"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	got := map[string]*string{}
	for _, it := range page.Items {
		if it.Agent == nil {
			t.Fatalf("item %s has no agent: %s", it.ID, rr.Body.String())
		}
		got[it.ID] = it.Agent.AvatarURL
	}
	want := "/api/v1/agents/agent-stored/avatar?v=abc123&workspace_id=" + ws
	if got["wk-stored"] == nil || *got["wk-stored"] != want {
		t.Errorf("stored avatar_url = %v, want %s", got["wk-stored"], want)
	}
	if got["wk-plain"] != nil {
		t.Errorf("an agent without a stored render gets avatar_url %q, want null", *got["wk-plain"])
	}
}
