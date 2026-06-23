package main

// Coverage tests for cmd_seed_data_integrations.go (seedIntegrations +
// resolveCrewIntegration). The seed data itself is static YAML in
// cmd/crewship/seeddata/builtin, so expectations are derived from that
// package at test time instead of hard-coding counts.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covSeedCrewIDs maps every crew slug referenced by the seed
// integrations to a deterministic CUID-shaped id.
func covSeedCrewIDs(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	n := 0
	for _, integ := range seeddata.Integrations {
		if _, ok := out[integ.CrewSlug]; !ok {
			out[integ.CrewSlug] = covSeedID(n)
			n++
		}
	}
	if len(out) == 0 {
		t.Fatal("seeddata.Integrations references no crews — schema drift?")
	}
	return out
}

// covSeedAgentIDs returns ids for every binding agent that exists in
// the static seed data AND belongs to a crew that has integrations.
func covSeedAgentIDs(crewIDs map[string]string) map[string]string {
	bySlug := map[string]seeddata.AgentDef{}
	for _, a := range seeddata.Agents {
		bySlug[a.Slug] = a
	}
	out := map[string]string{}
	n := 100
	for _, slug := range seeddata.AgentBindingSlugs {
		a, ok := bySlug[slug]
		if !ok {
			continue
		}
		if _, ok := crewIDs[a.CrewSlug]; !ok {
			continue
		}
		out[slug] = covSeedID(n)
		n++
	}
	return out
}

// covSeedID builds a CUID-shaped id ("c" + 20 lowercase alnum) that is
// unique per n.
func covSeedID(n int) string {
	return fmt.Sprintf("cseed%015dabcde", n)[:21]
}

// covClearOAuthEnv blanks the seed OAuth env vars so
// ResolveOAuthCredentials is deterministic regardless of the shell.
func covClearOAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SEED_LINEAR_OAUTH_ACCESS_TOKEN", "SEED_LINEAR_OAUTH_CLIENT_ID", "SEED_LINEAR_OAUTH_CLIENT_SECRET",
		"SEED_GOOGLE_OAUTH_ACCESS_TOKEN", "SEED_GOOGLE_OAUTH_CLIENT_ID", "SEED_GOOGLE_OAUTH_CLIENT_SECRET",
	} {
		t.Setenv(k, "")
	}
}

// covIntegrationCreateHandler returns 201 with an id derived from the
// posted integration name ("cid-<name>") so per-integration bindings
// can be asserted precisely.
func covIntegrationCreateHandler() clitest.Handler {
	return func(_ *http.Request, body []byte) (int, []byte, string) {
		var req struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body, &req)
		resp, _ := json.Marshal(map[string]string{"id": "cid-" + req.Name})
		return 201, resp, "application/json"
	}
}

