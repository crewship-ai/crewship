package api

// Pages — versions and rollback (docs/prd/pages.md §10b.1).
//
// The claim under test is the one §10b.1 states twice because it is the one
// that costs somebody money if it is wrong:
//
//	"Rollback restores structure, never numbers. A panel brought back by a
//	 rollback renders dimmed, in a 'waiting for first data' state, even if rows
//	 for it survive in the ring."
//
// So the interesting fixture is not "does the spec come back" — it is a panel
// whose ring SURVIVES the edit (a schema change keeps the page_panels row, and
// page_panel_data hangs off that row) and which the rollback then redefines.
// That panel must come back with no data at all. A panel the rollback does not
// touch keeps its data, because its payload was produced under exactly the
// definition that was restored.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Fixture ────────────────────────────────────────────────────────────────

const pagesVerSlug = "fleet-history"

// pagesVersionSpec builds a create/update body for a three-panel page.
// Callers vary the parts a rollback is supposed to notice.
func pagesVersionSpec(name, bSchema, cProducer string, withC bool) string {
	panels := []string{
		`{"id":"alfa","schema":"status.v1","owner":"crew/lookout","producer":"script/a.sh","sla_seconds":30,"span":4}`,
		`{"id":"beta","schema":"` + bSchema + `","owner":"crew/lookout","producer":"script/b.sh","sla_seconds":60,"span":4}`,
	}
	if withC {
		panels = append(panels,
			`{"id":"gama","schema":"status.v1","owner":"crew/lookout","producer":"`+cProducer+`","sla_seconds":90,"span":4}`)
	}
	return `{"slug":"` + pagesVerSlug + `","name":"` + name + `","panels":[` + strings.Join(panels, ",") + `]}`
}

func pagesVersionCreate(t *testing.T, h *PageHandler, wsID, userID, body string) string {
	t.Helper()
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("create response: %v", err)
	}
	return doc.ID
}

func pagesVersionUpdate(t *testing.T, h *PageHandler, wsID, userID, body string) {
	t.Helper()
	req := pagesRequest(t, "PATCH", "/api/v1/pages/"+pagesVerSlug, wsID, userID, "OWNER", body)
	req.SetPathValue("slug", pagesVerSlug)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
}

// pagesPushRow writes one payload straight into the ring.
//
// The push HANDLERS are proved in pages_handler_test.go and pages_internal_
// test.go; what this file needs is a ring with rows in it at a known time, and
// going through the producer-authority path to get one would make a rollback
// test fail for a permission reason.
func pagesPushRow(t *testing.T, h *PageHandler, pageID, panelID string, at time.Time) {
	t.Helper()
	var rowID string
	if err := h.db.QueryRow(
		`SELECT id FROM page_panels WHERE page_id = ? AND panel_id = ?`, pageID, panelID).Scan(&rowID); err != nil {
		t.Fatalf("find panel %s: %v", panelID, err)
	}
	var seq int64
	if err := h.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM page_panel_data WHERE panel_id = ?`, rowID).Scan(&seq); err != nil {
		t.Fatalf("next seq: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
		VALUES (?, ?, ?, ?, 'ok')`,
		rowID, seq, pagesStatusPayload, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("insert payload: %v", err)
	}
}

func pagesRingSize(t *testing.T, h *PageHandler, pageID, panelID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`
		SELECT COUNT(*) FROM page_panel_data d
		JOIN page_panels p ON p.id = d.panel_id
		WHERE p.page_id = ? AND p.panel_id = ?`, pageID, panelID).Scan(&n); err != nil {
		t.Fatalf("ring size: %v", err)
	}
	return n
}

func pagesRollback(t *testing.T, h *PageHandler, wsID, userID string, to int64) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, "POST", "/api/v1/pages/"+pagesVerSlug+"/rollback", wsID, userID, "OWNER",
		fmt.Sprintf(`{"to": %d}`, to))
	req.SetPathValue("slug", pagesVerSlug)
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	return rr
}

