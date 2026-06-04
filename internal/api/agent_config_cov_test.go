package api

// Coverage-focused tests for agent_config.go — the DB-driven resolver
// helpers that build the agent-config payload (system prompt, crew
// members, credentials, skills, MCP servers). No network/Docker.
//
// All test funcs are prefixed TestCovCfg; helpers are prefixed covCfg.
// Mirrors setup in core_handlers_test.go / internal_test.go.

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covCfgHandler builds a bare InternalHandler wired to the seeded DB.
// The resolver methods only touch h.db and h.logger.
func covCfgHandler(db *sql.DB) *InternalHandler {
	return &InternalHandler{db: db, logger: newTestLogger()}
}

// covCfgSeedSkillFull seeds a skill row with explicit vendor, description,
// credential_requirements, and content so the skills resolvers exercise
// their frontmatter / credential-status branches.
func covCfgSeedSkillFull(t *testing.T, db *sql.DB, id, slug, vendor, displayName, description, credReqJSON, content string) {
	t.Helper()
	var cr any
	if credReqJSON != "" {
		cr = credReqJSON
	}
	_, err := db.Exec(`INSERT INTO skills
		(id, name, slug, display_name, description, vendor, version, category, source, verification,
		 downloads, rating_count, pricing_tier, featured, tags, credential_requirements, content)
		VALUES (?, ?, ?, ?, ?, ?, '1.0.0', 'CODING', 'CUSTOM', 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', ?, ?)`,
		id, slug, slug, displayName, description, vendor, cr, content)
	if err != nil {
		t.Fatalf("covCfgSeedSkillFull %s: %v", id, err)
	}
}

// covCfgAssignSkill links a skill to an agent.
func covCfgAssignSkill(t *testing.T, db *sql.DB, id, agentID, skillID string, enabled int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES (?, ?, ?, ?)`,
		id, agentID, skillID, enabled); err != nil {
		t.Fatalf("covCfgAssignSkill %s: %v", id, err)
	}
}

// covCfgAssignCred links a credential to an agent under an env var name.
func covCfgAssignCred(t *testing.T, db *sql.DB, id, agentID, credID, envVar string, priority int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES (?, ?, ?, ?, ?)`, id, agentID, credID, envVar, priority); err != nil {
		t.Fatalf("covCfgAssignCred %s: %v", id, err)
	}
}

// covCfgSeedWSServer seeds a workspace-scoped MCP server.
func covCfgSeedWSServer(t *testing.T, db *sql.DB, id, wsID, name, transport, endpoint, command, argsJSON, envJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, command, args_json, env_json, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		id, wsID, name, "Display "+name, transport, covCfgNull(endpoint), covCfgNull(command),
		covCfgNull(argsJSON), covCfgNull(envJSON)); err != nil {
		t.Fatalf("covCfgSeedWSServer %s: %v", id, err)
	}
}

