package api

// Precedence between a builtin crew template and a workspace template of the
// same slug (#1796).
//
// crew_templates.slug used to carry a global UNIQUE, which meant the predicate
// every by-slug lookup shares —
//
//	WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)
//
// — could match at most one row. Scoping that constraint to the workspace is
// what makes two matches possible, and QueryRow with no ORDER BY takes
// whichever row SQLite yields first. These tests pin the rule that replaces
// the accident: THE MORE SPECIFIC ROW WINS — a workspace template shadows the
// builtin of the same slug, for that workspace only.
//
// Every by-slug reader is covered, because the failure mode of missing one is
// not an error but an inconsistency: List showing the override while Deploy
// builds the builtin.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/services"
)

// shadowSlug is the one slug both rows claim in every test below.
const shadowSlug = "shadow-team"

// shadowAgentsJSON builds an agents_json roster of the given size, so a caller
// can tell which of the two rows a code path actually read by counting agents.
func shadowAgentsJSON(t *testing.T, n int) string {
	t.Helper()
	agents := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		slug := string(rune('a'+i)) + "-agent"
		agents = append(agents, map[string]any{
			"name":          slug,
			"slug":          slug,
			"role_title":    "Engineer",
			"agent_role":    "AGENT",
			"cli_adapter":   "CLAUDE_CODE",
			"llm_provider":  "ANTHROPIC",
			"llm_model":     "claude-haiku-4-5",
			"tool_profile":  "CODING",
			"system_prompt": "do work",
		})
	}
	b, err := json.Marshal(agents)
	if err != nil {
		t.Fatalf("marshal agents: %v", err)
	}
	return string(b)
}

// seedShadowPair writes the collision: a builtin (workspace_id NULL, one
// agent) and wsID's own template under the SAME slug (two agents). Before
// #1796 the second INSERT was rejected by the global UNIQUE, which is exactly
// why no lookup ever needed a tie-break.
func seedShadowPair(t *testing.T, db *sql.DB, wsID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-shadow-builtin', 'Shipped Team', ?, 'GENERAL', ?, 1, NULL)`,
		shadowSlug, shadowAgentsJSON(t, 1)); err != nil {
		t.Fatalf("seed builtin template: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-shadow-ws', 'Our Team', ?, 'CUSTOM', ?, 0, ?)`,
		shadowSlug, shadowAgentsJSON(t, 2), wsID); err != nil {
		t.Fatalf("seed workspace template: %v", err)
	}
}

// seedShadowOtherWorkspace adds a SECOND tenant that has no template of its
// own, so tests can assert the override is scoped: it must not follow the slug
// into a workspace that never created it.
func seedShadowOtherWorkspace(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	const wsID = "ws-shadow-other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, wsID); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-shadow-other', ?, ?, 'OWNER')`,
		wsID, userID); err != nil {
		t.Fatalf("insert other member: %v", err)
	}
	return wsID
}

// ---------------------------------------------------------------------------
// Get — GET /api/v1/crew-templates/{slug}
// ---------------------------------------------------------------------------

func TestCrewTemplateShadow_Get(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	otherWS := seedShadowOtherWorkspace(t, db, userID)
	seedShadowPair(t, db, wsID)

	get := func(callerWS string) crewTemplateResponse {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/crew-templates/"+shadowSlug, nil)
		req.SetPathValue("slug", shadowSlug)
		req = withWorkspaceUser(req, userID, callerWS, "OWNER")
		rr := httptest.NewRecorder()
		NewCrewTemplateHandler(db, newTestLogger()).Get(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("get as %s = %d, body: %s", callerWS, rr.Code, rr.Body.String())
		}
		var got crewTemplateResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return got
	}

	owner := get(wsID)
	if owner.ID != "ct-shadow-ws" {
		t.Errorf("Get returned %q (%s), want the workspace template ct-shadow-ws — "+
			"without an ORDER BY the row is whichever SQLite yields first", owner.ID, owner.Name)
	}
	if owner.IsBuiltin || len(owner.Agents) != 2 {
		t.Errorf("Get returned is_builtin=%v with %d agents, want the 2-agent override",
			owner.IsBuiltin, len(owner.Agents))
	}

	// The override belongs to one tenant. Another workspace asking for the
	// same slug must still get the builtin.
	other := get(otherWS)
	if other.ID != "ct-shadow-builtin" {
		t.Errorf("a workspace with no template of its own got %q — the override "+
			"leaked across tenants", other.ID)
	}
}