func TestSeedIntegrations_HappyPathBindsAgents(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	agentIDs := covSeedAgentIDs(crewIDs)
	if len(agentIDs) == 0 {
		t.Fatal("no binding agents resolved from seed data — schema drift?")
	}

	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	for _, agentID := range agentIDs {
		stub.OnPost("/api/v1/agents/"+agentID+"/integrations", clitest.EmptyResponse(201))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, agentIDs)
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}

	// One create POST per seed integration.
	createCalls := 0
	for _, crewID := range crewIDs {
		createCalls += len(stub.CallsFor("POST", "/api/v1/crews/"+crewID+"/integrations"))
	}
	if createCalls != len(seeddata.Integrations) {
		t.Errorf("integration create calls = %d, want %d", createCalls, len(seeddata.Integrations))
	}

	// Each bound agent gets one binding per integration in its own crew.
	integPerCrew := map[string]int{}
	for _, integ := range seeddata.Integrations {
		integPerCrew[integ.CrewSlug]++
	}
	crewSlugByAgent := map[string]string{}
	for _, a := range seeddata.Agents {
		crewSlugByAgent[a.Slug] = a.CrewSlug
	}
	wantBindings := 0
	for slug, agentID := range agentIDs {
		want := integPerCrew[crewSlugByAgent[slug]]
		wantBindings += want
		calls := stub.CallsFor("POST", "/api/v1/agents/"+agentID+"/integrations")
		if len(calls) != want {
			t.Errorf("agent %s: %d binding calls, want %d", slug, len(calls), want)
			continue
		}
		for _, c := range calls {
			body := covDecodeBody(t, c.Body)
			if body["mcp_server_scope"] != "crew" || body["cred_type"] != "bearer" || body["enabled"] != true {
				t.Errorf("agent %s: binding body wrong: %v", slug, body)
			}
			id, _ := body["mcp_server_id"].(string)
			if !strings.HasPrefix(id, "cid-") {
				t.Errorf("agent %s: mcp_server_id = %q, want a created integration id", slug, id)
			}
			if _, hasCred := body["credential_id"]; hasCred {
				t.Errorf("agent %s: credential_id must be absent without OAuth env", slug)
			}
		}
	}

	if !strings.Contains(out, fmt.Sprintf("+ Bound %d agents, %d/%d bindings succeeded", len(agentIDs), wantBindings, wantBindings)) {
		t.Errorf("summary line missing/wrong: %q", out)
	}
}

func TestSeedIntegrations_ConflictRecoversViaLookup(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)

	for slug, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", clitest.ErrorResponse(409, "exists"))
		// The recovery lookup lists the crew's integrations.
		var existing []map[string]string
		for _, integ := range seeddata.Integrations {
			if integ.CrewSlug == slug {
				existing = append(existing, map[string]string{"id": "cid-" + integ.Name, "name": integ.Name})
			}
		}
		stub.OnGet("/api/v1/crews/"+crewID+"/integrations", clitest.JSONResponse(200, existing))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "= Integration exists:") {
		t.Errorf("expected idempotent-exists line, got %q", out)
	}
	if strings.Contains(out, "conflict but lookup failed") {
		t.Errorf("lookup should have recovered: %q", out)
	}
}

func TestSeedIntegrations_ConflictLookupFails(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", clitest.ErrorResponse(409, "exists"))
		stub.OnGet("/api/v1/crews/"+crewID+"/integrations", clitest.JSONResponse(200, []map[string]string{}))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "409 conflict but lookup failed") {
		t.Errorf("expected lookup-failed warning, got %q", out)
	}
}

func TestSeedIntegrations_HTTPErrorSkips(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", clitest.ErrorResponse(400, "bad request"))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "HTTP 400") {
		t.Errorf("expected HTTP 400 warning, got %q", out)
	}
}

func TestSeedIntegrations_ParseFailureSurfaced(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", clitest.TextResponse(200, "not json"))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "parse response") {
		t.Errorf("expected parse-response warning, got %q", out)
	}
}

func TestSeedIntegrations_MissingIDSurfaced(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", clitest.JSONResponse(200, map[string]string{}))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "response missing id") {
		t.Errorf("expected missing-id warning, got %q", out)
	}
}

func TestSeedIntegrations_UnknownCrewWarns(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, map[string]string{}, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "! Crew") || !strings.Contains(out, "not found for integration") {
		t.Errorf("expected unknown-crew warning, got %q", out)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("no HTTP calls expected when every crew is unknown, got %d", n)
	}
}

func TestSeedIntegrations_CancelledContext(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(ctx, client, covSeedCrewIDs(t), map[string]string{})
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context cancellation, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("no HTTP calls expected after cancellation, got %d", n)
	}
}

