package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	covCrewIDCli2  = "ccrew000000000000001"
	covAgentIDCli2 = "cagent00000000000001"
	covCredIDCli2  = "ccred000000000000001"
)

func newSeedClient(stub *clitest.StubServer) *cli.Client {
	return cli.NewClient(stub.URL(), "seed-token", covWS)
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// ─── seedCrews ──────────────────────────────────────────────────────

func TestSeedCrews_CreatesAndLinksUser(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/crews", clitest.JSONResponse(201, map[string]string{"id": covCrewIDCli2}))
	stub.OnPost("/api/v1/crews/"+covCrewIDCli2+"/members", clitest.JSONResponse(201, map[string]string{"id": "cm1"}))

	var ids map[string]string
	_ = captureStdoutCovCli2(t, func() {
		var err error
		ids, err = seedCrews(context.Background(), newSeedClient(stub), "cuser000000000000001")
		if err != nil {
			t.Errorf("seedCrews: %v", err)
		}
	})

	if len(ids) != len(seeddata.Crews) {
		t.Fatalf("ids = %d, want %d", len(ids), len(seeddata.Crews))
	}
	for _, c := range seeddata.Crews {
		if ids[c.Slug] != covCrewIDCli2 {
			t.Errorf("crew %s id = %q", c.Slug, ids[c.Slug])
		}
	}
	createCalls := stub.CallsFor("POST", "/api/v1/crews")
	if len(createCalls) != len(seeddata.Crews) {
		t.Errorf("crew POSTs = %d, want %d", len(createCalls), len(seeddata.Crews))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(createCalls[0].Body, &body)
	if body["slug"] != seeddata.Crews[0].Slug || body["name"] != seeddata.Crews[0].Name {
		t.Errorf("first crew body = %v", body)
	}
	memberCalls := stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli2+"/members")
	if len(memberCalls) != len(seeddata.Crews) {
		t.Errorf("member POSTs = %d, want %d", len(memberCalls), len(seeddata.Crews))
	}
	var member map[string]string
	clitest.MustDecodeJSONBody(memberCalls[0].Body, &member)
	if member["user_id"] != "cuser000000000000001" {
		t.Errorf("member body = %v", member)
	}
}

func TestSeedCrews_ConflictResolvesBySlugAndSkipsLinkWithoutUser(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/crews", clitest.ErrorResponse(409, "exists"))
	existing := make([]map[string]string, 0, len(seeddata.Crews))
	for i, c := range seeddata.Crews {
		existing = append(existing, map[string]string{"id": covCrewIDCli2[:19] + string(rune('1'+i)), "slug": c.Slug})
	}
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, existing))

	var ids map[string]string
	_ = captureStdoutCovCli2(t, func() {
		var err error
		ids, err = seedCrews(context.Background(), newSeedClient(stub), "")
		if err != nil {
			t.Errorf("seedCrews: %v", err)
		}
	})
	if len(ids) != len(seeddata.Crews) {
		t.Fatalf("ids = %d, want %d", len(ids), len(seeddata.Crews))
	}
	// userID empty → no member POSTs at all.
	for _, call := range stub.Calls() {
		if strings.Contains(call.Path, "/members") {
			t.Errorf("unexpected member call: %s %s", call.Method, call.Path)
		}
	}
}

func TestSeedCrews_ErrorAndCancellation(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/crews", clitest.ErrorResponse(500, "db down"))

	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedCrews(context.Background(), newSeedClient(stub), ""); err == nil ||
			!strings.Contains(err.Error(), "crew "+seeddata.Crews[0].Slug) {
			t.Errorf("got %v", err)
		}
	})

	if _, err := seedCrews(canceledCtx(), newSeedClient(stub), ""); err != context.Canceled {
		t.Errorf("canceled ctx: got %v", err)
	}
}

// ─── seedCrewConnections ────────────────────────────────────────────

