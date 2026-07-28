package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/credprovider"
)

// ─── coherence between the two feature maps ─────────────────────────────

// TestRequiredFeaturesResolveThroughFeatureToolNames is the anti-drift
// invariant across package boundaries: credprovider names the feature a
// provider needs, crew_resources.go names the CLI a feature installs.
// If they disagree — a ref whose id isn't in featureToolNames — the
// readiness check compares a required tool name against a tool set that
// can never contain it, and every crew reports a permanent phantom gap
// no amount of configuration can clear.
func TestRequiredFeaturesResolveThroughFeatureToolNames(t *testing.T) {
	for _, provider := range credprovider.ProvidersWithRequiredFeature() {
		ref := credprovider.RequiredFeature(provider)
		id := featureID(ref)
		if id == "" {
			t.Errorf("provider %q: featureID(%q) is empty", provider, ref)
			continue
		}
		tool, known := featureToolNames[id]
		if !known {
			t.Errorf("provider %q requires %q (feature id %q), but featureToolNames has no entry "+
				"for that id — add it there or the gap can never be cleared", provider, ref, id)
			continue
		}
		if tool == "" {
			t.Errorf("provider %q: feature %q maps to an empty tool name", provider, id)
		}
	}
}

// TestRequiredFeatureToolsAreNotInTheBaseImage guards the premise of the
// whole feature: reporting a gap only makes sense for tools the sandbox
// runtime image does NOT already ship. docker/crewship-sandbox/Dockerfile
// installs git, curl, jq and friends — if a provider's required tool is
// one of those, the report is noise the user learns to ignore.
func TestRequiredFeatureToolsAreNotInTheBaseImage(t *testing.T) {
	baseImageTools := map[string]bool{
		"git": true, "curl": true, "wget": true, "jq": true,
		"bash": true, "zsh": true, "vim": true, "nano": true,
	}
	for _, provider := range credprovider.ProvidersWithRequiredFeature() {
		tool := featureToolName(featureID(credprovider.RequiredFeature(provider)))
		if baseImageTools[tool] {
			t.Errorf("provider %q requires %q, which the sandbox base image already ships — "+
				"a credential for it can never produce a real gap", provider, tool)
		}
	}
}

// ─── readiness resolution ───────────────────────────────────────────────

// ccrSeed builds a workspace + crew with the given devcontainer config.
func ccrSeed(t *testing.T, devcontainerConfig string) (*sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	var cfg any
	if devcontainerConfig != "" {
		cfg = devcontainerConfig
	}
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config) VALUES (?,?,?,?,?)`,
		"crew-ccr", wsID, "Ops", "ops", cfg); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return db, userID, wsID, "crew-ccr"
}

// ccrSeedCredential inserts a workspace-scoped credential for a provider.
func ccrSeedCredential(t *testing.T, db *sql.DB, id, wsID, userID, name, provider string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, provider, created_by)
		 VALUES (?,?,?,'enc',?,?)`,
		id, wsID, name, provider, userID); err != nil {
		t.Fatalf("seed credential %s: %v", id, err)
	}
}

func ccrGapProviders(gaps []credentialToolGap) []string {
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, g.Provider)
	}
	return out
}

// A GitHub credential on a crew that never declared the github-cli
// feature is the exact bug this endpoint exists to name: the vault says
// "connected", the container has no `gh`.
func TestCrewCredentialReadiness_ReportsMissingTool(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, `{"features":{"ghcr.io/devcontainers/features/terraform:1":{}}}`)
	ccrSeedCredential(t, db, "cred-gh", wsID, userID, "gh-pat", "GITHUB")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d: %+v", len(res.Gaps), res.Gaps)
	}
	g := res.Gaps[0]
	if g.CredentialID != "cred-gh" || g.CredentialName != "gh-pat" || g.Provider != "GITHUB" {
		t.Errorf("gap identifies the wrong credential: %+v", g)
	}
	if g.Tool != "gh" {
		t.Errorf("gap tool = %q, want gh", g.Tool)
	}
	if g.Feature != "ghcr.io/devcontainers/features/github-cli:1" || g.FeatureID != "github-cli" {
		t.Errorf("gap feature = %q/%q, want the github-cli ref", g.Feature, g.FeatureID)
	}
	if res.Checked != 1 {
		t.Errorf("checked = %d, want 1", res.Checked)
	}
	// The crew's existing tools travel with the report so the caller can
	// explain what IS present without a second round-trip.
	if len(res.Tools) != 1 || res.Tools[0] != "terraform" {
		t.Errorf("tools = %v, want [terraform]", res.Tools)
	}
}

