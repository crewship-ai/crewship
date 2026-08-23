package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for the two credentials phase 2 adds, driven through the BUILT
// BINARY rather than by calling RunE in-process.
//
// That is the project's rule for an endpoint's CLI command, and here it is also
// the only honest coverage: cli_route_contract_test.go only inspects call sites
// whose path argument renders to a literal starting "/api/", so every credential
// command — which reaches the API through the helpers in api_helpers.go — is
// dropped from it silently. A green route-contract run says nothing about these
// commands, so the wire body they send is asserted here instead.
//
// No network: the stub is an httptest server on 127.0.0.1 and the binary is
// pointed at it through a config file, with the ambient CREWSHIP_* variables
// explicitly cleared so a box that exports CREWSHIP_SERVER cannot make a passing
// run mean something else.

// credStubServer answers the endpoints `credential create`/`list` touch and
// records what was posted to each.
type credStubServer struct {
	mu sync.Mutex
	// created is the decoded POST /api/v1/credentials body.
	created map[string]any
	// probed is the decoded POST /api/v1/credentials/test body, nil when the
	// probe was never called — which is itself an assertion (see the
	// OPENAI_COMPAT case).
	probed map[string]any
	// rotated is the decoded POST …/rotate body, nil when rotate was refused
	// client-side and never reached the server.
	rotated map[string]any
}

func (s *credStubServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/credentials/test":
			s.mu.Lock()
			raw, _ := io.ReadAll(r.Body)
			s.probed = map[string]any{}
			_ = json.Unmarshal(raw, &s.probed)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"valid":true,"supported":true}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/credentials":
			s.mu.Lock()
			raw, _ := io.ReadAll(r.Body)
			s.created = map[string]any{}
			_ = json.Unmarshal(raw, &s.created)
			name, _ := s.created["name"].(string)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"cred_stub_1","name":"` + name + `"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/credentials/cred_stub_1/rotate":
			s.mu.Lock()
			raw, _ := io.ReadAll(r.Body)
			s.rotated = map[string]any{}
			_ = json.Unmarshal(raw, &s.rotated)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"rot_1","credential_id":"cred_stub_1","rotated_at":"2026-01-01T00:00:00Z"}`))

		case r.URL.Path == "/api/v1/credentials/cred_stub_1":
			// GET is the metadata fetch `credential update` does before it
			// decides whether to validate; PATCH is the update itself.
			s.mu.Lock()
			name, _ := s.created["name"].(string)
			provider, _ := s.created["provider"].(string)
			credType, _ := s.created["type"].(string)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"cred_stub_1","name":"` + name + `","type":"` + credType +
				`","provider":"` + provider + `"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/credentials":
			s.mu.Lock()
			name, _ := s.created["name"].(string)
			provider, _ := s.created["provider"].(string)
			credType, _ := s.created["type"].(string)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`[{"id":"cred_stub_1","name":"` + name + `","type":"` + credType +
				`","provider":"` + provider + `","status":"ACTIVE","_count_agent_credentials":0,"security_level":1}]`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// createdBody returns the recorded create body, failing when nothing was
// posted — a create that never reached the server would otherwise read as a
// row full of empty expectations.
func (s *credStubServer) createdBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created == nil {
		t.Fatal("POST /api/v1/credentials was never called")
	}
	return s.created
}

func (s *credStubServer) probeCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probed != nil
}