func TestSeedCrewConnections_AllPairs(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/crew-connections", clitest.JSONResponse(201, map[string]string{"id": "cc"}))

	crews := map[string]string{
		"alpha": "ccrewalpha0000000001",
		"beta":  "ccrewbeta00000000001",
		"gamma": "ccrewgamma0000000001",
	}
	_ = captureStdoutCovCli2(t, func() {
		if err := seedCrewConnections(context.Background(), newSeedClient(stub), crews); err != nil {
			t.Errorf("seedCrewConnections: %v", err)
		}
	})

	calls := stub.CallsFor("POST", "/api/v1/crew-connections")
	if len(calls) != 3 { // C(3,2)
		t.Fatalf("connection POSTs = %d, want 3", len(calls))
	}
	// Deterministic sorted ordering: alpha↔beta, alpha↔gamma, beta↔gamma.
	var first map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &first)
	if first["from_crew_id"] != crews["alpha"] || first["to_crew_id"] != crews["beta"] || first["direction"] != "bidirectional" {
		t.Errorf("first pair body = %v", first)
	}
}

func TestSeedCrewConnections_EdgeCases(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	client := newSeedClient(stub)

	// Fewer than two crews → no calls, nil error.
	if err := seedCrewConnections(context.Background(), client, map[string]string{"solo": "c1"}); err != nil {
		t.Errorf("single crew: %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Errorf("single crew must not POST, got %d calls", len(stub.Calls()))
	}

	// Canceled context.
	two := map[string]string{"a": "c1", "b": "c2"}
	if err := seedCrewConnections(canceledCtx(), client, two); err != context.Canceled {
		t.Errorf("canceled: got %v", err)
	}

	// Conflict and server-error responses are tolerated (warn, not fail).
	stub.OnPost("/api/v1/crew-connections", clitest.ErrorResponse(409, "exists"))
	_ = captureStdoutCovCli2(t, func() {
		if err := seedCrewConnections(context.Background(), client, two); err != nil {
			t.Errorf("409: %v", err)
		}
	})
	stub.OnPost("/api/v1/crew-connections", clitest.ErrorResponse(500, "boom"))
	_ = captureStdoutCovCli2(t, func() {
		if err := seedCrewConnections(context.Background(), client, two); err != nil {
			t.Errorf("500 should not abort the seed: %v", err)
		}
	})
}

// ─── seedAgents ─────────────────────────────────────────────────────

func TestSeedAgents_CreatesAllAgents(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/agents", clitest.JSONResponse(201, map[string]string{"id": covAgentIDCli2}))

	crewIDs := map[string]string{}
	for _, c := range seeddata.Crews {
		crewIDs[c.Slug] = covCrewIDCli2
	}

	var ids map[string]string
	_ = captureStdoutCovCli2(t, func() {
		var err error
		ids, err = seedAgents(context.Background(), newSeedClient(stub), crewIDs)
		if err != nil {
			t.Errorf("seedAgents: %v", err)
		}
	})
	if len(ids) != len(seeddata.Agents) {
		t.Fatalf("agent ids = %d, want %d", len(ids), len(seeddata.Agents))
	}
	calls := stub.CallsFor("POST", "/api/v1/agents")
	if len(calls) != len(seeddata.Agents) {
		t.Fatalf("agent POSTs = %d, want %d", len(calls), len(seeddata.Agents))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	first := seeddata.Agents[0]
	if body["slug"] != first.Slug || body["crew_id"] != covCrewIDCli2 || body["agent_role"] != first.AgentRole {
		t.Errorf("first agent body = %v", body)
	}
	if body["system_prompt"] == "" || body["system_prompt"] == nil {
		t.Errorf("system_prompt not populated from prompt fixture")
	}
}

func TestSeedAgents_SkipsUnknownCrewAndPropagatesErrors(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()

	// Empty crew map → every agent skipped, no HTTP calls, empty result.
	var ids map[string]string
	_ = captureStdoutCovCli2(t, func() {
		var err error
		ids, err = seedAgents(context.Background(), newSeedClient(stub), map[string]string{})
		if err != nil {
			t.Errorf("seedAgents: %v", err)
		}
	})
	if len(ids) != 0 || len(stub.Calls()) != 0 {
		t.Errorf("expected all agents skipped, ids=%v calls=%d", ids, len(stub.Calls()))
	}

	// Server error aborts with agent slug context.
	stub.OnPost("/api/v1/agents", clitest.ErrorResponse(500, "boom"))
	crewIDs := map[string]string{}
	for _, c := range seeddata.Crews {
		crewIDs[c.Slug] = covCrewIDCli2
	}
	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedAgents(context.Background(), newSeedClient(stub), crewIDs); err == nil ||
			!strings.Contains(err.Error(), "agent "+seeddata.Agents[0].Slug) {
			t.Errorf("got %v", err)
		}
	})

	if _, err := seedAgents(canceledCtx(), newSeedClient(stub), crewIDs); err != context.Canceled {
		t.Errorf("canceled: got %v", err)
	}
}