// Same crew, same credential, feature declared → nothing to report. This
// is the half that stops the check degenerating into "always complain".
func TestCrewCredentialReadiness_FeatureDeclaredNoGap(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, `{"features":{"ghcr.io/devcontainers/features/github-cli:1":{}}}`)
	ccrSeedCredential(t, db, "cred-gh", wsID, userID, "gh-pat", "GITHUB")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("expected no gaps, got %+v", res.Gaps)
	}
	if res.Checked != 1 {
		t.Errorf("checked = %d, want 1 — the credential was still examined", res.Checked)
	}
}

// A different feature that installs the same binary must satisfy the
// requirement. docker-outside-of-docker is not the ref we suggest, but
// it does put `docker` on PATH — matching on the ref instead of the tool
// would tell the user to install a second docker feature.
func TestCrewCredentialReadiness_EquivalentFeatureSatisfies(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, `{"features":{"ghcr.io/devcontainers/features/docker-outside-of-docker:1":{}}}`)
	ccrSeedCredential(t, db, "cred-dh", wsID, userID, "registry", "DOCKER")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("docker-outside-of-docker should satisfy a DOCKER credential, got %+v", res.Gaps)
	}
}

// mise is the other way a tool lands on PATH (crew_resources reads both),
// so a mise-installed node must clear an NPM credential's requirement.
func TestCrewCredentialReadiness_MiseToolSatisfies(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	if _, err := db.Exec(`UPDATE crews SET mise_config = ? WHERE id = ?`,
		`{"tools":{"nodejs":"22"}}`, crewID); err != nil {
		t.Fatalf("set mise config: %v", err)
	}
	ccrSeedCredential(t, db, "cred-npm", wsID, userID, "npm-publish", "NPM")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("mise nodejs should satisfy an NPM credential, got %+v", res.Gaps)
	}
}

// A credential for a provider we have no tool opinion about must be
// silent. Reporting "unknown" for every Notion/Stripe/API key would bury
// the handful of gaps that are actionable.
func TestCrewCredentialReadiness_UnknownProviderIsSilent(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	ccrSeedCredential(t, db, "cred-notion", wsID, userID, "notion", "NOTION")
	ccrSeedCredential(t, db, "cred-weird", wsID, userID, "acme", "ACME_INTERNAL")
	ccrSeedCredential(t, db, "cred-none", wsID, userID, "opaque", "NONE")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("expected no gaps for providers with no known tool, got %+v", res.Gaps)
	}
	if res.Checked != 0 {
		t.Errorf("checked = %d, want 0 — none of these providers require a tool", res.Checked)
	}
}

// Revoked and soft-deleted credentials are not credentials the agent can
// use, so they must not manufacture work for the user.
func TestCrewCredentialReadiness_SkipsRevokedAndDeleted(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	ccrSeedCredential(t, db, "cred-rev", wsID, userID, "old-gh", "GITHUB")
	ccrSeedCredential(t, db, "cred-del", wsID, userID, "gone-aws", "AWS")
	if _, err := db.Exec(`UPDATE credentials SET status='REVOKED' WHERE id='cred-rev'`); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := db.Exec(`UPDATE credentials SET deleted_at='2026-01-01T00:00:00Z' WHERE id='cred-del'`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("revoked/deleted credentials must not report gaps, got %+v", res.Gaps)
	}
}

