package api

// Coverage for crews_create.go (Create) and crews_update.go (Update).
//
// Mirrors the harness in core_handlers_test.go: setupTestDB / seedTestUser /
// seedTestWorkspace / seedCrewRow / NewCrewHandler / newTestLogger /
// withWorkspaceUser. New helpers are prefixed covCru; all test funcs are
// prefixed TestCovCru to avoid clashing with the existing crew tests.
//
// SKIPPED branches (require Docker / network / a live registry):
//   - createCrewRequest.RuntimeImage / updateCrewRequest.RuntimeImage →
//     devcontainer.ValidateImageExists makes an outbound registry call.
//   - restartCrewContainer goroutine (fired async on network/services
//     change) needs the docker provider + socket; we assert the 200/DB
//     write instead of the container restart.

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covCruNewCrew builds a CrewHandler over a freshly seeded user+workspace
// and returns (handler, userID, wsID) so each test starts from a clean DB.
func covCruNewCrew(t *testing.T) (*CrewHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewCrewHandler(db, newTestLogger()), db, userID, wsID
}

// covCruDoCreate POSTs a create body and returns the recorder.
func covCruDoCreate(h *CrewHandler, userID, wsID, role, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/crews", bytes.NewBufferString(body))
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

// covCruDoUpdate PATCHes a body against crewID and returns the recorder.
func covCruDoUpdate(h *CrewHandler, crewID, userID, wsID, role, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID, bytes.NewBufferString(body))
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestCovCruCreate_Forbidden(t *testing.T) {
	h, _, userID, wsID := covCruNewCrew(t)
	rr := covCruDoCreate(h, userID, wsID, "VIEWER", `{"name":"Engineering","slug":"engineering"}`)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", rr.Code)
	}
}

