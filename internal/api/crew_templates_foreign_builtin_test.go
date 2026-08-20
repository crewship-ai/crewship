package api

// Cross-tenant isolation of the by-slug crew-template lookup when ANOTHER
// workspace owns a row that carries is_builtin = 1 (#1796).
//
// The shadowing tests next door (crew_templates_shadow_test.go) cover the two
// row classes the feature is about: a real builtin (workspace_id NULL) and the
// caller's own override. This file covers the third class, the one the schema
// used to make unreachable:
//
//	is_builtin = 1 AND workspace_id IS NOT NULL
//
// Nothing in the schema forbids it — is_builtin is a plain INTEGER DEFAULT 0
// with no CHECK tying it to workspace_id, and the API's own create path lets a
// workspace-owned row be written. What USED to make it harmless was the global
// UNIQUE(slug): a foreign row could never share a slug with a builtin, so
//
//	WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)
//
// could not return somebody else's template. Splitting that UNIQUE into two
// partial indexes (one per (workspace_id, slug), one per slug for the builtins)
// is exactly what makes the collision expressible — so the predicate has to say
// what the indexes say:
//
//	WHERE slug = ? AND ((is_builtin = 1 AND workspace_id IS NULL) OR workspace_id = ?)
//
// Without that, the foreign row not only matches, it sorts at 0 under
// `ORDER BY (workspace_id IS NULL)` — ahead of the genuine builtin and TIED
// with the caller's own override, so which tenant's template a caller gets is
// decided by SQLite's row order. And List, which drops a shadowed builtin only
// when `ct.workspace_id IS NULL`, emits the foreign row as a second copy of the
// slug outright.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// foreignSlug is the one slug all three rows below claim.
const foreignSlug = "shared-team"

// foreignWorkspaceID owns the offending row. The caller is not a member of it
// and never sees it in any other surface.
const foreignWorkspaceID = "ws-foreign-tenant"

// seedForeignBuiltinCollision writes the three-way collision on foreignSlug:
//
//	ct-foreign  — another tenant's row, is_builtin = 1, 3 agents
//	ct-builtin  — the genuine builtin, workspace_id NULL, 1 agent
//	ct-mine     — the caller's own override, 2 agents
//
// The foreign row is inserted FIRST on purpose: under the buggy predicate it
// ties with the caller's own row on the ORDER BY key, and insertion order is
// what breaks the tie in practice. Seeding it first makes the leak reproduce
// rather than hide behind a lucky scan order.
func seedForeignBuiltinCollision(t *testing.T, db *sql.DB, wsID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign')`,
		foreignWorkspaceID); err != nil {
		t.Fatalf("insert foreign workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-foreign', 'FOREIGN', ?, 'CUSTOM', ?, 1, ?)`,
		foreignSlug, shadowAgentsJSON(t, 3), foreignWorkspaceID); err != nil {
		t.Fatalf("seed foreign template: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-builtin', 'Shipped Team', ?, 'GENERAL', ?, 1, NULL)`,
		foreignSlug, shadowAgentsJSON(t, 1)); err != nil {
		t.Fatalf("seed builtin template: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-mine', 'Our Team', ?, 'CUSTOM', ?, 0, ?)`,
		foreignSlug, shadowAgentsJSON(t, 2), wsID); err != nil {
		t.Fatalf("seed caller template: %v", err)
	}
}

// TestCrewTemplateForeignBuiltin_GetResolvesOwnRow pins the single-row lookup:
// the caller must land on their own override, never on the foreign row that
// claims to be a builtin.
func TestCrewTemplateForeignBuiltin_GetResolvesOwnRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedForeignBuiltinCollision(t, db, wsID)

	req := httptest.NewRequest("GET", "/api/v1/crew-templates/"+foreignSlug, nil)
	req.SetPathValue("slug", foreignSlug)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	NewCrewTemplateHandler(db, newTestLogger()).Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d, body: %s", rr.Code, rr.Body.String())
	}
	var got crewTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID == "ct-foreign" {
		t.Fatalf("Get returned %q (%s) — another workspace's template, leaked because "+
			"is_builtin = 1 was treated as if it implied workspace_id IS NULL", got.ID, got.Name)
	}
	if got.ID != "ct-mine" {
		t.Errorf("Get returned %q (%s), want the caller's own override ct-mine", got.ID, got.Name)
	}
}

// TestCrewTemplateForeignBuiltin_GetFallsBackToRealBuiltin is the other half:
// a workspace with no template of its own must get the GENUINE builtin, not
// the foreign row that outranks it under the tie-break.
func TestCrewTemplateForeignBuiltin_GetFallsBackToRealBuiltin(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	otherWS := seedShadowOtherWorkspace(t, db, userID)
	seedForeignBuiltinCollision(t, db, wsID)

	req := httptest.NewRequest("GET", "/api/v1/crew-templates/"+foreignSlug, nil)
	req.SetPathValue("slug", foreignSlug)
	req = withWorkspaceUser(req, userID, otherWS, "OWNER")
	rr := httptest.NewRecorder()
	NewCrewTemplateHandler(db, newTestLogger()).Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d, body: %s", rr.Code, rr.Body.String())
	}
	var got crewTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "ct-builtin" {
		t.Errorf("a workspace with no template of its own resolved %q (%s), want the real "+
			"builtin ct-builtin — the foreign row sorts at 0 and outranks it", got.ID, got.Name)
	}
}