// Crew scoping: a CREW-scoped credential belonging to a sibling crew is
// not usable here and must not be reported. Mirrors the visibility idiom
// in credentialVisibilityFilter / internal_credentials.go (#1031).
func TestCrewCredentialReadiness_ScopesToTheCrew(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?,?,?,?)`,
		"crew-other", wsID, "Other", "other"); err != nil {
		t.Fatalf("seed sibling crew: %v", err)
	}

	// Mine, via the junction table.
	ccrSeedCredential(t, db, "cred-mine", wsID, userID, "mine-gh", "GITHUB")
	// Theirs.
	ccrSeedCredential(t, db, "cred-theirs", wsID, userID, "their-aws", "AWS")
	for _, row := range [][2]string{{"cred-mine", crewID}, {"cred-theirs", "crew-other"}} {
		if _, err := db.Exec(
			`UPDATE credentials SET scope='CREW' WHERE id=?`, row[0]); err != nil {
			t.Fatalf("scope credential: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO credential_crews (credential_id, crew_id) VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatalf("link credential: %v", err)
		}
	}

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if got := ccrGapProviders(res.Gaps); len(got) != 1 || got[0] != "GITHUB" {
		t.Errorf("gap providers = %v, want [GITHUB] only — the sibling crew's AWS credential leaked", got)
	}
}

// A credential assigned to one of the crew's agents counts even when it
// is neither workspace-scoped nor linked through credential_crews — that
// is the third grant path the sidecar honours.
func TestCrewCredentialReadiness_AgentAssignmentCounts(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	covCISeedAgent(t, db, "ag-ccr", wsID, crewID)
	ccrSeedCredential(t, db, "cred-aws", wsID, userID, "deploy-aws", "AWS")
	if _, err := db.Exec(`UPDATE credentials SET scope='CREW' WHERE id='cred-aws'`); err != nil {
		t.Fatalf("scope credential: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac1','ag-ccr','cred-aws','AWS_ACCESS_KEY_ID')`); err != nil {
		t.Fatalf("assign credential: %v", err)
	}

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if got := ccrGapProviders(res.Gaps); len(got) != 1 || got[0] != "AWS" {
		t.Errorf("gap providers = %v, want [AWS]", got)
	}
}

// An expired lease (#1373) means the container no longer holds the
// credential, so it must stop producing a gap once it lapses.
func TestCrewCredentialReadiness_ExpiredLeaseDropsOut(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	covCISeedAgent(t, db, "ag-ccr", wsID, crewID)
	ccrSeedCredential(t, db, "cred-aws", wsID, userID, "deploy-aws", "AWS")
	if _, err := db.Exec(`UPDATE credentials SET scope='CREW' WHERE id='cred-aws'`); err != nil {
		t.Fatalf("scope credential: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, expires_at)
		 VALUES ('ac1','ag-ccr','cred-aws','AWS_ACCESS_KEY_ID','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("assign credential: %v", err)
	}

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("lapsed lease must not report a gap, got %+v", res.Gaps)
	}
}

// Workspace isolation: a WORKSPACE-scoped credential is visible to every
// crew in ITS workspace and to no crew outside it.
func TestCrewCredentialReadiness_WorkspaceIsolation(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other','Other','other')`); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	ccrSeedCredential(t, db, "cred-foreign", "ws-other", userID, "foreign-gh", "GITHUB")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("another workspace's credential leaked into the report: %+v", res.Gaps)
	}
}

// A malformed devcontainer blob must not blank the report — crew_resources
// is lenient by design, and a parse failure here would silently claim the
// crew has no tools and flood the user with phantom gaps... which is the
// honest answer, so assert we stay lenient rather than error.
func TestCrewCredentialReadiness_MalformedDevcontainerIsLenient(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, `{not json`)
	ccrSeedCredential(t, db, "cred-gh", wsID, userID, "gh-pat", "GITHUB")

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("malformed config must not error the resolve: %v", err)
	}
	if len(res.Gaps) != 1 {
		t.Errorf("expected the GITHUB gap, got %+v", res.Gaps)
	}
}