// ─── seedSkills ─────────────────────────────────────────────────────

func TestSeedSkills_ExistingSkillsAreAssigned(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	existing := make([]map[string]string, 0, len(seeddata.Skills))
	seenSlug := map[string]bool{}
	for i, s := range seeddata.Skills {
		existing = append(existing, map[string]string{"id": "cskill0000000000000" + string(rune('1'+i)), "slug": s.Slug})
		seenSlug[s.Slug] = true
	}
	// Bundled first-party skills (e.g. routine-author) are auto-installed on
	// server startup, so a real GET /skills returns them even though they're
	// not in seeddata.Skills. Model that here, otherwise their assignments are
	// silently skipped and the count is short.
	for _, skills := range seeddata.SkillAssignments {
		for _, slug := range skills {
			if !seenSlug[slug] {
				seenSlug[slug] = true
				existing = append(existing, map[string]string{"id": fmt.Sprintf("cskillbundled%012d", len(existing)), "slug": slug})
			}
		}
	}
	stub.OnGet("/api/v1/skills", clitest.JSONResponse(200, existing))
	stub.OnPost("/api/v1/agents/"+covAgentIDCli2+"/skills", clitest.JSONResponse(201, map[string]string{"id": "as1"}))

	agentIDs := map[string]string{}
	wantAssignments := 0
	for slug, skills := range seeddata.SkillAssignments {
		agentIDs[slug] = covAgentIDCli2
		wantAssignments += len(skills)
	}

	_ = captureStdoutCovCli2(t, func() {
		if err := seedSkills(context.Background(), newSeedClient(stub), agentIDs); err != nil {
			t.Errorf("seedSkills: %v", err)
		}
	})

	// No imports needed (all skills pre-existing).
	for _, call := range stub.Calls() {
		if strings.Contains(call.Path, "/skills/import") {
			t.Errorf("unexpected import call: %s", call.Path)
		}
	}
	assigns := stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli2+"/skills")
	if len(assigns) != wantAssignments {
		t.Errorf("assignment POSTs = %d, want %d", len(assigns), wantAssignments)
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(assigns[0].Body, &body)
	if !strings.HasPrefix(body["skill_id"], "cskill") {
		t.Errorf("assignment body = %v", body)
	}
}

func TestSeedSkills_ImportsMissingAndToleratesConflicts(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	// Bundled first-party skills (e.g. routine-author) are auto-installed on the
	// server, so GET /skills returns them even though they're not in
	// seeddata.Skills (which is the set that gets imported). Without modelling
	// this, a bundled-skill assignment is silently skipped.
	bundledExisting := []map[string]string{}
	bundledSeen := map[string]bool{}
	for _, s := range seeddata.Skills {
		bundledSeen[s.Slug] = true
	}
	for _, skills := range seeddata.SkillAssignments {
		for _, slug := range skills {
			if !bundledSeen[slug] {
				bundledSeen[slug] = true
				bundledExisting = append(bundledExisting, map[string]string{"id": fmt.Sprintf("cskillbundled%012d", len(bundledExisting)), "slug": slug})
			}
		}
	}
	stub.OnGet("/api/v1/skills", clitest.JSONResponse(200, bundledExisting))
	importPath := "/api/v1/workspaces/" + covWS + "/skills/import"
	stub.OnPost(importPath, clitest.JSONResponse(201, map[string]string{"skill_id": "cskillnew00000000001", "slug": "x"}))
	// All assignments answer 409 — treated as already-assigned success.
	stub.OnPost("/api/v1/agents/"+covAgentIDCli2+"/skills", clitest.ErrorResponse(409, "already assigned"))

	agentSlug := ""
	for slug := range seeddata.SkillAssignments {
		agentSlug = slug
		break
	}
	agentIDs := map[string]string{agentSlug: covAgentIDCli2}

	_ = captureStdoutCovCli2(t, func() {
		if err := seedSkills(context.Background(), newSeedClient(stub), agentIDs); err != nil {
			t.Errorf("seedSkills: %v", err)
		}
	})

	imports := stub.CallsFor("POST", importPath)
	if len(imports) != len(seeddata.Skills) {
		t.Errorf("imports = %d, want %d", len(imports), len(seeddata.Skills))
	}
	// Import body is SKILL.md content with YAML frontmatter.
	var body map[string]string
	clitest.MustDecodeJSONBody(imports[0].Body, &body)
	if !strings.HasPrefix(body["content"], "---\nname:") {
		t.Errorf("import content not SKILL.md shaped: %q", body["content"][:40])
	}
	if got := len(stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli2+"/skills")); got != len(seeddata.SkillAssignments[agentSlug]) {
		t.Errorf("assignment calls = %d, want %d", got, len(seeddata.SkillAssignments[agentSlug]))
	}

	if err := seedSkills(canceledCtx(), newSeedClient(stub), agentIDs); err != context.Canceled {
		t.Errorf("canceled: got %v", err)
	}
}