// TestCrewTemplateForeignBuiltin_HireLookupResolvesOwnRow covers
// agents_hire.go's lookupCrewTemplate, which shares the same predicate.
func TestCrewTemplateForeignBuiltin_HireLookupResolvesOwnRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedForeignBuiltinCollision(t, db, wsID)

	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/agents/hire", nil)
	name, _, err := h.lookupCrewTemplate(req, wsID, foreignSlug)
	if err != nil {
		t.Fatalf("lookupCrewTemplate: %v", err)
	}
	if name == "FOREIGN" {
		t.Fatalf("hire resolved another workspace's template %q", name)
	}
	if name != "Our Team" {
		t.Errorf("hire resolved template name %q, want %q (the caller's own override)", name, "Our Team")
	}
}

// TestCrewTemplateForeignBuiltin_DeployBuildsOwnRoster covers
// deployCrewTemplate. The agent count is the tell: 3 is the foreign roster, 1
// the builtin, 2 the caller's own.
func TestCrewTemplateForeignBuiltin_DeployBuildsOwnRoster(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedForeignBuiltinCollision(t, db, wsID)

	body := strings.NewReader(`{"crew_name":"Deployed","crew_slug":"deployed"}`)
	req := httptest.NewRequest("POST", "/api/v1/crew-templates/"+foreignSlug+"/deploy", body)
	req.SetPathValue("slug", foreignSlug)
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
	if got.AgentCount == 3 {
		t.Fatalf("deploy built the crew from the foreign tenant's 3-agent roster")
	}
	if got.AgentCount != 2 {
		t.Errorf("deployed crew has %d agents, want 2 (the caller's own override)", got.AgentCount)
	}
}

// TestCrewTemplateForeignBuiltin_ListHidesForeignRow is the list-shaped half.
// The NOT EXISTS shadow filter only drops builtins with workspace_id IS NULL,
// so a foreign row carrying is_builtin = 1 is emitted alongside the caller's
// own — the slug appears twice and one of the two copies is another tenant's
// template, name and roster included.
func TestCrewTemplateForeignBuiltin_ListHidesForeignRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedForeignBuiltinCollision(t, db, wsID)

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

	var matches []string
	for _, tmpl := range out {
		if tmpl.ID == "ct-foreign" {
			t.Errorf("list leaked another workspace's template %q (%s)", tmpl.ID, tmpl.Name)
		}
		if tmpl.Slug == foreignSlug {
			matches = append(matches, tmpl.ID)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("list returned %d rows for slug %q (%v), want exactly 1", len(matches), foreignSlug, matches)
	}
	if matches[0] != "ct-mine" {
		t.Errorf("list kept %q for %q, want the caller's own override ct-mine", matches[0], foreignSlug)
	}
}

// TestCrewTemplateForeignBuiltin_SeederLeavesForeignRowAlone covers the write
// side of the same confusion. SeedBuiltinCrewTemplates updates by
// `slug = ? AND is_builtin = 1`, and List runs it on every request — so a
// foreign row carrying is_builtin = 1 under a slug the embedded roster also
// ships gets its name and roster overwritten with the builtin's, and the
// non-zero rowsAffected then suppresses the insert of the real builtin.
func TestCrewTemplateForeignBuiltin_SeederLeavesForeignRowAlone(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign')`,
		foreignWorkspaceID); err != nil {
		t.Fatalf("insert foreign workspace: %v", err)
	}
	// software-development is in the embedded roster, so the seeder will look
	// for a builtin under this slug on the very next List.
	const builtinSlug = "software-development"
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-foreign', 'FOREIGN', ?, 'CUSTOM', ?, 1, ?)`,
		builtinSlug, shadowAgentsJSON(t, 3), foreignWorkspaceID); err != nil {
		t.Fatalf("seed foreign template: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	NewCrewTemplateHandler(db, newTestLogger()).List(rr, req) // List seeds builtins first
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d, body: %s", rr.Code, rr.Body.String())
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM crew_templates WHERE id = 'ct-foreign'`).Scan(&name); err != nil {
		t.Fatalf("read foreign row: %v", err)
	}
	if name != "FOREIGN" {
		t.Errorf("the seeder rewrote another workspace's template (name is now %q) — "+
			"is_builtin = 1 was read as if it meant workspace_id IS NULL", name)
	}

	var builtins int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM crew_templates WHERE slug = ? AND is_builtin = 1 AND workspace_id IS NULL`,
		builtinSlug).Scan(&builtins); err != nil {
		t.Fatalf("count builtin rows: %v", err)
	}
	if builtins != 1 {
		t.Errorf("builtin %q seeded %d time(s), want 1 — the foreign row absorbed the "+
			"seeder's UPDATE and the real builtin was never inserted", builtinSlug, builtins)
	}
}