// ─── handler ────────────────────────────────────────────────────────────

func TestCrewCredentialReadinessHandler(t *testing.T) {
	db, userID, wsID, crewID := ccrSeed(t, "")
	ccrSeedCredential(t, db, "cred-gh", wsID, userID, "gh-pat", "GITHUB")

	h := NewCrewHandler(db, newTestLogger())
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/credential-readiness", nil), wsID)
	req.SetPathValue("crewId", crewID)
	w := httptest.NewRecorder()
	h.CredentialReadiness(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out crewCredentialReadinessResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CrewID != crewID || out.CrewSlug != "ops" {
		t.Errorf("crew identity wrong: %+v", out)
	}
	if len(out.Gaps) != 1 || out.Gaps[0].Tool != "gh" {
		t.Errorf("gaps = %+v, want one gh gap", out.Gaps)
	}
	// Slices must serialize as [] not null so a JS consumer can iterate
	// without a null check — the crew here declares no features at all.
	if strings.Contains(w.Body.String(), ":null") {
		t.Errorf("empty slices must render as [], body: %s", w.Body.String())
	}
}

func TestCrewCredentialReadinessHandler_Errors(t *testing.T) {
	db, _, wsID, _ := ccrSeed(t, "")
	h := NewCrewHandler(db, newTestLogger())

	t.Run("missing crewId", func(t *testing.T) {
		req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/crews//credential-readiness", nil), wsID)
		w := httptest.NewRecorder()
		h.CredentialReadiness(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	// A crew in another workspace must 404, not leak its readiness.
	t.Run("crew not in workspace", func(t *testing.T) {
		req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/crews/nope/credential-readiness", nil), "ws-elsewhere")
		req.SetPathValue("crewId", "crew-ccr")
		w := httptest.NewRecorder()
		h.CredentialReadiness(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}

// TestCrewCredentialReadiness_SeesBindingOnlyCredential closes the reader gap
// the merge review found. A credential delivered to a crew ONLY through a
// CREW-scoped binding — not WORKSPACE scope, no credential_crews row, no
// agent_credentials grant — was invisible to readiness, because the query knew
// the three pre-binding delivery sources and not bindings. The container gets
// the credential, may lack the tool, and the report that exists to catch
// exactly that said nothing.
func TestCrewCredentialReadiness_SeesBindingOnlyCredential(t *testing.T) {
	// Crew has terraform, so an AWS credential (needs aws-cli) is a gap — but
	// only if readiness sees the credential at all.
	db, userID, wsID, crewID := ccrSeed(t, `{"features":{"ghcr.io/devcontainers/features/terraform:1":{}}}`)

	// CREW-scoped, reachable by NONE of the three legacy sources: not WORKSPACE
	// scope, and no credential_crews / agent_credentials row.
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, provider, scope, created_by)
		 VALUES ('cred-aws', ?, 'aws-key', 'enc', 'AWS', 'CREW', ?)`, wsID, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	// Its only path to the crew is a CREW binding.
	if _, err := db.Exec(
		`INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, slot)
		 VALUES ('bind-aws', ?, 'cred-aws', 'CREW', ?, 'AWS_ACCESS_KEY_ID')`, wsID, crewID); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	res, err := resolveCrewCredentialReadiness(context.Background(), db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolveCrewCredentialReadiness: %v", err)
	}
	found := false
	for _, g := range res.Gaps {
		if g.CredentialID == "cred-aws" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a credential delivered only via a binding was invisible to readiness — the "+
			"container gets it and may lack aws, and the report is silent. Gaps: %+v", res.Gaps)
	}
}
