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

type ledgerItemJSON struct {
	ID    string `json:"id"`
	Agent *struct {
		Name string `json:"name"`
	} `json:"agent"`
	Crew *struct {
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
	} `json:"crew"`
	EventType  string   `json:"event_type"`
	DurationMS *int64   `json:"duration_ms"`
	CostUSD    *float64 `json:"cost_usd"`
}

func listLedger(t *testing.T, h *WorkItemsHandler, user, ws string) map[string]ledgerItemJSON {
	t.Helper()
	rr := httptest.NewRecorder()
	h.List(rr, workReq(t, "GET", "/x", "", user, ws, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var page struct {
		Items []ledgerItemJSON `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	out := map[string]ledgerItemJSON{}
	for _, it := range page.Items {
		out[it.ID] = it
	}
	return out
}

// Resolve and replay answer with the work item the list and get describe. They
// returned it without agent, crew or event, so a client patching its cached
// row from the answer drew a live agent as deleted.
func TestWorkResolveAndReplay_AnswerWithTheLedgerContext(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "crew-x", ws, "Shop", "shop")
	agent := seedAgentRow(t, db, "agent-casey", ws, crew, "Casey", "casey", "AGENT")
	seedWorkItem(t, db, seededWork{ID: "wk-unclear", WorkspaceID: ws, State: "needs_reconciliation", Source: "webhook", SourceRef: "dlv-1", AgentID: agent, Generation: 3})
	seedDelivery(t, db, seededDelivery{ID: "dlv-1", WorkspaceID: ws, EndpointID: agent, SourceDeliveryID: "s-1", ContentKey: "k-1", EventType: "invoice.export", WorkID: "wk-unclear"})
	seedWorkItem(t, db, seededWork{ID: "wk-failed", WorkspaceID: ws, State: "failed", Source: "assignment", AgentID: agent})
	h := NewWorkItemsHandler(db, quietLogger())

	req := workReq(t, "POST", "/work-items/wk-unclear/resolve", `{"state":"failed","generation":3,"runtime_stopped":true,"reason":"checked the ERP"}`, user, ws, "MANAGER")
	req.SetPathValue("workItemId", "wk-unclear")
	rr := httptest.NewRecorder()
	h.Resolve(rr, req)
	var resolved ledgerItemJSON
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &resolved) != nil {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}
	if resolved.Agent == nil || resolved.Agent.Name != "Casey" || resolved.Crew == nil || resolved.EventType != "invoice.export" {
		t.Errorf("resolve answered without the ledger context: %s", rr.Body.String())
	}

	req = workReq(t, "POST", "/work-items/wk-failed/replay", `{"reason":"provider is back"}`, user, ws, "MANAGER")
	req.SetPathValue("workItemId", "wk-failed")
	rr = httptest.NewRecorder()
	h.Replay(rr, req)
	var replayed ledgerItemJSON
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &replayed) != nil {
		t.Fatalf("replay: %d %s", rr.Code, rr.Body.String())
	}
	if replayed.Agent == nil || replayed.Agent.Name != "Casey" {
		t.Errorf("replay answered without the agent: %s", rr.Body.String())
	}
}

// A replay of webhook work keeps no delivery of its own; it is still about the
// event its original came in for.
func TestWorkItemsList_AReplayNamesItsOriginalsEvent(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedWorkItem(t, db, seededWork{ID: "wk-orig", WorkspaceID: ws, State: "failed", Source: "webhook", SourceRef: "dlv-1"})
	seedDelivery(t, db, seededDelivery{ID: "dlv-1", WorkspaceID: ws, EndpointID: "agent-x", SourceDeliveryID: "s-1", ContentKey: "k-1", EventType: "invoice.export", WorkID: "wk-orig"})
	seedWorkItem(t, db, seededWork{ID: "wk-re", WorkspaceID: ws, State: "failed", Source: "webhook"})
	seedWorkItem(t, db, seededWork{ID: "wk-re2", WorkspaceID: ws, State: "queued", Source: "webhook"})
	if _, err := db.Exec(`UPDATE work_items SET replay_of = 'wk-orig' WHERE id = 'wk-re'; UPDATE work_items SET replay_of = 'wk-re' WHERE id = 'wk-re2'`); err != nil {
		t.Fatal(err)
	}
	got := listLedger(t, NewWorkItemsHandler(db, quietLogger()), user, ws)
	for _, id := range []string{"wk-re", "wk-re2"} {
		if got[id].EventType != "invoice.export" {
			t.Errorf("%s event_type = %q, want its original's invoice.export", id, got[id].EventType)
		}
	}
}

// Duration is how long finished work took; cost is what its attempts recorded.
// Neither is reported for what is not known yet.
func TestWorkItemsList_DurationAndCostOnlyWhenKnown(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedWorkItem(t, db, seededWork{ID: "wk-retried", WorkspaceID: ws, State: "succeeded", Generation: 2})
	seedWorkAttempt(t, db, "wk-retried", "run-a", 1)
	seedWorkAttempt(t, db, "wk-retried", "run-b", 2)
	seedWorkItem(t, db, seededWork{ID: "wk-running", WorkspaceID: ws, State: "running", Generation: 2})
	seedWorkAttempt(t, db, "wk-running", "run-c", 1)
	seedWorkAttempt(t, db, "wk-running", "run-d", 2)
	seedWorkItem(t, db, seededWork{ID: "wk-free", WorkspaceID: ws, State: "succeeded", Generation: 1})
	seedWorkAttempt(t, db, "wk-free", "run-e", 1)
	if _, err := db.Exec(`
		UPDATE work_attempts SET started_at = '2026-10-08T10:00:00.000000000Z', ended_at = '2026-10-08T10:00:10.000000000Z', cost_usd = 0.01 WHERE run_id IN ('run-a','run-c');
		UPDATE work_attempts SET started_at = '2026-10-08T10:01:00.000000000Z', ended_at = '2026-10-08T10:01:20.000000000Z', cost_usd = 0.02 WHERE run_id = 'run-b';
		UPDATE work_attempts SET started_at = '2026-10-08T10:01:00.000000000Z', ended_at = NULL WHERE run_id = 'run-d';
		UPDATE work_attempts SET started_at = '2026-10-08T10:00:00.000000000Z', ended_at = '2026-10-08T10:00:05.000000000Z', cost_usd = 0 WHERE run_id = 'run-e';`); err != nil {
		t.Fatal(err)
	}
	got := listLedger(t, NewWorkItemsHandler(db, quietLogger()), user, ws)
	if d := got["wk-retried"].DurationMS; d == nil || *d != 80_000 {
		t.Errorf("finished work's duration = %v, want 80000 (first start to last end)", d)
	}
	if c := got["wk-retried"].CostUSD; c == nil || *c < 0.0299 || *c > 0.0301 {
		t.Errorf("cost = %v, want both attempts summed (0.03)", c)
	}
	if d := got["wk-running"].DurationMS; d != nil {
		t.Errorf("running work reports a duration %d from its earlier attempt", *d)
	}
	if c := got["wk-free"].CostUSD; c != nil {
		t.Errorf("work whose attempts recorded no cost reports cost_usd %v, want null", *c)
	}
}

// A deleted crew keeps its name on the work it held and says it is deleted.
func TestWorkItemsList_SaysTheCrewWasDeleted(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "crew-gone", ws, "Old crew", "old-crew")
	agent := seedAgentRow(t, db, "agent-a", ws, crew, "Robin", "robin", "AGENT")
	if _, err := db.Exec(`UPDATE crews SET deleted_at = datetime('now') WHERE id = ?`, crew); err != nil {
		t.Fatal(err)
	}
	seedWorkItem(t, db, seededWork{ID: "wk-1", WorkspaceID: ws, State: "succeeded", AgentID: agent})
	got := listLedger(t, NewWorkItemsHandler(db, quietLogger()), user, ws)
	if c := got["wk-1"].Crew; c == nil || c.Name != "Old crew" || !c.Deleted {
		t.Fatalf("crew = %+v, want Old crew marked deleted", c)
	}
}

// An id that names an agent or crew in another workspace resolves to nothing.
func TestWorkItemsList_NeverNamesAnotherWorkspacesAgent(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	other := "ws-other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, other); err != nil {
		t.Fatal(err)
	}
	crew := seedCrewRow(t, db, "crew-o", other, "Their crew", "their-crew")
	agent := seedAgentRow(t, db, "agent-o", other, crew, "Theirs", "theirs", "AGENT")
	seedWorkItem(t, db, seededWork{ID: "wk-1", WorkspaceID: ws, State: "failed", Source: "webhook", SourceRef: "dlv-o", AgentID: agent})
	seedDelivery(t, db, seededDelivery{ID: "dlv-o", WorkspaceID: other, EndpointID: agent, SourceDeliveryID: "s-o", ContentKey: "k-o", EventType: "secret.event", WorkID: "wk-1"})
	if _, err := db.Exec(`UPDATE work_items SET crew_id = ? WHERE id = 'wk-1'`, crew); err != nil {
		t.Fatal(err)
	}
	got := listLedger(t, NewWorkItemsHandler(db, quietLogger()), user, ws)["wk-1"]
	if got.Agent != nil || got.Crew != nil || got.EventType != "" {
		t.Fatalf("another workspace leaked into the ledger: %+v", got)
	}
}

// The Work queue reads a time window. The list was the OLDEST hundred, so a
// busy workspace's window came back empty; since= keeps it to the window, and
// open=true adds what is still unfinished however old — the work that holds
// an agent's queue must never fall out of view.
func TestWorkItemsList_ReadsAWindowAndTheOpenWork(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedWorkItem(t, db, seededWork{ID: "wk-old-done", WorkspaceID: ws, State: "succeeded"})
	seedWorkItem(t, db, seededWork{ID: "wk-old-stuck", WorkspaceID: ws, State: "needs_reconciliation"})
	seedWorkItem(t, db, seededWork{ID: "wk-new", WorkspaceID: ws, State: "succeeded"})
	if _, err := db.Exec(`UPDATE work_items SET created_at = '2026-10-01T10:00:00.000000000Z' WHERE id IN ('wk-old-done','wk-old-stuck')`); err != nil {
		t.Fatal(err)
	}
	h := NewWorkItemsHandler(db, quietLogger())
	read := func(qs string) []string {
		rr := httptest.NewRecorder()
		h.List(rr, workReq(t, "GET", "/x?"+qs, "", user, ws, "OWNER"))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", qs, rr.Code, rr.Body.String())
		}
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &page)
		ids := []string{}
		for _, it := range page.Items {
			ids = append(ids, it.ID)
		}
		return ids
	}
	if got := read("since=2026-10-05T00:00:00Z"); len(got) != 1 || got[0] != "wk-new" {
		t.Errorf("since: %v, want [wk-new]", got)
	}
	if got := read("open=true"); len(got) != 1 || got[0] != "wk-old-stuck" {
		t.Errorf("open: %v, want [wk-old-stuck]", got)
	}
	rr := httptest.NewRecorder()
	h.List(rr, workReq(t, "GET", "/x?since=yesterday", "", user, ws, "OWNER"))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("an unreadable since is %d, want 400", rr.Code)
	}
}

func TestWebhookDeliveriesList_ReadsAWindow(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedDelivery(t, db, seededDelivery{ID: "dlv-old", WorkspaceID: ws, EndpointID: "a", SourceDeliveryID: "s-1", ContentKey: "k-1", EventType: "ping"})
	seedDelivery(t, db, seededDelivery{ID: "dlv-new", WorkspaceID: ws, EndpointID: "a", SourceDeliveryID: "s-2", ContentKey: "k-2", EventType: "ping"})
	if _, err := db.Exec(`UPDATE webhook_deliveries SET received_at = '2026-10-01T10:00:00.000000000Z' WHERE id = 'dlv-old'`); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	NewWebhookDeliveriesHandler(db, quietLogger()).List(rr, workReq(t, "GET", "/x?since=2026-10-05T00:00:00Z", "", user, ws, "OWNER"))
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &page) != nil {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if len(page.Items) != 1 || page.Items[0].ID != "dlv-new" {
		t.Fatalf("since: %+v, want only dlv-new", page.Items)
	}
}