// ─── seedCredentials / seedOneCredential ────────────────────────────

func TestSeedCredentials_CreatesAndAssigns(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	t.Setenv("SEED_ANTHROPIC_API_KEY", "sk-ant-test-key")
	t.Setenv("SEED_GOOGLE_EMAIL", "")
	t.Setenv("SEED_GOOGLE_PASSWORD", "")

	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": covCredIDCli2}))
	stub.OnPost("/api/v1/agents/"+covAgentIDCli2+"/credentials", clitest.JSONResponse(201, map[string]string{"id": "ac1"}))

	agentIDs := map[string]string{"tomas": covAgentIDCli2, "eva": covAgentIDCli2}
	out := captureStdoutCovCli2(t, func() {
		if err := seedCredentials(context.Background(), newSeedClient(stub), agentIDs); err != nil {
			t.Errorf("seedCredentials: %v", err)
		}
	})
	if !strings.Contains(out, "Using real API_KEY from SEED_ANTHROPIC_API_KEY") {
		t.Errorf("expected real-key banner:\n%s", out)
	}
	if !strings.Contains(out, "Skipping Google credential") {
		t.Errorf("expected Google skip note:\n%s", out)
	}

	creates := stub.CallsFor("POST", "/api/v1/credentials")
	if len(creates) != 1 {
		t.Fatalf("credential POSTs = %d, want 1", len(creates))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(creates[0].Body, &body)
	if body["name"] != "ANTHROPIC_API_KEY" || body["value"] != "sk-ant-test-key" || body["scope"] != "WORKSPACE" {
		t.Errorf("credential body = %v", body)
	}
	assigns := stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli2+"/credentials")
	if len(assigns) != 2 {
		t.Errorf("assignment POSTs = %d, want 2", len(assigns))
	}
	var assign map[string]string
	clitest.MustDecodeJSONBody(assigns[0].Body, &assign)
	if assign["credential_id"] != covCredIDCli2 || assign["env_var_name"] != "ANTHROPIC_API_KEY" {
		t.Errorf("assignment body = %v", assign)
	}
}