func TestCovCruCreate_BadJSON(t *testing.T) {
	h, _, userID, wsID := covCruNewCrew(t)
	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{not json`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d, want 400", rr.Code)
	}
}

func TestCovCruCreate_Validation(t *testing.T) {
	h, _, userID, wsID := covCruNewCrew(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty name", `{"name":"","slug":"engineering"}`, http.StatusBadRequest},
		{"name too short", `{"name":"A","slug":"engineering"}`, http.StatusBadRequest},
		{"empty slug", `{"name":"Engineering","slug":""}`, http.StatusBadRequest},
		{"slug too short", `{"name":"Engineering","slug":"a"}`, http.StatusBadRequest},
		{"slug bad chars", `{"name":"Engineering","slug":"Bad Slug!"}`, http.StatusBadRequest},
		{"bad network_mode", `{"name":"Engineering","slug":"engineering","network_mode":"yolo"}`, http.StatusBadRequest},
		{"bad domain", `{"name":"Engineering","slug":"engineering","network_mode":"restricted","allowed_domains":["not a domain"]}`, http.StatusBadRequest},
		{"devcontainer too big", `{"name":"Engineering","slug":"engineering","devcontainer_config":"` + covCruRepeat("x", 102401) + `"}`, http.StatusBadRequest},
		{"mise too big", `{"name":"Engineering","slug":"engineering","mise_config":"` + covCruRepeat("x", 10241) + `"}`, http.StatusBadRequest},
		{"bad devcontainer json", `{"name":"Engineering","slug":"engineering","devcontainer_config":"{not json"}`, http.StatusBadRequest},
		{"bad mise toml", `{"name":"Engineering","slug":"engineering","mise_config":"= = ="}`, http.StatusBadRequest},
		{"services too big", `{"name":"Engineering","slug":"engineering","services_json":"` + covCruRepeat("x", 64*1024+1) + `"}`, http.StatusBadRequest},
		{"bad services json", `{"name":"Engineering","slug":"engineering","services_json":"[{\"image\":\"redis:7\"}]"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := covCruDoCreate(h, userID, wsID, "OWNER", tc.body)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestCovCruCreate_DuplicateSlug(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "dup-existing", wsID, "Existing", "engineering")

	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{"name":"Engineering","slug":"engineering"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate slug = %d, want 409", rr.Code)
	}
}

func TestCovCruCreate_Happy_DefaultsAndDBRow(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)

	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Engineering","slug":"engineering","description":"the eng crew"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	// Assert the persisted DB row: defaults for resource limits + network mode.
	var (
		name, slug, networkMode string
		memMB                   int
		cpus                    float64
		ttl                     sql.NullInt64
		domains                 sql.NullString
	)
	err := db.QueryRow(`SELECT name, slug, container_memory_mb, container_cpus,
		container_ttl_hours, network_mode, allowed_domains
		FROM crews WHERE workspace_id = ? AND slug = ?`, wsID, "engineering").
		Scan(&name, &slug, &memMB, &cpus, &ttl, &networkMode, &domains)
	if err != nil {
		t.Fatalf("read created crew: %v", err)
	}
	if name != "Engineering" || slug != "engineering" {
		t.Errorf("name/slug = %q/%q", name, slug)
	}
	if memMB != 4096 || cpus != 2.0 {
		t.Errorf("resource defaults = %d MB / %v cpus, want 4096 / 2.0", memMB, cpus)
	}
	if ttl.Valid {
		t.Errorf("ttl = %v, want NULL by default", ttl.Int64)
	}
	if networkMode != "free" {
		t.Errorf("network_mode = %q, want free", networkMode)
	}
	if domains.Valid {
		t.Errorf("allowed_domains = %q, want NULL in free mode", domains.String)
	}
}

func TestCovCruCreate_Happy_CustomLimitsAndRestrictedDomains(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)

	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{
		"name":"Restricted Crew","slug":"restricted","container_memory_mb":2048,
		"container_cpus":1.5,"container_ttl_hours":12,"network_mode":"RESTRICTED",
		"allowed_domains":["https://API.GitHub.com/v3","npmjs.org"],
		"services_json":"[{\"name\":\"redis\",\"image\":\"redis:7\"}]"
	}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	var (
		memMB        int
		cpus         float64
		ttl          sql.NullInt64
		networkMode  string
		domains      sql.NullString
		servicesJSON sql.NullString
	)
	err := db.QueryRow(`SELECT container_memory_mb, container_cpus, container_ttl_hours,
		network_mode, allowed_domains, services_json
		FROM crews WHERE workspace_id = ? AND slug = ?`, wsID, "restricted").
		Scan(&memMB, &cpus, &ttl, &networkMode, &domains, &servicesJSON)
	if err != nil {
		t.Fatalf("read created crew: %v", err)
	}
	if memMB != 2048 || cpus != 1.5 {
		t.Errorf("custom limits = %d / %v, want 2048 / 1.5", memMB, cpus)
	}
	if !ttl.Valid || ttl.Int64 != 12 {
		t.Errorf("ttl = %v, want 12", ttl)
	}
	if networkMode != "restricted" {
		t.Errorf("network_mode = %q, want restricted (lowercased)", networkMode)
	}
	// Domains should be normalized (host-only, lowercased) and stored as JSON.
	if !domains.Valid {
		t.Fatalf("allowed_domains is NULL, want normalized JSON")
	}
	for _, want := range []string{"api.github.com", "npmjs.org"} {
		if !bytes.Contains([]byte(domains.String), []byte(want)) {
			t.Errorf("allowed_domains %q missing %q", domains.String, want)
		}
	}
	if !servicesJSON.Valid || servicesJSON.String == "" {
		t.Errorf("services_json not persisted: %v", servicesJSON)
	}
}