func TestSeedIntegrations_OAuthCredentialBinding(t *testing.T) {
	covClearOAuthEnv(t)
	t.Setenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN", "tok-123")

	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	agentIDs := covSeedAgentIDs(crewIDs)

	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": "ccred-linear"}))
	for _, agentID := range agentIDs {
		stub.OnPost("/api/v1/agents/"+agentID+"/integrations", clitest.EmptyResponse(201))
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, agentIDs)
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "+ OAuth credential: linear-oauth (ACTIVE)") {
		t.Errorf("OAuth credential creation line missing: %q", out)
	}

	credCalls := stub.CallsFor("POST", "/api/v1/credentials")
	if len(credCalls) != 1 {
		t.Fatalf("expected 1 credential POST, got %d", len(credCalls))
	}
	credBody := covDecodeBody(t, credCalls[0].Body)
	if credBody["type"] != "OAUTH2" || credBody["value"] != "tok-123" || credBody["name"] != "linear-oauth" {
		t.Errorf("credential body wrong: %v", credBody)
	}

	// The linear binding must carry credential_id; other integrations not.
	sawLinearWithCred := false
	for _, agentID := range agentIDs {
		for _, c := range stub.CallsFor("POST", "/api/v1/agents/"+agentID+"/integrations") {
			body := covDecodeBody(t, c.Body)
			if body["mcp_server_id"] == "cid-linear" {
				if body["credential_id"] != "ccred-linear" {
					t.Errorf("linear binding missing credential_id: %v", body)
				}
				sawLinearWithCred = true
			} else if _, ok := body["credential_id"]; ok {
				t.Errorf("non-linear binding must not carry credential_id: %v", body)
			}
		}
	}
	if !sawLinearWithCred {
		t.Error("no linear binding observed")
	}
}

func TestSeedIntegrations_OAuthConflictRecoversByName(t *testing.T) {
	covClearOAuthEnv(t)
	t.Setenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN", "tok-123")

	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	stub.OnPost("/api/v1/credentials", clitest.ErrorResponse(409, "exists"))
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccred-existing", "name": "linear-oauth"},
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if strings.Contains(out, "409 conflict but lookup failed") {
		t.Errorf("credential lookup should have recovered: %q", out)
	}
	if n := len(stub.CallsFor("GET", "/api/v1/credentials")); n != 1 {
		t.Errorf("expected 1 credential lookup, got %d", n)
	}
}

func TestSeedIntegrations_OAuthHTTPError(t *testing.T) {
	covClearOAuthEnv(t)
	t.Setenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN", "tok-123")

	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	stub.OnPost("/api/v1/credentials", clitest.ErrorResponse(500, "boom"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "! OAuth credential linear-oauth: HTTP 500") {
		t.Errorf("expected OAuth HTTP error warning, got %q", out)
	}
}

func TestSeedIntegrations_TransportErrorContinues(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	crewIDs := covSeedCrewIDs(t)
	stub.Close() // every POST fails at the transport level

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("transport failures must be absorbed per-integration, got %v", err)
	}
	if !strings.Contains(out, "! Integration") || !strings.Contains(out, "request failed") {
		t.Errorf("transport warnings missing: %q", out)
	}
}

func TestSeedIntegrations_CancelMidIntegrationLoop(t *testing.T) {
	if len(seeddata.Integrations) < 2 {
		t.Skip("needs at least 2 seed integrations to cancel mid-loop")
	}
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First create succeeds AND pulls the plug — the next loop
	// iteration's ctx check must abort the seed run.
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations",
			func(r *http.Request, body []byte) (int, []byte, string) {
				cancel()
				return 201, []byte(`{"id":"cid-x"}`), "application/json"
			})
	}

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(ctx, client, crewIDs, map[string]string{})
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected mid-loop cancellation, got %v", err)
	}
}

func TestSeedIntegrations_OAuthPendingWithoutToken(t *testing.T) {
	covClearOAuthEnv(t)
	t.Setenv("SEED_LINEAR_OAUTH_CLIENT_ID", "cid")
	t.Setenv("SEED_LINEAR_OAUTH_CLIENT_SECRET", "csecret")

	stub := clitest.NewStubServer()
	defer stub.Close()
	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": "ccred1"}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "+ OAuth credential: linear-oauth (PENDING)") {
		t.Errorf("token-less credential must be PENDING: %q", out)
	}
}