func TestSeedCredentials_PlaceholderWarningAndGoogle(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	t.Setenv("SEED_ANTHROPIC_API_KEY", "")
	t.Setenv("SEED_GOOGLE_EMAIL", "g@b.c")
	t.Setenv("SEED_GOOGLE_PASSWORD", "pw")

	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": covCredIDCli2}))
	stub.OnPost("/api/v1/agents/"+covAgentIDCli2+"/credentials", clitest.ErrorResponse(409, "already assigned"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedCredentials(context.Background(), newSeedClient(stub), map[string]string{"tomas": covAgentIDCli2}); err != nil {
			t.Errorf("seedCredentials: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING: using demo placeholder key") {
		t.Errorf("expected placeholder warning:\n%s", out)
	}
	// Anthropic + Google both created.
	if got := len(stub.CallsFor("POST", "/api/v1/credentials")); got != 2 {
		t.Errorf("credential creates = %d, want 2 (anthropic + google)", got)
	}
	// 409 on assignment is idempotent-success; summary still printed.
	if !strings.Contains(out, "1/1 agents") {
		t.Errorf("expected assignment summary:\n%s", out)
	}

	if err := seedCredentials(canceledCtx(), newSeedClient(stub), nil); err != context.Canceled {
		t.Errorf("canceled: got %v", err)
	}
}

func TestSeedOneCredential_Paths(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	cred := seeddata.CredentialDef{
		Name: "MY_KEY", Description: "d", Type: "API_KEY", Provider: "ANTHROPIC",
		EnvVarName: "MY_KEY", Value: "v",
	}

	// Already exists by name → resolved, no POST.
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": "cexisting00000000001", "name": "MY_KEY"},
	}))
	var id string
	_ = captureStdoutCovCli2(t, func() {
		var err error
		id, err = seedOneCredential(newSeedClient(stub), cred)
		if err != nil {
			t.Errorf("existing: %v", err)
		}
	})
	if id != "cexisting00000000001" {
		t.Errorf("existing id = %q", id)
	}
	if len(stub.CallsFor("POST", "/api/v1/credentials")) != 0 {
		t.Error("must not POST when credential already exists")
	}

	// Not found → POST → 409 → re-resolve by name. With the listing
	// kept empty the second resolve fails with "not found", proving
	// the conflict branch re-queries the list endpoint.
	stub.ResetCalls()
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/credentials", clitest.ErrorResponse(409, "exists"))
	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedOneCredential(newSeedClient(stub), cred); err == nil ||
			!strings.Contains(err.Error(), `"MY_KEY" not found`) {
			t.Errorf("conflict + empty list: got %v", err)
		}
	})
	if got := len(stub.CallsFor("GET", "/api/v1/credentials")); got != 2 {
		t.Errorf("expected 2 list calls (pre-check + post-409 resolve), got %d", got)
	}

	// Server error on create.
	stub.OnPost("/api/v1/credentials", clitest.ErrorResponse(500, "boom"))
	if _, err := seedOneCredential(newSeedClient(stub), cred); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("500: got %v", err)
	}

	// Fresh create returns the new id.
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": "cfresh0000000000001"}))
	_ = captureStdoutCovCli2(t, func() {
		got, err := seedOneCredential(newSeedClient(stub), cred)
		if err != nil {
			t.Errorf("create: %v", err)
		}
		if got != "cfresh0000000000001" {
			t.Errorf("created id = %q", got)
		}
	})
}

func TestSeedCrews_MemberLinkHTTPErrorTolerated(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/crews", clitest.JSONResponse(201, map[string]string{"id": covCrewIDCli2}))
	stub.OnPost("/api/v1/crews/"+covCrewIDCli2+"/members", clitest.ErrorResponse(500, "membership broken"))

	var ids map[string]string
	out := captureStdoutCovCli2(t, func() {
		var err error
		ids, err = seedCrews(context.Background(), newSeedClient(stub), "cuser000000000000001")
		if err != nil {
			t.Errorf("member failures must not abort: %v", err)
		}
	})
	if len(ids) != len(seeddata.Crews) {
		t.Errorf("ids = %d, want %d", len(ids), len(seeddata.Crews))
	}
	if !strings.Contains(out, "! Link user to crew") || !strings.Contains(out, "HTTP 500") {
		t.Errorf("expected link failure lines:\n%s", out)
	}
	if !strings.Contains(out, "Linked user to 0/") {
		t.Errorf("expected 0 linked summary:\n%s", out)
	}
}