// pagesPanelStates reads the page through the ordinary GET and returns each
// panel's SERVER-computed state and whether it carries data.
func pagesPanelStates(t *testing.T, h *PageHandler, wsID, userID string) map[string]struct {
	State   string
	HasData bool
} {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages/"+pagesVerSlug, wsID, userID, "OWNER", "")
	req.SetPathValue("slug", pagesVerSlug)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var doc struct {
		Panels []struct {
			ID    string          `json:"id"`
			State string          `json:"state"`
			Data  json.RawMessage `json:"data"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	out := map[string]struct {
		State   string
		HasData bool
	}{}
	for _, p := range doc.Panels {
		out[p.ID] = struct {
			State   string
			HasData bool
		}{State: p.State, HasData: len(p.Data) > 0 && string(p.Data) != "null"}
	}
	return out
}

// ── 1. The history a human chooses from ────────────────────────────────────

// TestPageVersions_ShowsWhatARollbackCouldChooseFrom — `crewship page versions`
// exists so a human can see what they would roll back to, which means the list
// has to carry the seq, when it was saved, who saved it and how big the page
// was at the time.
func TestPageVersions_ShowsWhatARollbackCouldChooseFrom(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesVersionCreate(t, h, wsID, userID, pagesVersionSpec("Prvni", "status.v1", "script/c.sh", true))
	clock.advance(time.Minute)
	pagesVersionUpdate(t, h, wsID, userID, pagesVersionSpec("Druhy", "status.v1", "script/c.sh", false))

	req := pagesRequest(t, "GET", "/api/v1/pages/"+pagesVerSlug+"/versions", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", pagesVerSlug)
	rr := httptest.NewRecorder()
	h.ListVersions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("versions: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Page     string `json:"page"`
		Retained int    `json:"retained"`
		Versions []struct {
			Seq         int64  `json:"seq"`
			CreatedAt   string `json:"created_at"`
			Author      string `json:"author"`
			AuthorLabel string `json:"author_label"`
			Name        string `json:"name"`
			PanelCount  int    `json:"panel_count"`
			Current     bool   `json:"current"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if out.Retained != 50 {
		t.Errorf("retained = %d, want 50 (§10b.3)", out.Retained)
	}
	if len(out.Versions) != 2 {
		t.Fatalf("got %d versions, want 2 — a create and an update are two saves: %s", len(out.Versions), rr.Body.String())
	}
	// Newest first, and the newest is the one you are looking at.
	if out.Versions[0].Seq != 2 || !out.Versions[0].Current {
		t.Errorf("first row = seq %d current=%v, want seq 2 flagged current", out.Versions[0].Seq, out.Versions[0].Current)
	}
	if out.Versions[1].Seq != 1 || out.Versions[1].Current {
		t.Errorf("second row = seq %d current=%v, want seq 1 not current", out.Versions[1].Seq, out.Versions[1].Current)
	}
	// What was IN each version, so the choice is informed rather than a guess.
	if out.Versions[0].Name != "Druhy" || out.Versions[0].PanelCount != 2 {
		t.Errorf("seq 2 = %q with %d panels, want \"Druhy\" with 2", out.Versions[0].Name, out.Versions[0].PanelCount)
	}
	if out.Versions[1].Name != "Prvni" || out.Versions[1].PanelCount != 3 {
		t.Errorf("seq 1 = %q with %d panels, want \"Prvni\" with 3", out.Versions[1].Name, out.Versions[1].PanelCount)
	}
	// Who — "the one who breaks it is rarely the one who notices".
	if out.Versions[0].Author != "user/"+userID {
		t.Errorf("author = %q, want user/%s", out.Versions[0].Author, userID)
	}
	if out.Versions[0].AuthorLabel != "test@example.com" {
		t.Errorf("author_label = %q, want the address a human recognises", out.Versions[0].AuthorLabel)
	}
	if strings.TrimSpace(out.Versions[0].CreatedAt) == "" {
		t.Error("a version with no timestamp cannot be chosen between")
	}
}

// ── 2. The rule ────────────────────────────────────────────────────────────

// TestPageRollback_RestoresTheSpecAndLeavesTheNumbersBehind is §10b.1, whole.
//
// The fixture is built so that all three cases are live at once:
//
//	alfa — untouched by the rollback: keeps its data, because that payload was
//	       produced under exactly the definition being restored.
//	beta — its SCHEMA changed in v2 and the rollback changes it back. The
//	       page_panels row survived the edit, so its ring survived with it —
//	       this is literally "rows for it survive in the ring", and the panel
//	       must still come back dimmed.
//	gama — dropped in v2, brought back by the rollback: no data, no pretence.
func TestPageRollback_RestoresTheSpecAndLeavesTheNumbersBehind(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pageID := pagesVersionCreate(t, h, wsID, userID,
		pagesVersionSpec("Prvni", "status.v1", "script/c.sh", true))

	// v2: beta becomes a metric panel, gama is dropped.
	clock.advance(time.Minute)
	pagesVersionUpdate(t, h, wsID, userID, pagesVersionSpec("Druhy", "metric.v1", "script/c.sh", false))

	// Both surviving panels have fresh data at the moment of the rollback.
	pagesPushRow(t, h, pageID, "alfa", clock.now)
	pagesPushRow(t, h, pageID, "beta", clock.now)
	if pagesRingSize(t, h, pageID, "beta") != 1 {
		t.Fatalf("fixture: beta's ring is empty, so the test cannot prove rows SURVIVE the edit")
	}

	rr := pagesRollback(t, h, wsID, userID, 1)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		RolledBackTo int64    `json:"rolled_back_to"`
		Version      int64    `json:"version"`
		AwaitingData []string `json:"awaiting_data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode rollback: %v", err)
	}
	if out.RolledBackTo != 1 {
		t.Errorf("rolled_back_to = %d, want 1", out.RolledBackTo)
	}
	// A rollback is a save, and appends. Truncating the history to the target
	// would make a rollback impossible to undo.
	if out.Version != 3 {
		t.Errorf("the rollback saved version %d, want 3 — a rollback appends rather than rewriting the log", out.Version)
	}

	// STRUCTURE is back: three panels, beta a status panel again.
	states := pagesPanelStates(t, h, wsID, userID)
	if len(states) != 3 {
		t.Fatalf("page has %d panels after the rollback, want the 3 of version 1: %v", len(states), states)
	}
	var betaSchema string
	if err := h.db.QueryRow(
		`SELECT schema FROM page_panels WHERE page_id = ? AND panel_id = 'beta'`, pageID).Scan(&betaSchema); err != nil {
		t.Fatalf("read beta: %v", err)
	}
	if betaSchema != "status.v1" {
		t.Errorf("beta schema = %q after rollback, want status.v1", betaSchema)
	}

	// NUMBERS are not. The two panels the rollback brought back or redefined
	// are dimmed — the server's own fourth state, not a renderer's guess.
	for _, panel := range []string{"beta", "gama"} {
		got := states[panel]
		if got.State != "never_produced" {
			t.Errorf("panel %s is %q after the rollback, want never_produced — §10b.1: a panel brought back "+
				"renders waiting for first data, even if rows for it survive in the ring", panel, got.State)
		}
		if got.HasData {
			t.Errorf("panel %s carries data after the rollback; an old payload is being shown as current, "+
				"which is precisely the lie §4 exists to prevent", panel)
		}
		if n := pagesRingSize(t, h, pageID, panel); n != 0 {
			t.Errorf("panel %s still holds %d ring rows; the payload would resurface on the next read", panel, n)
		}
	}
	if !pagesListNames(out.AwaitingData, "beta") || !pagesListNames(out.AwaitingData, "gama") {
		t.Errorf("awaiting_data = %v, want it to name beta and gama so the operator learns it from the "+
			"response rather than from a blank panel later", out.AwaitingData)
	}

	// And the panel the rollback did not touch keeps its history: blanking it
	// would be destroying data the rollback had no quarrel with.
	if states["alfa"].State == "never_produced" || !states["alfa"].HasData {
		t.Errorf("panel alfa lost its data (%v); it was not brought back or redefined, and its payload was "+
			"produced under exactly the definition that was restored", states["alfa"])
	}
	if n := pagesRingSize(t, h, pageID, "alfa"); n != 1 {
		t.Errorf("alfa's ring holds %d rows, want 1", n)
	}
}

// TestPageRollback_RefusesAVersionNamingSomethingThatIsGone — a rollback is
// authoring, and the authoring gate says a save may not name a producer that
// does not exist. Restoring a spec whose routine has since been deleted would
// bring back a grid of dead panels, which is the same failure §10b.2 designs
// import against.
func TestPageRollback_RefusesAVersionNamingSomethingThatIsGone(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, "nightly")
	pagesVersionCreate(t, h, wsID, userID,
		pagesVersionSpec("Prvni", "status.v1", "routine/nightly", true))
	clock.advance(time.Minute)
	pagesVersionUpdate(t, h, wsID, userID, pagesVersionSpec("Druhy", "status.v1", "script/c.sh", false))

	if _, err := h.db.Exec(
		`UPDATE pipelines SET deleted_at = ? WHERE workspace_id = ? AND slug = 'nightly'`,
		clock.now.Format(time.RFC3339), wsID); err != nil {
		t.Fatalf("delete routine: %v", err)
	}

	rr := pagesRollback(t, h, wsID, userID, 1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("rollback to a version naming a deleted routine: status = %d, want 400, body: %s",
			rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "nightly") {
		t.Errorf("the refusal does not name the routine that is gone: %s", rr.Body.String())
	}
}

// TestPageRollback_RefusesToRollBackWithoutTheWriteAuthority — a rollback
// rewrites the arrangement, so it is the `write` verb, not readership.
func TestPageRollback_RefusesToRollBackWithoutTheWriteAuthority(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesVersionCreate(t, h, wsID, userID, pagesVersionSpec("Prvni", "status.v1", "script/c.sh", true))
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES ('outsider', 'out@example.com', 'Out')`); err != nil {
		t.Fatalf("insert outsider: %v", err)
	}

	req := pagesRequest(t, "POST", "/api/v1/pages/"+pagesVerSlug+"/rollback", wsID, "outsider", "MEMBER", `{"to":1}`)
	req.SetPathValue("slug", pagesVerSlug)
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("rollback by a plain member: status = %d, want 403, body: %s", rr.Code, rr.Body.String())
	}
}