func TestSeedIntegrations_OAuthParseFailure(t *testing.T) {
	covClearOAuthEnv(t)
	t.Setenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN", "tok")

	stub := clitest.NewStubServer()
	defer stub.Close()
	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	stub.OnPost("/api/v1/credentials", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "! OAuth credential linear-oauth: parse response") {
		t.Errorf("expected OAuth parse warning, got %q", out)
	}
}

func TestSeedIntegrations_OAuthMissingID(t *testing.T) {
	covClearOAuthEnv(t)
	t.Setenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN", "tok")

	stub := clitest.NewStubServer()
	defer stub.Close()
	crewIDs := covSeedCrewIDs(t)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(200, map[string]string{}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, map[string]string{})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "! OAuth credential linear-oauth: response missing id") {
		t.Errorf("expected missing-id warning, got %q", out)
	}
}

func TestSeedIntegrations_BindAgentCrewUnknown(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	if len(seeddata.AgentBindingSlugs) == 0 {
		t.Fatal("no binding slugs in seed data")
	}
	bindSlug := seeddata.AgentBindingSlugs[0]

	// crewIDs is empty: the integration phase warns per-integration AND
	// the binding phase cannot resolve the agent's crew.
	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client,
			map[string]string{}, map[string]string{bindSlug: covAgentIDCli6})
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "! Bind "+bindSlug+": crew not found, skipping") {
		t.Errorf("expected bind-skip warning, got %q", out)
	}
}

func TestSeedIntegrations_BindHTTPErrorCounted(t *testing.T) {
	covClearOAuthEnv(t)
	stub := clitest.NewStubServer()
	defer stub.Close()

	crewIDs := covSeedCrewIDs(t)
	agentIDs := covSeedAgentIDs(crewIDs)
	for _, crewID := range crewIDs {
		stub.OnPost("/api/v1/crews/"+crewID+"/integrations", covIntegrationCreateHandler())
	}
	// Bindings deliberately unstubbed → fallback 404 → every binding
	// fails and the summary must reflect 0 successes.
	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return seedIntegrations(context.Background(), client, crewIDs, agentIDs)
	})
	if err != nil {
		t.Fatalf("seedIntegrations: %v", err)
	}
	if !strings.Contains(out, "HTTP 404") {
		t.Errorf("expected binding HTTP 404 warnings, got %q", out)
	}
	if !strings.Contains(out, "+ Bound 0 agents, 0/") {
		t.Errorf("summary must count zero successful bindings: %q", out)
	}
}

// ─── resolveCrewIntegration ──────────────────────────────────────────────

func TestResolveCrewIntegration_Found(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()

	stub.OnGet("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.JSONResponse(200, []map[string]string{
		{"id": "i1", "name": "linear"},
		{"id": "i2", "name": "github"},
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	id, err := resolveCrewIntegration(client, covCrewIDCli6, "github")
	if err != nil {
		t.Fatalf("resolveCrewIntegration: %v", err)
	}
	if id != "i2" {
		t.Errorf("id = %q, want i2", id)
	}
}

func TestResolveCrewIntegration_NotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()

	stub.OnGet("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.JSONResponse(200, []map[string]string{
		{"id": "i1", "name": "linear"},
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := resolveCrewIntegration(client, covCrewIDCli6, "ghost")
	if err == nil || !strings.Contains(err.Error(), `integration "ghost" not found in crew`) {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestResolveCrewIntegration_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()

	stub.OnGet("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := resolveCrewIntegration(client, covCrewIDCli6, "linear")
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestResolveCrewIntegration_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close() // closed server → connection refused

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := resolveCrewIntegration(client, covCrewIDCli6, "linear")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error, got %v", err)
	}
}