// credStubConfig writes a CLI config pointing at the stub. Both a token and a
// workspace are needed: `credential create` calls requireAuth then
// requireWorkspace before it looks at a single flag.
func credStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// runCredCLI runs the built binary against the stub and returns its combined
// output plus the exit error, so a case can assert on either.
func runCredCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// An OpenRouter credential is an ordinary API key: the value goes to the server
// verbatim, the provider column carries the id the sidecar routes on, and the
// probe runs because crewshipd knows how to check an openrouter.ai key.
func TestAcceptance_CredentialCreate_OpenRouter(t *testing.T) {
	stub := &credStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	out, err := runCredCLI(t, cfg, "credential", "create",
		"--name", "openrouter-key",
		"--type", "API_KEY",
		"--provider", "OPENROUTER",
		"--value", "sk-or-v1-testvalue")
	if err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}

	body := stub.createdBody(t)
	if body["provider"] != "OPENROUTER" {
		t.Errorf("provider = %v, want OPENROUTER", body["provider"])
	}
	if body["type"] != "API_KEY" {
		t.Errorf("type = %v, want API_KEY", body["type"])
	}
	if body["value"] != "sk-or-v1-testvalue" {
		t.Errorf("value = %v, want the key verbatim", body["value"])
	}
	if !stub.probeCalled() {
		t.Error("POST /api/v1/credentials/test was not called; an OpenRouter key is checkable and must be checked")
	}

	// …and it lists. This is the second half of the contract an agent scripts
	// against: created rows come back on the surface that enumerates them.
	listOut, err := runCredCLI(t, cfg, "credential", "list")
	if err != nil {
		t.Fatalf("list: %v\noutput: %s", err, listOut)
	}
	if !strings.Contains(listOut, "openrouter-key") {
		t.Errorf("credential list does not show the created credential:\n%s", listOut)
	}
}