// ---------------------------------------------------------------------------
// Deploy — POST /api/v1/crew-templates/{slug}/deploy (deployCrewTemplate)
// ---------------------------------------------------------------------------

func TestCrewTemplateShadow_Deploy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedShadowPair(t, db, wsID)

	body := strings.NewReader(`{"crew_name":"Deployed","crew_slug":"deployed"}`)
	req := httptest.NewRequest("POST", "/api/v1/crew-templates/"+shadowSlug+"/deploy", body)
	req.SetPathValue("slug", shadowSlug)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	NewCrewTemplateHandler(db, newTestLogger()).Deploy(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("deploy = %d, body: %s", rr.Code, rr.Body.String())
	}

	var got deployCrewResult
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The agent count is the tell: the builtin carries one agent, the
	// override two. A crew built from the wrong row is not an error the
	// operator ever sees — it is simply the wrong team.
	if got.AgentCount != 2 {
		t.Errorf("deployed crew has %d agents, want 2 — Deploy built the crew from "+
			"the builtin the workspace overrode", got.AgentCount)
	}
}

// ---------------------------------------------------------------------------
// lookupCrewTemplate — the hire path (agents_hire.go)
// ---------------------------------------------------------------------------

func TestCrewTemplateShadow_HireLookup(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedShadowPair(t, db, wsID)

	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/agents/hire", nil)
	name, _, err := h.lookupCrewTemplate(req, wsID, shadowSlug)
	if err != nil {
		t.Fatalf("lookupCrewTemplate: %v", err)
	}
	if name != "Our Team" {
		t.Errorf("hire resolved template name %q, want %q (the workspace override)", name, "Our Team")
	}
}

// ---------------------------------------------------------------------------
// Onboarding — the crew-name default (onboarding.go)
// ---------------------------------------------------------------------------

func TestCrewTemplateShadow_OnboardingCrewNameDefault(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedShadowPair(t, db, wsID)

	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	// No crew_name → the handler names the crew after the template it is
	// about to deploy. It must read the same row deployCrewTemplate does,
	// or the crew is named after a template it was not built from.
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_template_slug":"`+shadowSlug+`"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("onboarding setup = %d, body: %s", w.Code, w.Body.String())
	}

	var crewName string
	var agents int
	if err := db.QueryRow(
		`SELECT c.name, (SELECT COUNT(*) FROM agents a WHERE a.crew_id = c.id)
		 FROM crews c WHERE c.workspace_id = ?`, wsID).Scan(&crewName, &agents); err != nil {
		t.Fatalf("read deployed crew: %v", err)
	}
	if crewName != "Our Team" {
		t.Errorf("onboarding named the crew %q, want %q (the workspace override)", crewName, "Our Team")
	}
	if agents != 2 {
		t.Errorf("onboarding deployed %d agents, want 2 — the name and the roster came "+
			"from different rows", agents)
	}
}

// ---------------------------------------------------------------------------
// List — GET /api/v1/crew-templates
// ---------------------------------------------------------------------------