// ── 3. Retention ───────────────────────────────────────────────────────────

// TestPageVersions_PruneAtFiftyOne — §10b.3 retains 50 versions per page, and
// the retention is Go's job. The 51st save is where the rule either works or
// has never been exercised.
func TestPageVersions_PruneAtFiftyOne(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesVersionCreate(t, h, wsID, userID, pagesVersionSpec("v1", "status.v1", "script/c.sh", true))

	// 50 updates on top of the create = 51 saves.
	for i := 2; i <= 51; i++ {
		clock.advance(time.Second)
		pagesVersionUpdate(t, h, wsID, userID,
			pagesVersionSpec(fmt.Sprintf("v%d", i), "status.v1", "script/c.sh", true))
	}

	var count, lowest, highest int64
	if err := h.db.QueryRow(`
		SELECT COUNT(*), COALESCE(MIN(seq), 0), COALESCE(MAX(seq), 0)
		FROM page_versions v JOIN pages p ON p.id = v.page_id
		WHERE p.workspace_id = ? AND p.slug = ?`, wsID, pagesVerSlug).Scan(&count, &lowest, &highest); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if highest != 51 {
		t.Fatalf("fixture: highest seq = %d, want 51 (one create + 50 updates)", highest)
	}
	if count != 50 {
		t.Errorf("page_versions holds %d rows after 51 saves, want 50 (§10b.3: versions per page 50)", count)
	}
	if lowest != 2 {
		t.Errorf("oldest retained seq = %d, want 2 — the 51st save drops the 1st", lowest)
	}

	// And the pruned version is refused with a sentence that says WHY it is
	// gone, which is a different fact from "there is no such version".
	rr := pagesRollback(t, h, wsID, userID, 1)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("rollback to a pruned version: status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no longer retained") {
		t.Errorf("the refusal does not distinguish an aged-out version from one that never existed: %s", rr.Body.String())
	}

	// A rollback is itself a save, so it prunes too: 52 saves, still 50 rows.
	if rr := pagesRollback(t, h, wsID, userID, 2); rr.Code != http.StatusOK {
		t.Fatalf("rollback to the oldest retained version: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if err := h.db.QueryRow(`
		SELECT COUNT(*), COALESCE(MIN(seq), 0)
		FROM page_versions v JOIN pages p ON p.id = v.page_id
		WHERE p.workspace_id = ? AND p.slug = ?`, wsID, pagesVerSlug).Scan(&count, &lowest); err != nil {
		t.Fatalf("count versions after rollback: %v", err)
	}
	if count != 50 || lowest != 3 {
		t.Errorf("after the rollback: %d rows starting at seq %d, want 50 starting at 3 — "+
			"a rollback is a save and prunes like one", count, lowest)
	}
}

// TestPageRollback_RefusesAVersionThatNeverExisted keeps the two "not found"
// sentences apart from each other.
func TestPageRollback_RefusesAVersionThatNeverExisted(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesVersionCreate(t, h, wsID, userID, pagesVersionSpec("v1", "status.v1", "script/c.sh", true))

	rr := pagesRollback(t, h, wsID, userID, 99)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "retained range") {
		t.Errorf("the refusal does not say what range IS available: %s", rr.Body.String())
	}
}

func pagesListNames(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