// The generic OpenAI-compatible endpoint: --base-url and the key are folded
// into ONE credential object, which is what lets the sidecar dial the endpoint
// without the key ever entering the agent container.
func TestAcceptance_CredentialCreate_OpenAICompatFoldsEndpointAndKey(t *testing.T) {
	stub := &credStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	// Lower-case provider on purpose: it must be normalized to the registry's
	// spelling, because the sidecar's lookup is case-sensitive.
	out, err := runCredCLI(t, cfg, "credential", "create",
		"--name", "self-hosted-llm",
		"--type", "API_KEY",
		"--provider", "openai_compat",
		"--base-url", "https://llm.internal.example/v1",
		"--value", "sk-internal-key",
		"--header", "X-Org=acme")
	if err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}

	body := stub.createdBody(t)
	if body["provider"] != "OPENAI_COMPAT" {
		t.Errorf("provider = %v, want the normalized OPENAI_COMPAT", body["provider"])
	}

	value, _ := body["value"].(string)
	var obj struct {
		BaseURL string            `json:"baseURL"`
		APIKey  string            `json:"apiKey"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(value), &obj); err != nil {
		t.Fatalf("stored value is not the endpoint object: %v (%q)", err, value)
	}
	if obj.BaseURL != "https://llm.internal.example/v1" {
		t.Errorf("baseURL = %q", obj.BaseURL)
	}
	if obj.APIKey != "sk-internal-key" {
		t.Errorf("apiKey = %q", obj.APIKey)
	}
	if obj.Headers["X-Org"] != "acme" {
		t.Errorf("headers = %v", obj.Headers)
	}

	// The base URL must not also be sent as a loose field: the server stores
	// the value, and a second copy of the endpoint would be a second source of
	// truth for where an agent's traffic goes.
	if _, ok := body["base_url"]; ok {
		t.Errorf("create body carries a separate base_url field: %v", body)
	}

	// Not probed, and the output says so rather than claiming a pass. Crewshipd
	// does not dial an operator-supplied endpoint, so a green "Key validated
	// successfully" here would be a tick over a check that never ran.
	if stub.probeCalled() {
		t.Error("POST /api/v1/credentials/test was called for an operator-supplied endpoint")
	}
	if strings.Contains(out, "Key validated successfully") {
		t.Errorf("output claims validation that never happened:\n%s", out)
	}
	if !strings.Contains(out, "not validated on create") {
		t.Errorf("output does not say the credential was left unchecked:\n%s", out)
	}
}

// The key may also arrive on --auth-token, the flag an operator who already
// writes #961 ENDPOINT_URL credentials will reach for. Same stored object.
func TestAcceptance_CredentialCreate_OpenAICompatAuthTokenForm(t *testing.T) {
	stub := &credStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	out, err := runCredCLI(t, cfg, "credential", "create",
		"--name", "self-hosted-llm",
		"--type", "API_KEY",
		"--provider", "OPENAI_COMPAT",
		"--base-url", "https://llm.internal.example/v1",
		"--auth-token", "sk-internal-key")
	if err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}

	value, _ := stub.createdBody(t)["value"].(string)
	var obj struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	}
	if err := json.Unmarshal([]byte(value), &obj); err != nil {
		t.Fatalf("stored value is not the endpoint object: %v (%q)", err, value)
	}
	if obj.BaseURL != "https://llm.internal.example/v1" || obj.APIKey != "sk-internal-key" {
		t.Errorf("stored object = %+v", obj)
	}
}

// Updating the value of a credential-supplied endpoint is not validated
// either, and says so. The same placebo is available on this path — the update
// command re-fetches the credential's provider and probes with it — so the
// guard has to be on both.
func TestAcceptance_CredentialUpdate_OpenAICompatIsNotProbed(t *testing.T) {
	stub := &credStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	out, err := runCredCLI(t, cfg, "credential", "create",
		"--name", "self-hosted-llm",
		"--type", "API_KEY",
		"--provider", "OPENAI_COMPAT",
		"--base-url", "https://llm.internal.example/v1",
		"--value", "sk-internal-key")
	if err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}

	out, err = runCredCLI(t, cfg, "credential", "update", "self-hosted-llm",
		"--value", `{"baseURL":"https://llm.internal.example/v1","apiKey":"sk-rotated"}`)
	if err != nil {
		t.Fatalf("update: %v\noutput: %s", err, out)
	}
	if stub.probeCalled() {
		t.Error("POST /api/v1/credentials/test was called on update for an operator-supplied endpoint")
	}
	if strings.Contains(out, "Key validated successfully") {
		t.Errorf("update claims validation that never happened:\n%s", out)
	}
	if !strings.Contains(out, "not validated on update") {
		t.Errorf("update does not say the value was left unchecked:\n%s", out)
	}
}

// Local validation failures. All of them are exit 2 per
// scripts/cli-exit-code-contract.sh, and none of them may reach the server —
// a half-formed endpoint credential that got stored would be delivered to a
// sidecar that then has nowhere to dial.
func TestAcceptance_CredentialCreate_EndpointFlagRejections(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText string
	}{
		{
			name: "--base-url on a fixed-upstream provider",
			args: []string{
				"--name", "or", "--type", "API_KEY", "--provider", "OPENROUTER",
				"--base-url", "https://openrouter.example/api/v1", "--value", "sk-or-v1-x",
			},
			wantText: "--base-url is only valid",
		},
		{
			name: "--base-url on a provider with no route at all",
			args: []string{
				"--name", "gh", "--type", "API_KEY", "--provider", "GITHUB",
				"--base-url", "https://ghe.acme.internal", "--value", "ghp_x",
			},
			wantText: "--base-url is only valid",
		},
		{
			name: "endpoint provider with no --base-url",
			args: []string{
				"--name", "compat", "--type", "API_KEY", "--provider", "OPENAI_COMPAT",
				"--value", "sk-internal-key",
			},
			wantText: "--base-url is required",
		},
		{
			name: "endpoint provider filed as a type that goes to the agent env",
			args: []string{
				"--name", "compat", "--type", "SECRET", "--provider", "OPENAI_COMPAT",
				"--base-url", "https://llm.internal.example/v1", "--value", "sk-internal-key",
			},
			wantText: "needs --type API_KEY",
		},
		{
			// No auth material of ANY kind — neither a token nor a header. An
			// endpoint with nothing to inject is not routed through the sidecar,
			// so there is nothing to isolate; the header-only form is accepted
			// and covered by TestAcceptance_CredentialCreate_EndpointAuthMaterialForms.
			name: "endpoint provider with a base URL but no auth material at all",
			args: []string{
				"--name", "compat", "--type", "API_KEY", "--provider", "OPENAI_COMPAT",
				"--base-url", "https://llm.internal.example/v1",
			},
			wantText: "--value, --value-stdin, --auth-token or --header is required",
		},
		{
			name: "malformed --header",
			args: []string{
				"--name", "compat", "--type", "API_KEY", "--provider", "OPENAI_COMPAT",
				"--base-url", "https://llm.internal.example/v1", "--value", "sk-internal-key",
				"--header", "noequals",
			},
			wantText: "--header must be KEY=VALUE",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &credStubServer{}
			srv := stub.start(t)
			out, err := runCredCLI(t, credStubConfig(t, srv.URL), append([]string{"credential", "create"}, tc.args...)...)
			if err == nil {
				t.Fatalf("expected a non-zero exit; output: %s", out)
			}
			if got := exitCodeOf(t, err); got != cli.ExitValidation {
				t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output missing %q:\n%s", tc.wantText, out)
			}
			stub.mu.Lock()
			created := stub.created
			stub.mu.Unlock()
			if created != nil {
				t.Errorf("a rejected credential reached the server: %v", created)
			}
		})
	}
}

// The route the credential will take is answerable from the same binary, with
// no server — which is the pairing an operator actually uses: create the
// credential, then ask where its traffic goes.
func TestAcceptance_CredentialThenRouteShow(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "route", "show", "OPENAI_COMPAT", "--format", "json")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	var row providerRouteRow
	if err := json.Unmarshal(out, &row); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if !row.UpstreamFromCredential || !row.RequiresCredential {
		t.Errorf("route show OPENAI_COMPAT = %+v", row)
	}
	if row.PathPrefix != "/llm/openai-compat" {
		t.Errorf("path prefix = %q", row.PathPrefix)
	}
}

// TestAcceptance_CredentialRotate_OpenAICompatKeyOnly drives the field-by-field
// rotate against the built binary.
//
// The gate this covers used to key on type == ENDPOINT_URL alone, so an
// endpoint-backed API_KEY credential was refused client-side with a message
// saying it is not an endpoint credential — leaving a full-value rotate, which
// replaces the endpoint along with the key, as the only way to change the key.
func TestAcceptance_CredentialRotate_OpenAICompatKeyOnly(t *testing.T) {
	stub := &credStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	// Create it first so the stub's metadata GET reports the right type+provider.
	if out, err := runCredCLI(t, cfg, "credential", "create",
		"--name", "self-hosted-llm",
		"--type", "API_KEY",
		"--provider", "OPENAI_COMPAT",
		"--base-url", "https://llm.internal.example/v1",
		"--auth-token", "sk-original"); err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}

	out, err := runCredCLI(t, cfg, "credential", "rotate", "self-hosted-llm",
		"--auth-token", "sk-rotated", "--yes")
	if err != nil {
		t.Fatalf("rotate --auth-token on an endpoint-backed API_KEY was refused: %v\noutput: %s", err, out)
	}

	stub.mu.Lock()
	body := stub.rotated
	stub.mu.Unlock()
	if body == nil {
		t.Fatal("POST …/rotate was never called — the CLI refused the flags before reaching the server")
	}
	if body["endpoint_auth_token"] != "sk-rotated" {
		t.Errorf("endpoint_auth_token = %v, want sk-rotated", body["endpoint_auth_token"])
	}
	// The endpoint must NOT be in the body: the server merges over the stored
	// value, and sending a blank base URL would erase it.
	if v, ok := body["endpoint_base_url"]; ok {
		t.Errorf("endpoint_base_url = %v was sent on a key-only rotate; the stored endpoint would be overwritten", v)
	}
}

// TestAcceptance_CredentialRotate_PlainKeyStillRefusesEndpointFlags keeps the
// footgun guard: --auth-token on a credential that stores no endpoint would
// otherwise write a JSON object over, say, a GitHub token.
func TestAcceptance_CredentialRotate_PlainKeyStillRefusesEndpointFlags(t *testing.T) {
	stub := &credStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	if out, err := runCredCLI(t, cfg, "credential", "create",
		"--name", "gh-token", "--type", "API_KEY", "--provider", "GITHUB",
		"--value", "ghp_something"); err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}

	out, err := runCredCLI(t, cfg, "credential", "rotate", "gh-token",
		"--auth-token", "sk-nope", "--yes")
	if err == nil {
		t.Fatalf("rotate accepted endpoint flags on a plain API key\noutput: %s", out)
	}
	stub.mu.Lock()
	rotated := stub.rotated
	stub.mu.Unlock()
	if rotated != nil {
		t.Error("the refused rotate still reached the server")
	}
	if !strings.Contains(out, "endpoint") {
		t.Errorf("error message does not explain the endpoint requirement: %s", out)
	}
}