// covCfgSeedCrewServer seeds a crew-scoped MCP server.
func covCfgSeedCrewServer(t *testing.T, db *sql.DB, id, crewID, name, transport, endpoint string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO crew_mcp_servers
		(id, crew_id, name, display_name, transport, endpoint, enabled)
		VALUES (?, ?, ?, ?, ?, ?, 1)`,
		id, crewID, name, "Display "+name, transport, covCfgNull(endpoint)); err != nil {
		t.Fatalf("covCfgSeedCrewServer %s: %v", id, err)
	}
}

// covCfgBindServer binds an agent to an MCP server, optionally with a
// credential. scope is "workspace" or "crew". credID "" → no credential.
func covCfgBindServer(t *testing.T, db *sql.DB, id, agentID, serverID, scope, credID, credType, envVarName string, enabled int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, cred_type, env_var_name, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agentID, serverID, scope, covCfgNull(credID), credType, envVarName, enabled); err != nil {
		t.Fatalf("covCfgBindServer %s: %v", id, err)
	}
}

func covCfgNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// covCfgEncCred inserts an ACTIVE credential with an encrypted value of
// the given type so the decrypt path round-trips.
func covCfgEncCred(t *testing.T, db *sql.DB, wsID, userID, id, name, credType, username, plain string) {
	t.Helper()
	seedTypedCredential(t, db, wsID, userID, id, name, credType, username, plain)
}

// -----------------------------------------------------------------------------
// yamlQuote — pure function.
// -----------------------------------------------------------------------------

func TestCovCfgYamlQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", `""`},
		{"plain", `"plain"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{"a: b # c", `"a: b # c"`},
		{`both " and \`, `"both \" and \\"`},
		{"unicode-→", `"unicode-→"`},
	}
	for _, c := range cases {
		if got := yamlQuote(c.in); got != c.want {
			t.Errorf("yamlQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -----------------------------------------------------------------------------
// reconstructSKILLMD.
// -----------------------------------------------------------------------------

func TestCovCfgReconstructSKILLMD(t *testing.T) {
	// Already-frontmattered body is returned verbatim.
	withFM := "---\nname: x\n---\nbody"
	if got := reconstructSKILLMD("slug", "v", "Disp", "desc", withFM); got != withFM {
		t.Errorf("expected verbatim passthrough, got %q", got)
	}
	// Leading whitespace before --- still counts as frontmatter.
	pre := "  \n---\nname: y\n---\n"
	if got := reconstructSKILLMD("slug", "v", "Disp", "desc", pre); got != pre {
		t.Errorf("expected verbatim passthrough for leading-ws frontmatter")
	}

	// Full synthesis: distinct display_name, multiline description, vendor.
	out := reconstructSKILLMD("my-skill", "acme", "My Skill", "line1\r\nline2", "# Body")
	for _, want := range []string{
		"---\n",
		`name: "my-skill"`,
		`display_name: "My Skill"`,
		`description: "line1  line2"`, // CR and LF collapsed to spaces
		`vendor: "acme"`,
		"# Body",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("synthesised SKILL.md missing %q:\n%s", want, out)
		}
	}

	// display_name equal to slug is omitted; empty description/vendor omitted.
	out2 := reconstructSKILLMD("same", "", "same", "", "body2")
	if strings.Contains(out2, "display_name:") {
		t.Errorf("display_name should be omitted when equal to slug:\n%s", out2)
	}
	if strings.Contains(out2, "description:") || strings.Contains(out2, "vendor:") {
		t.Errorf("empty description/vendor should be omitted:\n%s", out2)
	}
}

// -----------------------------------------------------------------------------
// loadAgentData — found / not-found.
// -----------------------------------------------------------------------------

func TestCovCfgLoadAgentData(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew1", wsID, "Crew One", "crew-one")
	agentID := seedAgentRow(t, db, "ag1", wsID, crewID, "Ada", "ada", "LEAD")

	req := httptest.NewRequest("GET", "/", nil)
	d, err := h.loadAgentData(req, agentID)
	if err != nil {
		t.Fatalf("loadAgentData: %v", err)
	}
	if d.agentSlug != "ada" || !d.crewID.Valid || d.crewID.String != crewID {
		t.Fatalf("unexpected data: %+v", d)
	}

	if _, err := h.loadAgentData(req, "nope"); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// loadAgentSystemPrompt — language pref, custom prompt, skills, routines.
// -----------------------------------------------------------------------------

func TestCovCfgLoadAgentSystemPrompt(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crewP", wsID, "Marketing", "marketing")
	agentID := seedAgentRow(t, db, "agP", wsID, crewID, "Bob", "bob", "AGENT")

	// Add workspace preferred language + a custom system prompt + role title.
	if _, err := db.Exec(`UPDATE workspaces SET preferred_language='Czech' WHERE id=?`, wsID); err != nil {
		t.Fatalf("set lang: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET system_prompt_legacy='Do good work', role_title='Engineer' WHERE id=?`, agentID); err != nil {
		t.Fatalf("set prompt: %v", err)
	}

	// Assign an enabled skill so the [SKILLS AVAILABLE] block renders.
	covCfgSeedSkillFull(t, db, "skP", "deploy", "acme", "Deploy", "desc", `["DEPLOY_TOKEN"]`, "Deploy playbook body")
	covCfgAssignSkill(t, db, "asP", agentID, "skP", 1)

	req := httptest.NewRequest("GET", "/", nil)
	d, err := h.loadAgentData(req, agentID)
	if err != nil {
		t.Fatalf("loadAgentData: %v", err)
	}
	creds := []mcpCredEntry{{ID: "c", EnvVar: "DEPLOY_TOKEN", Value: "v"}}
	prompt, err := h.loadAgentSystemPrompt(req, d, creds, agentID)
	if err != nil {
		t.Fatalf("loadAgentSystemPrompt: %v", err)
	}
	for _, want := range []string{
		"[LANGUAGE PREFERENCE]", "Czech",
		"[AGENT IDENTITY]", "Name: Bob", "Role: Engineer", "Crew: Marketing",
		"[CUSTOM SYSTEM PROMPT]", "Do good work",
		"[SKILLS AVAILABLE]", "Deploy playbook body", "configured ✓",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// loadAgentSystemPrompt with no crew, no language, no custom prompt, no
// skills — exercises the minimal branch.
func TestCovCfgLoadAgentSystemPromptMinimal(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agMin", wsID, "", "Solo", "solo", "AGENT")

	req := httptest.NewRequest("GET", "/", nil)
	d, err := h.loadAgentData(req, agentID)
	if err != nil {
		t.Fatalf("loadAgentData: %v", err)
	}
	prompt, err := h.loadAgentSystemPrompt(req, d, nil, agentID)
	if err != nil {
		t.Fatalf("loadAgentSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Name: Solo") {
		t.Errorf("expected identity block, got %q", prompt)
	}
	if strings.Contains(prompt, "[LANGUAGE PREFERENCE]") || strings.Contains(prompt, "Crew:") {
		t.Errorf("unexpected blocks for minimal agent: %q", prompt)
	}
}

// -----------------------------------------------------------------------------
// lookupCrewNamesForWorkspace.
// -----------------------------------------------------------------------------

func TestCovCfgLookupCrewNames(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "cA", wsID, "Alpha", "alpha")
	seedCrewRow(t, db, "cB", wsID, "Beta", "beta")
	// Soft-deleted crew must be excluded.
	seedCrewRow(t, db, "cC", wsID, "Gamma", "gamma")
	if _, err := db.Exec(`UPDATE crews SET deleted_at=datetime('now') WHERE id='cC'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	out := h.lookupCrewNamesForWorkspace(req, wsID)
	if out["cA"] != "Alpha" || out["cB"] != "Beta" {
		t.Errorf("unexpected crew names: %+v", out)
	}
	if _, ok := out["cC"]; ok {
		t.Errorf("deleted crew should be excluded: %+v", out)
	}

	// Empty workspace → empty map (no error).
	if got := h.lookupCrewNamesForWorkspace(req, "no-such-ws"); len(got) != 0 {
		t.Errorf("expected empty map, got %+v", got)
	}
}

// -----------------------------------------------------------------------------
// resolveAgentCredentials — active vs filtered, decrypt round-trip, empty.
// -----------------------------------------------------------------------------

func TestCovCfgResolveAgentCredentials(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agC", wsID, "", "Cred", "cred", "AGENT")

	covCfgEncCred(t, db, wsID, userID, "cr1", "GitHub Token", "SECRET", "", "ghp_real")
	covCfgAssignCred(t, db, "ac1", agentID, "cr1", "GITHUB_TOKEN", 1)

	// PENDING-status credential should be filtered at the SQL boundary.
	covCfgEncCred(t, db, wsID, userID, "cr2", "Pending One", "SECRET", "", "secret2")
	if _, err := db.Exec(`UPDATE credentials SET status='PENDING' WHERE id='cr2'`); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	covCfgAssignCred(t, db, "ac2", agentID, "cr2", "PENDING_TOKEN", 2)

	req := httptest.NewRequest("GET", "/", nil)
	creds, err := h.resolveAgentCredentials(req, agentID)
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 active cred, got %d (%+v)", len(creds), creds)
	}
	if creds[0].EnvVar != "GITHUB_TOKEN" || creds[0].Value != "ghp_real" {
		t.Errorf("unexpected cred: %+v", creds[0])
	}

	// No credentials → non-nil empty slice.
	other := seedAgentRow(t, db, "agC2", wsID, "", "None", "none", "AGENT")
	empty, err := h.resolveAgentCredentials(req, other)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty non-nil slice, got %v err=%v", empty, err)
	}
}

// -----------------------------------------------------------------------------
// resolveOAuthAccessTokens.
// -----------------------------------------------------------------------------

func TestCovCfgResolveOAuthAccessTokens(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// OAUTH2 credential whose encrypted_value is the access token.
	enc, err := encryption.Encrypt("access-tok-123")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('oc1', ?, 'OAuth Cred', ?, 'OAUTH2', 'NONE', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, enc, userID); err != nil {
		t.Fatalf("seed oauth cred: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)

	// creds holds only the CLIENT_ID half → access token must be appended.
	in := []mcpCredEntry{{ID: "oc1", EnvVar: "FOO_CLIENT_ID", Type: "OAUTH2", Value: "cid"}}
	out := h.resolveOAuthAccessTokens(req, in)
	var found bool
	for _, c := range out {
		if c.EnvVar == "_OAUTH_ACCESS_TOKEN:oc1" && c.Value == "access-tok-123" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected appended oauth access token, got %+v", out)
	}

	// When an access token is already present, nothing is appended.
	in2 := []mcpCredEntry{{ID: "oc1", EnvVar: "FOO_TOKEN", Type: "OAUTH2", Value: "tok"}}
	out2 := h.resolveOAuthAccessTokens(req, in2)
	if len(out2) != 1 {
		t.Errorf("expected no append when access token present, got %+v", out2)
	}
}

// -----------------------------------------------------------------------------
// resolveCrewMembers — no crew, populated, LEAD integration enrichment.
// -----------------------------------------------------------------------------

func TestCovCfgResolveCrewMembers(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// No crew → empty members, no error.
	soloID := seedAgentRow(t, db, "agSolo", wsID, "", "Solo", "solo", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, soloID)
	if ms, err := h.resolveCrewMembers(req, d, soloID); err != nil || len(ms) != 0 {
		t.Fatalf("expected no members for crewless agent, got %v err=%v", ms, err)
	}

	// Crew with a LEAD + two peers; lead has an enabled MCP binding.
	crewID := seedCrewRow(t, db, "crewM", wsID, "Crew M", "crew-m")
	leadID := seedAgentRow(t, db, "lead", wsID, crewID, "Lead", "lead", "LEAD")
	peerID := seedAgentRow(t, db, "peer1", wsID, crewID, "Peer", "peer", "AGENT")
	seedAgentRow(t, db, "peer2", wsID, crewID, "Peer2", "peer2", "AGENT")

	// Bind peer1 to a crew MCP server so the LEAD enrichment branch runs.
	covCfgSeedCrewServer(t, db, "csrv1", crewID, "github", "streamable-http", "https://mcp.example.com")
	covCfgBindServer(t, db, "bind1", peerID, "csrv1", "crew", "", "bearer", "", 1)

	dl, err := h.loadAgentData(req, leadID)
	if err != nil {
		t.Fatalf("loadAgentData lead: %v", err)
	}
	members, err := h.resolveCrewMembers(req, dl, leadID)
	if err != nil {
		t.Fatalf("resolveCrewMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 peers, got %d (%+v)", len(members), members)
	}
	var enriched bool
	for _, m := range members {
		if m.ID == peerID && len(m.Integrations) == 1 && m.Integrations[0].ServerName == "github" {
			enriched = true
		}
	}
	if !enriched {
		t.Errorf("expected peer1 enriched with github integration, got %+v", members)
	}
}

// -----------------------------------------------------------------------------
// resolveNetworkPolicy & resolveContainerResources.
// -----------------------------------------------------------------------------

func TestCovCfgResolveNetworkPolicy(t *testing.T) {
	h := covCfgHandler(nil)

	// Default (no crew data) → free, empty domains.
	mode, domains := h.resolveNetworkPolicy(&agentConfigData{})
	if mode != "free" || len(domains) != 0 {
		t.Errorf("default: mode=%q domains=%v", mode, domains)
	}

	// Explicit restricted + valid domains JSON.
	d := &agentConfigData{
		crewID:             sql.NullString{String: "c", Valid: true},
		crewNetworkMode:    sql.NullString{String: "restricted", Valid: true},
		crewAllowedDomains: sql.NullString{String: `["a.com","b.com"]`, Valid: true},
	}
	mode, domains = h.resolveNetworkPolicy(d)
	if mode != "restricted" || len(domains) != 2 {
		t.Errorf("restricted: mode=%q domains=%v", mode, domains)
	}

	// Unknown mode → fail closed to restricted; malformed JSON → empty.
	d2 := &agentConfigData{
		crewNetworkMode:    sql.NullString{String: "bogus", Valid: true},
		crewAllowedDomains: sql.NullString{String: `{not json`, Valid: true},
	}
	mode, domains = h.resolveNetworkPolicy(d2)
	if mode != "restricted" || len(domains) != 0 {
		t.Errorf("bogus: mode=%q domains=%v", mode, domains)
	}
}

func TestCovCfgResolveContainerResources(t *testing.T) {
	h := covCfgHandler(nil)

	// Defaults.
	mem, cpus, ttl := h.resolveContainerResources(&agentConfigData{})
	if mem != 4096 || cpus != 2.0 || ttl != 0 {
		t.Errorf("defaults: %d %v %d", mem, cpus, ttl)
	}

	// Explicit values.
	d := &agentConfigData{
		crewMemoryMB: sql.NullInt64{Int64: 8192, Valid: true},
		crewCPUs:     sql.NullFloat64{Float64: 4.5, Valid: true},
		crewTTLHours: sql.NullInt64{Int64: 12, Valid: true},
	}
	mem, cpus, ttl = h.resolveContainerResources(d)
	if mem != 8192 || cpus != 4.5 || ttl != 12 {
		t.Errorf("explicit: %d %v %d", mem, cpus, ttl)
	}
}

// -----------------------------------------------------------------------------
// resolveSkillsBlock — empty, populated with credential status lines.
// -----------------------------------------------------------------------------

func TestCovCfgResolveSkillsBlock(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agSk", wsID, "", "Sk", "sk", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)

	// No skills → empty block.
	block, err := h.resolveSkillsBlock(req, nil, agentID)
	if err != nil || block != "" {
		t.Fatalf("expected empty block, got %q err=%v", block, err)
	}

	// Two skills: one with a configured cred req, one with a missing cred req.
	covCfgSeedSkillFull(t, db, "s1", "alpha", "acme", "Alpha", "d", `["TOKEN_A"]`, "Alpha body")
	covCfgSeedSkillFull(t, db, "s2", "beta", "", "Beta", "d", `["TOKEN_B"]`, "Beta body")
	covCfgAssignSkill(t, db, "asx1", agentID, "s1", 1)
	covCfgAssignSkill(t, db, "asx2", agentID, "s2", 1)
	// A disabled skill must be excluded.
	covCfgSeedSkillFull(t, db, "s3", "gamma", "", "Gamma", "d", "", "Gamma body")
	covCfgAssignSkill(t, db, "asx3", agentID, "s3", 0)

	creds := []mcpCredEntry{{EnvVar: "TOKEN_A"}}
	block, err = h.resolveSkillsBlock(req, creds, agentID)
	if err != nil {
		t.Fatalf("resolveSkillsBlock: %v", err)
	}
	for _, want := range []string{
		"[SKILLS AVAILABLE]", "[END SKILLS AVAILABLE]",
		"Alpha body", "Beta body",
		"TOKEN_A: configured ✓",
		"TOKEN_B: NOT CONFIGURED",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("skills block missing %q", want)
		}
	}
	if strings.Contains(block, "Gamma body") {
		t.Errorf("disabled skill must not appear")
	}
}

// resolveSkillsBlock truncation branch — a skill larger than the budget.
func TestCovCfgResolveSkillsBlockTruncate(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agTr", wsID, "", "Tr", "tr", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)

	big := strings.Repeat("X", 25000) // exceeds maxSkillsContextChars budget
	covCfgSeedSkillFull(t, db, "big", "bigskill", "", "Big", "d", "", big)
	covCfgAssignSkill(t, db, "asbig", agentID, "big", 1)

	block, err := h.resolveSkillsBlock(req, nil, agentID)
	if err != nil {
		t.Fatalf("resolveSkillsBlock: %v", err)
	}
	if !strings.Contains(block, "(truncated)") {
		t.Errorf("expected truncation marker, got len=%d", len(block))
	}
}

// -----------------------------------------------------------------------------
// resolveInstalledSkills.
// -----------------------------------------------------------------------------

func TestCovCfgResolveInstalledSkills(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agIns", wsID, "", "Ins", "ins", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)

	// Empty.
	if out, err := h.resolveInstalledSkills(req, agentID); err != nil || len(out) != 0 {
		t.Fatalf("expected empty, got %v err=%v", out, err)
	}

	// One skill whose body lacks frontmatter → reconstructed SKILL.md.
	covCfgSeedSkillFull(t, db, "is1", "writer", "anthropic", "Writer", "Write things", "", "# Writer\nbody")
	covCfgAssignSkill(t, db, "asis1", agentID, "is1", 1)

	out, err := h.resolveInstalledSkills(req, agentID)
	if err != nil {
		t.Fatalf("resolveInstalledSkills: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(out))
	}
	if out[0].Slug != "writer" || out[0].Vendor != "anthropic" {
		t.Errorf("unexpected: %+v", out[0])
	}
	if !strings.Contains(out[0].Content, "name: \"writer\"") || !strings.Contains(out[0].Content, "# Writer") {
		t.Errorf("content not reconstructed: %q", out[0].Content)
	}
}

// -----------------------------------------------------------------------------
// resolveAgentMCPServers — workspace + crew cascade, bindings, opt-out,
// opt-in filtering, credential attachment.
// -----------------------------------------------------------------------------

func TestCovCfgResolveAgentMCPServers(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crewS", wsID, "Crew S", "crew-s")
	agentID := seedAgentRow(t, db, "agMcp", wsID, crewID, "Mcp", "mcp", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)
	d, err := h.loadAgentData(req, agentID)
	if err != nil {
		t.Fatalf("loadAgentData: %v", err)
	}

	// ws1: workspace stdio server, bound to this agent WITH a credential.
	covCfgSeedWSServer(t, db, "ws1", wsID, "github", "stdio", "", "github-mcp", `["--flag"]`, `{"GITHUB_TOKEN":"x"}`)
	covCfgEncCred(t, db, wsID, userID, "mc1", "MCP Cred", "SECRET", "", "tok-plain")
	covCfgBindServer(t, db, "b1", agentID, "ws1", "workspace", "mc1", "bearer", "GITHUB_TOKEN", 1)

	// ws2: workspace server the agent OPTED OUT of (enabled=0) → skipped.
	covCfgSeedWSServer(t, db, "ws2", wsID, "optout", "streamable-http", "https://o.example.com", "", "", "")
	covCfgBindServer(t, db, "b2", agentID, "ws2", "workspace", "", "bearer", "", 0)

	// ws3: workspace server bound to ANOTHER agent only → opt-in filter skips it.
	other := seedAgentRow(t, db, "agOther", wsID, crewID, "Other", "other", "AGENT")
	covCfgSeedWSServer(t, db, "ws3", wsID, "others-only", "streamable-http", "https://x.example.com", "", "", "")
	covCfgBindServer(t, db, "b3", other, "ws3", "workspace", "", "bearer", "", 1)

	// cs1: crew server with NO binding for anyone → included (open).
	covCfgSeedCrewServer(t, db, "cs1", crewID, "crewopen", "streamable-http", "https://c.example.com")

	servers := h.resolveAgentMCPServers(req, d, agentID)

	byName := map[string]mcpServerEntry{}
	for _, s := range servers {
		byName[s.Name] = s
	}
	if _, ok := byName["github"]; !ok {
		t.Errorf("expected github server present")
	}
	if _, ok := byName["optout"]; ok {
		t.Errorf("opted-out server should be skipped")
	}
	if _, ok := byName["others-only"]; ok {
		t.Errorf("other-agent-only server should be filtered by opt-in")
	}
	if _, ok := byName["crewopen"]; !ok {
		t.Errorf("open crew server should be included")
	}
	gh := byName["github"]
	if gh.EnvVarName != "GITHUB_TOKEN" {
		t.Errorf("expected stdio env var name set, got %q", gh.EnvVarName)
	}
	if gh.CredToken != "tok-plain" || gh.CredType != "bearer" {
		t.Errorf("expected credential attached, got token=%q type=%q", gh.CredToken, gh.CredType)
	}
	if len(gh.Args) != 1 || gh.Args[0] != "--flag" {
		t.Errorf("expected parsed args, got %v", gh.Args)
	}
	if gh.Env["GITHUB_TOKEN"] != "x" {
		t.Errorf("expected parsed env, got %v", gh.Env)
	}
}

// resolveAgentMCPServers with no servers at all → empty result.
func TestCovCfgResolveAgentMCPServersEmpty(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agNoMcp", wsID, "", "NoMcp", "nomcp", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, agentID)
	if got := h.resolveAgentMCPServers(req, d, agentID); len(got) != 0 {
		t.Fatalf("expected no servers, got %+v", got)
	}
}

// -----------------------------------------------------------------------------
// buildKeeperBlock — empty (no SECRET) vs populated (redacts values).
// -----------------------------------------------------------------------------

func TestCovCfgBuildKeeperBlock(t *testing.T) {
	h := covCfgHandler(nil)

	// No SECRET creds → empty.
	if got := h.buildKeeperBlock("slug", []mcpCredEntry{{Type: "OAUTH2", EnvVar: "X"}}); got != "" {
		t.Errorf("expected empty keeper block, got %q", got)
	}

	// SECRET creds → block lists them and the input value is redacted.
	creds := []mcpCredEntry{
		{Type: "SECRET", EnvVar: "API_KEY", Value: "should-be-wiped"},
		{Type: "OAUTH2", EnvVar: "OTHER", Value: "keep"},
	}
	block := h.buildKeeperBlock("ada", creds)
	for _, want := range []string{
		"[CREDENTIAL ACCESS CONTROL — KEEPER]",
		"agent_slug\":\"ada\"",
		"- API_KEY",
		"[END CREDENTIAL ACCESS CONTROL]",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("keeper block missing %q", want)
		}
	}
	if creds[0].Value != "" {
		t.Errorf("SECRET value should be wiped, got %q", creds[0].Value)
	}
	if creds[1].Value != "keep" {
		t.Errorf("non-SECRET value should be untouched, got %q", creds[1].Value)
	}
}