func TestCrewTemplateShadow_ListReturnsEachSlugOnce(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	otherWS := seedShadowOtherWorkspace(t, db, userID)
	seedShadowPair(t, db, wsID)

	list := func(callerWS string) []crewTemplateResponse {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
		req = withWorkspaceUser(req, userID, callerWS, "OWNER")
		rr := httptest.NewRecorder()
		NewCrewTemplateHandler(db, newTestLogger()).List(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("list as %s = %d, body: %s", callerWS, rr.Code, rr.Body.String())
		}
		var out []crewTemplateResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	var matches []crewTemplateResponse
	for _, tmpl := range list(wsID) {
		if tmpl.Slug == shadowSlug {
			matches = append(matches, tmpl)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("list returned %d rows for slug %q, want 1 — the catalogue shows a "+
			"duplicate whose second copy Get and Deploy both refuse to resolve to",
			len(matches), shadowSlug)
	}
	if matches[0].ID != "ct-shadow-ws" {
		t.Errorf("list kept %q, want the workspace override ct-shadow-ws", matches[0].ID)
	}

	// Shadowing is per-workspace: the other tenant still sees the builtin,
	// exactly once.
	matches = nil
	for _, tmpl := range list(otherWS) {
		if tmpl.Slug == shadowSlug {
			matches = append(matches, tmpl)
		}
	}
	if len(matches) != 1 || matches[0].ID != "ct-shadow-builtin" {
		t.Errorf("other workspace saw %d row(s) %v for %q, want just the builtin",
			len(matches), matches, shadowSlug)
	}
}

// TestCrewTemplateShadow_ListUnshadowedBuiltinSurvives is the mutation guard
// for the NOT EXISTS clause above: a filter that drops builtins whenever the
// workspace owns ANY template — rather than one of the same slug — passes
// every assertion in the test above while quietly emptying the catalogue.
func TestCrewTemplateShadow_ListUnshadowedBuiltinSurvives(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedShadowPair(t, db, wsID)

	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-shadow-lonely', 'Lonely Builtin', 'lonely-team', 'GENERAL', ?, 1, NULL)`,
		shadowAgentsJSON(t, 1)); err != nil {
		t.Fatalf("seed unshadowed builtin: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	NewCrewTemplateHandler(db, newTestLogger()).List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d, body: %s", rr.Code, rr.Body.String())
	}
	var out []crewTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, tmpl := range out {
		if tmpl.Slug == "lonely-team" {
			found = true
		}
	}
	if !found {
		t.Error("a builtin nobody overrode vanished from the list — the shadow filter " +
			"is keyed on something other than the slug")
	}
}

// TestCrewTemplateShadow_SeederNoLongerSuppressedByUserSlug records the
// behaviour change that falls out of the migration. Under the global UNIQUE, a
// user template holding slug X made the builtin X unseedable forever: the
// seeder's UPDATE matched nothing (is_builtin = 1 filters the user row out)
// and its INSERT OR IGNORE then collided with the global UNIQUE and gave up
// silently. Now both rows coexist and the user's row shadows the builtin.
func TestCrewTemplateShadow_SeederNoLongerSuppressedByUserSlug(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// A user template squatting a slug the embedded roster also ships.
	// software-development is the slug onboarding's own tests deploy, so it
	// is guaranteed to be in the builtin roster.
	const builtinSlug = "software-development"
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-squatter', 'Squatter', ?, 'CUSTOM', ?, 0, ?)`,
		builtinSlug, shadowAgentsJSON(t, 2), wsID); err != nil {
		t.Fatalf("seed squatting user template: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	NewCrewTemplateHandler(db, newTestLogger()).List(rr, req) // List seeds builtins first
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d, body: %s", rr.Code, rr.Body.String())
	}

	var builtins int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM crew_templates WHERE slug = ? AND is_builtin = 1 AND workspace_id IS NULL`,
		builtinSlug).Scan(&builtins); err != nil {
		t.Fatalf("count builtin rows: %v", err)
	}
	if builtins != 1 {
		t.Errorf("builtin %q seeded %d time(s), want 1 — a user template holding the "+
			"slug must no longer suppress it", builtinSlug, builtins)
	}
}