func TestCovCruCreate_DBError500(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	// Fault injection: close the DB so the INSERT (and the pre-INSERT slug
	// SELECT) fail → 500.
	db.Close()

	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{"name":"Engineering","slug":"engineering"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("create with closed db = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestCovCruUpdate_Forbidden(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "cru-forbid", wsID, "Crew", "crew")
	rr := covCruDoUpdate(h, "cru-forbid", userID, wsID, "VIEWER", `{"name":"New"}`)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer update = %d, want 403", rr.Code)
	}
}

func TestCovCruUpdate_MissingCrewID(t *testing.T) {
	h, _, userID, wsID := covCruNewCrew(t)
	// Empty path value → 400.
	req := httptest.NewRequest("PATCH", "/api/v1/crews/", bytes.NewBufferString(`{"name":"New"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing crewId = %d, want 400", rr.Code)
	}
}

func TestCovCruUpdate_NotFound(t *testing.T) {
	h, _, userID, wsID := covCruNewCrew(t)
	rr := covCruDoUpdate(h, "ghost", userID, wsID, "OWNER", `{"name":"New"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("missing crew = %d, want 404", rr.Code)
	}
}

func TestCovCruUpdate_BadJSON(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "cru-badjson", wsID, "Crew", "crew")
	rr := covCruDoUpdate(h, "cru-badjson", userID, wsID, "OWNER", `{not json`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d, want 400", rr.Code)
	}
}

func TestCovCruUpdate_InvalidValues(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "cru-inval", wsID, "Crew", "crew")

	cases := []struct {
		name string
		body string
		want int
	}{
		{"name too short", `{"name":"A"}`, http.StatusBadRequest},
		{"name too long", `{"name":"` + covCruRepeat("a", 101) + `"}`, http.StatusBadRequest},
		{"slug too short", `{"slug":"a"}`, http.StatusBadRequest},
		{"slug bad chars", `{"slug":"Bad Slug"}`, http.StatusBadRequest},
		{"devcontainer too big", `{"devcontainer_config":"` + covCruRepeat("x", 102401) + `"}`, http.StatusBadRequest},
		{"mise too big", `{"mise_config":"` + covCruRepeat("x", 10241) + `"}`, http.StatusBadRequest},
		{"bad devcontainer json", `{"devcontainer_config":"{not json"}`, http.StatusBadRequest},
		{"bad mise toml", `{"mise_config":"= = ="}`, http.StatusBadRequest},
		{"services too big", `{"services_json":"` + covCruRepeat("x", 64*1024+1) + `"}`, http.StatusBadRequest},
		{"bad services json", `{"services_json":"[{\"image\":\"redis:7\"}]"}`, http.StatusBadRequest},
		{"bad network_mode", `{"network_mode":"yolo"}`, http.StatusBadRequest},
		{"bad domain", `{"network_mode":"restricted","allowed_domains":["not a domain"]}`, http.StatusBadRequest},
		{"negative ttl", `{"container_ttl_hours":-1}`, http.StatusBadRequest},
		{"max_ephemeral negative", `{"max_ephemeral_agents":-1}`, http.StatusBadRequest},
		{"max_ephemeral too high", `{"max_ephemeral_agents":101}`, http.StatusBadRequest},
		{"bad mcp json", `{"mcp_config_json":"not json"}`, http.StatusBadRequest},
		{"mcp missing mcpServers", `{"mcp_config_json":"{\"foo\":1}"}`, http.StatusBadRequest},
		{"bad escalation json", `{"escalation_config":"not json"}`, http.StatusBadRequest},
		{"escalation out of range", `{"escalation_config":"{\"auto_approve_threshold\":2.0}"}`, http.StatusBadRequest},
		{"escalation auto<=require", `{"escalation_config":"{\"auto_approve_threshold\":0.3,\"require_approval_below\":0.4}"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := covCruDoUpdate(h, "cru-inval", userID, wsID, "OWNER", tc.body)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestCovCruUpdate_DuplicateSlug(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "cru-a", wsID, "Alpha", "alpha")
	seedCrewRow(t, db, "cru-b", wsID, "Beta", "beta")

	rr := covCruDoUpdate(h, "cru-b", userID, wsID, "OWNER", `{"slug":"alpha"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("rename to taken slug = %d, want 409", rr.Code)
	}
}

func TestCovCruUpdate_Happy_EverySettableField(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "cru-happy", wsID, "Crew", "crew")

	body := `{
		"name":"Renamed Crew","slug":"renamed","description":"desc","color":"#fff",
		"icon":"rocket","avatar_style":"adventurer","container_memory_mb":8192,
		"container_cpus":4,"container_ttl_hours":24,"max_ephemeral_agents":5,
		"issue_prefix":"DEV",
		"mcp_config_json":"{\"mcpServers\":{}}",
		"escalation_config":"{\"auto_approve_threshold\":0.9,\"notify_threshold\":0.5,\"require_approval_below\":0.3}",
		"devcontainer_config":"{\"image\":\"debian:bookworm\"}",
		"mise_config":"[tools]\nnode = \"20\"",
		"network_mode":"restricted","allowed_domains":["GitHub.com"]
	}`
	rr := covCruDoUpdate(h, "cru-happy", userID, wsID, "OWNER", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	var (
		name, slug, networkMode string
		memMB, ttl, maxEph      int
		cpus                    float64
		avatarStyle             sql.NullString
		issuePrefix             sql.NullString
		mcp                     sql.NullString
		escalation              sql.NullString
		domains                 sql.NullString
	)
	err := db.QueryRow(`SELECT name, slug, network_mode, container_memory_mb,
		container_ttl_hours, max_ephemeral_agents, container_cpus, avatar_style,
		issue_prefix, mcp_config_json, escalation_config, allowed_domains
		FROM crews WHERE id = ?`, "cru-happy").
		Scan(&name, &slug, &networkMode, &memMB, &ttl, &maxEph, &cpus,
			&avatarStyle, &issuePrefix, &mcp, &escalation, &domains)
	if err != nil {
		t.Fatalf("read updated crew: %v", err)
	}
	if name != "Renamed Crew" || slug != "renamed" {
		t.Errorf("name/slug = %q/%q", name, slug)
	}
	if memMB != 8192 || cpus != 4 || ttl != 24 || maxEph != 5 {
		t.Errorf("limits = mem %d cpus %v ttl %d maxEph %d", memMB, cpus, ttl, maxEph)
	}
	if networkMode != "restricted" {
		t.Errorf("network_mode = %q, want restricted", networkMode)
	}
	if avatarStyle.String != "adventurer" || issuePrefix.String != "DEV" {
		t.Errorf("avatar/prefix = %q/%q", avatarStyle.String, issuePrefix.String)
	}
	if !mcp.Valid || !escalation.Valid {
		t.Errorf("mcp/escalation not persisted: %v / %v", mcp, escalation)
	}
	if !domains.Valid || !bytes.Contains([]byte(domains.String), []byte("github.com")) {
		t.Errorf("allowed_domains = %v, want normalized github.com", domains)
	}
}

func TestCovCruUpdate_TTLZeroClearsAndNetworkFreeClearsDomains(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	// Seed a restricted crew with domains so we can watch free-mode clear them.
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode,
		container_memory_mb, container_cpus, container_ttl_hours, allowed_domains)
		VALUES ('cru-clear', ?, 'Crew', 'crew', 'restricted', 4096, 2.0, 5, '["github.com"]')`, wsID)
	if err != nil {
		t.Fatalf("seed restricted crew: %v", err)
	}

	// ttl=0 clears the column; network_mode=free nulls allowed_domains.
	rr := covCruDoUpdate(h, "cru-clear", userID, wsID, "OWNER",
		`{"container_ttl_hours":0,"network_mode":"free"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	var ttl, domains sql.NullString
	var mode string
	if err := db.QueryRow(`SELECT container_ttl_hours, network_mode, allowed_domains
		FROM crews WHERE id = ?`, "cru-clear").Scan(&ttl, &mode, &domains); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if ttl.Valid {
		t.Errorf("ttl = %q, want NULL after ttl=0", ttl.String)
	}
	if mode != "free" {
		t.Errorf("network_mode = %q, want free", mode)
	}
	if domains.Valid {
		t.Errorf("allowed_domains = %q, want NULL in free mode", domains.String)
	}
}

func TestCovCruUpdate_ClearOptionalFields(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode,
		container_memory_mb, container_cpus, issue_prefix, escalation_config,
		devcontainer_config, mise_config, services_json)
		VALUES ('cru-opt', ?, 'Crew', 'crew', 'free', 4096, 2.0, 'OLD',
			'{"notify_threshold":0.5}', '{"image":"debian:bookworm"}', '[tools]', '[]')`, wsID)
	if err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Empty strings clear issue_prefix / escalation / configs / services.
	rr := covCruDoUpdate(h, "cru-opt", userID, wsID, "OWNER",
		`{"issue_prefix":"","escalation_config":"","devcontainer_config":"","mise_config":"","services_json":"  "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	var prefix, esc, dev, mise, svc sql.NullString
	if err := db.QueryRow(`SELECT issue_prefix, escalation_config, devcontainer_config,
		mise_config, services_json FROM crews WHERE id = ?`, "cru-opt").
		Scan(&prefix, &esc, &dev, &mise, &svc); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	for label, v := range map[string]sql.NullString{
		"issue_prefix": prefix, "escalation_config": esc,
		"devcontainer_config": dev, "mise_config": mise, "services_json": svc,
	} {
		if v.Valid {
			t.Errorf("%s = %q, want NULL after clear", label, v.String)
		}
	}
}

func TestCovCruUpdate_DBError500(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	seedCrewRow(t, db, "cru-500", wsID, "Crew", "crew")
	// Fault injection: close the DB so the crewExists check (first DB hit
	// after the role gate) fails → 500.
	db.Close()

	rr := covCruDoUpdate(h, "cru-500", userID, wsID, "OWNER", `{"name":"New"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("update with closed db = %d, want 500", rr.Code)
	}
}

// covCruRepeat returns s repeated n times (avoids importing strings just
// for this and keeps the helper namespaced).
func covCruRepeat(s string, n int) string {
	b := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		b = append(b, s...)
	}
	return string(b)
}