func TestSeedSkills_ImportFailureFallsThroughToNotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/workspaces/"+covWS+"/skills/import", clitest.ErrorResponse(500, "import broken"))

	agentSlug := ""
	for slug := range seeddata.SkillAssignments {
		agentSlug = slug
		break
	}
	out := captureStdoutCovCli2(t, func() {
		if err := seedSkills(context.Background(), newSeedClient(stub), map[string]string{agentSlug: covAgentIDCli2}); err != nil {
			t.Errorf("import failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "HTTP 500") {
		t.Errorf("expected import failure lines:\n%s", out)
	}
	// With zero skills resolvable, every assignment warns "not found".
	if !strings.Contains(out, "not found for agent "+agentSlug) {
		t.Errorf("expected not-found assignment warnings:\n%s", out)
	}
	if got := len(stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli2+"/skills")); got != 0 {
		t.Errorf("no assignments should be attempted, got %d", got)
	}
}

func TestSeedCredentials_AnthropicCreateFailureAborts(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	t.Setenv("SEED_ANTHROPIC_API_KEY", "")
	t.Setenv("SEED_GOOGLE_EMAIL", "")
	t.Setenv("SEED_GOOGLE_PASSWORD", "")
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/credentials", clitest.ErrorResponse(500, "creds broken"))

	_ = captureStdoutCovCli2(t, func() {
		err := seedCredentials(context.Background(), newSeedClient(stub), map[string]string{"tomas": covAgentIDCli2})
		if err == nil || !strings.Contains(err.Error(), "anthropic credential:") {
			t.Errorf("got %v", err)
		}
	})
}

func TestSeedCredentials_GoogleFailureWarnsOnly(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	t.Setenv("SEED_ANTHROPIC_API_KEY", "sk-real")
	t.Setenv("SEED_GOOGLE_EMAIL", "g@b.c")
	t.Setenv("SEED_GOOGLE_PASSWORD", "pw")

	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	// Anthropic POST succeeds; the Google credential (provider GOOGLE)
	// fails — provider is inspected from the request body.
	stub.OnPost("/api/v1/credentials", func(_ *http.Request, body []byte) (int, []byte, string) {
		var req map[string]string
		clitest.MustDecodeJSONBody(body, &req)
		if req["provider"] == "GOOGLE" {
			return 500, []byte(`{"error":"google rejected"}`), "application/json"
		}
		return 201, []byte(`{"id":"` + covCredIDCli2 + `"}`), "application/json"
	})
	stub.OnPost("/api/v1/agents/"+covAgentIDCli2+"/credentials", clitest.JSONResponse(201, map[string]string{"id": "ac1"}))

	out := captureStdoutCovCli2(t, func() {
		if err := seedCredentials(context.Background(), newSeedClient(stub), map[string]string{"tomas": covAgentIDCli2}); err != nil {
			t.Errorf("google failure must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "Google credential:") {
		t.Errorf("expected google warning:\n%s", out)
	}
	// Only the anthropic credential reaches assignment.
	if got := len(stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli2+"/credentials")); got != 1 {
		t.Errorf("assignment POSTs = %d, want 1", got)
	}
}

// killConn hijacks the connection and closes it without a response,
// so the CLI sees a transport-level error (EOF / connection reset)
// rather than an HTTP status.
func killConn(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("test server does not support hijacking")
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func TestSeedCrews_MemberLinkTransportErrorTolerated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/crews", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"` + covCrewIDCli2 + `"}`))
	})
	mux.HandleFunc("/api/v1/crews/"+covCrewIDCli2+"/members", func(w http.ResponseWriter, _ *http.Request) {
		killConn(w)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := cli.NewClient(srv.URL, "tok", covWS)
	var ids map[string]string
	out := captureStdoutCovCli2(t, func() {
		var err error
		ids, err = seedCrews(context.Background(), client, "cuser000000000000001")
		if err != nil {
			t.Errorf("member transport failures must not abort: %v", err)
		}
	})
	if len(ids) != len(seeddata.Crews) {
		t.Errorf("ids = %d, want %d", len(ids), len(seeddata.Crews))
	}
	if !strings.Contains(out, "! Link user to crew") || !strings.Contains(out, "Linked user to 0/") {
		t.Errorf("expected transport failure lines:\n%s", out)
	}
}

func TestSeedCrews_MidLoopCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/crews", func(w http.ResponseWriter, _ *http.Request) {
		cancel() // first crew lands, then the loop's ctx check fires
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"` + covCrewIDCli2 + `"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedCrews(ctx, cli.NewClient(srv.URL, "tok", covWS), ""); err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

func TestSeedCrewConnections_TransportAndMidLoopCancel(t *testing.T) {
	crews := map[string]string{"a": "c1", "b": "c2", "c": "c3"}

	// Transport error on every pair → warn + continue, no abort.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/crew-connections", func(w http.ResponseWriter, _ *http.Request) { killConn(w) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	out := captureStdoutCovCli2(t, func() {
		if err := seedCrewConnections(context.Background(), cli.NewClient(srv.URL, "tok", covWS), crews); err != nil {
			t.Errorf("transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "! Connect") || !strings.Contains(out, "Connected 0 new pair(s)") {
		t.Errorf("expected connect failure lines:\n%s", out)
	}

	// Cancel inside the pair loop: first POST succeeds, second iteration
	// hits the inner ctx check.
	ctx, cancel := context.WithCancel(context.Background())
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/api/v1/crew-connections", func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"cc"}`))
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	_ = captureStdoutCovCli2(t, func() {
		if err := seedCrewConnections(ctx, cli.NewClient(srv2.URL, "tok", covWS), crews); err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

func TestSeedAgents_MidLoopCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"` + covAgentIDCli2 + `"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	crewIDs := map[string]string{}
	for _, c := range seeddata.Crews {
		crewIDs[c.Slug] = covCrewIDCli2
	}
	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedAgents(ctx, cli.NewClient(srv.URL, "tok", covWS), crewIDs); err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

func TestSeedSkills_TransportErrorsAndMidLoopCancel(t *testing.T) {
	// Import POST dies at the transport level → warn + continue; then all
	// assignments warn "not found".
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/v1/workspaces/"+covWS+"/skills/import", func(w http.ResponseWriter, _ *http.Request) { killConn(w) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	agentSlug := ""
	for slug := range seeddata.SkillAssignments {
		agentSlug = slug
		break
	}
	out := captureStdoutCovCli2(t, func() {
		if err := seedSkills(context.Background(), cli.NewClient(srv.URL, "tok", covWS), map[string]string{agentSlug: covAgentIDCli2}); err != nil {
			t.Errorf("transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "! Skill") {
		t.Errorf("expected import transport failure lines:\n%s", out)
	}

	// Cancel during the import loop.
	ctx, cancel := context.WithCancel(context.Background())
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/api/v1/skills", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	mux2.HandleFunc("/api/v1/workspaces/"+covWS+"/skills/import", func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"skill_id":"cskillnew00000000001","slug":"x"}`))
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	_ = captureStdoutCovCli2(t, func() {
		if err := seedSkills(ctx, cli.NewClient(srv2.URL, "tok", covWS), map[string]string{agentSlug: covAgentIDCli2}); err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

func TestSeedSkills_AssignmentTransportErrorTolerated(t *testing.T) {
	existing := make([]map[string]string, 0, len(seeddata.Skills))
	for i, s := range seeddata.Skills {
		existing = append(existing, map[string]string{"id": "cskill0000000000000" + string(rune('1'+i)), "slug": s.Slug})
	}
	skillsJSON, _ := json.Marshal(existing)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(skillsJSON)
	})
	mux.HandleFunc("/api/v1/agents/"+covAgentIDCli2+"/skills", func(w http.ResponseWriter, _ *http.Request) { killConn(w) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	agentSlug := ""
	for slug := range seeddata.SkillAssignments {
		agentSlug = slug
		break
	}
	out := captureStdoutCovCli2(t, func() {
		if err := seedSkills(context.Background(), cli.NewClient(srv.URL, "tok", covWS), map[string]string{agentSlug: covAgentIDCli2}); err != nil {
			t.Errorf("assignment transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "! Assign "+agentSlug) {
		t.Errorf("expected assign failure lines:\n%s", out)
	}
}

func TestSeedCredentials_AssignmentTransportErrorTolerated(t *testing.T) {
	t.Setenv("SEED_ANTHROPIC_API_KEY", "sk-x")
	t.Setenv("SEED_GOOGLE_EMAIL", "")
	t.Setenv("SEED_GOOGLE_PASSWORD", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"` + covCredIDCli2 + `"}`))
	})
	mux.HandleFunc("/api/v1/agents/"+covAgentIDCli2+"/credentials", func(w http.ResponseWriter, _ *http.Request) { killConn(w) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := captureStdoutCovCli2(t, func() {
		if err := seedCredentials(context.Background(), cli.NewClient(srv.URL, "tok", covWS), map[string]string{"tomas": covAgentIDCli2}); err != nil {
			t.Errorf("assignment transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "! Assign credential to agent tomas") {
		t.Errorf("expected assign failure line:\n%s", out)
	}
	if !strings.Contains(out, "0/1 agents") {
		t.Errorf("expected zero-assignment summary:\n%s", out)
	}
}

func TestSeedOneCredential_TransportAndParseErrors(t *testing.T) {
	cred := seeddata.CredentialDef{Name: "K", Type: "API_KEY", Provider: "ANTHROPIC", EnvVarName: "K", Value: "v"}

	// POST dies at transport level.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		killConn(w)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := seedOneCredential(cli.NewClient(srv.URL, "tok", covWS), cred); err == nil {
		t.Error("expected transport error")
	}

	// Created but unparseable body → ReadJSON error.
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/credentials", clitest.TextResponse(201, "{not json"))
	if _, err := seedOneCredential(newSeedClient(stub), cred); err == nil {
		t.Error("expected parse error")
	}
}
