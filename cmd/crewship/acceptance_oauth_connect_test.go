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
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for `crewship oauth`, driven through the BUILT BINARY rather than
// by calling RunE in-process — the same reasoning as
// acceptance_credential_openrouter_test.go: every one of these commands reaches
// the API through helpers whose path argument does not render to an `/api/…`
// literal, so cli_route_contract_test.go drops them silently and a green
// route-contract run says nothing about the body they put on the wire.
//
// What is asserted here is the wire contract, because that is what the handlers
// in internal/api/oauth_flow.go and oauth_creds.go read:
//
//   - `oauth exchange` MUST send `state`. The server only recovers the
//     server-side PKCE verifier when a state token comes back with the code
//     (internal/api/oauth_flow.go:247-275); drop it and the exchange runs
//     without a verifier, which a PKCE-enforcing provider rejects with
//     invalid_grant. The web UI drops it today.
//   - `oauth connect` MUST NOT report success on a flow that never completed.
//     There is no OAuth-specific wait endpoint, so the only truthful signal is
//     the credential flipping to ACTIVE; a timeout is a non-zero exit, not a
//     tick.
//   - `oauth auto-connect` MUST NOT report success on `status:"needs_client_id"`,
//     which is the shape the server returns when Dynamic Client Registration is
//     unavailable and no credential was created at all.
//
// No network: the stub is an httptest server on 127.0.0.1 and the binary is
// pointed at it through a config file, with the ambient CREWSHIP_* variables
// explicitly cleared so a box that exports CREWSHIP_SERVER cannot make a
// passing run mean something else.

// oauthStubServer answers the endpoints the `oauth` commands touch and records
// what was posted to each.
type oauthStubServer struct {
	mu sync.Mutex
	// posted maps request path -> decoded JSON body, so a case can assert both
	// what was sent and that it was sent at all.
	posted map[string]map[string]any
	// credStatus is what GET /api/v1/credentials/{id} reports. Tests flip it to
	// simulate the browser leg completing.
	credStatus string
	// credGets counts the status polls, so "did it actually poll?" is decidable.
	credGets int
	// autoConnectBody is the canned /auto-connect response.
	autoConnectBody string
	// discoverBody is the canned /discover response.
	discoverBody string
	// failNextCredGets makes that many status polls answer 502, simulating a
	// dropped keep-alive or a proxy hiccup mid-wait.
	failNextCredGets int
	// credGone makes every status poll answer 404 — the credential was deleted
	// or revoked while the flow was open.
	credGone bool
}

func (s *oauthStubServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	if s.credStatus == "" {
		s.credStatus = "PENDING"
	}
	if s.autoConnectBody == "" {
		s.autoConnectBody = `{"status":"authorize","auth_url":"https://provider.example/authorize?x=1","credential_id":"cred_auto_1"}`
	}
	if s.discoverBody == "" {
		s.discoverBody = `{"auth_url":"https://idp.example/authorize","token_url":"https://idp.example/token",` +
			`"registration_endpoint":"https://idp.example/register","scopes":"read write",` +
			`"supports_dcr":true,"supports_pkce":true,"source":"discovery"}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		record := func() map[string]any {
			raw, _ := io.ReadAll(r.Body)
			body := map[string]any{}
			_ = json.Unmarshal(raw, &body)
			s.mu.Lock()
			if s.posted == nil {
				s.posted = map[string]map[string]any{}
			}
			s.posted[r.URL.Path] = body
			s.mu.Unlock()
			return body
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/oauth/providers":
			_, _ = w.Write([]byte(`{
				"github":{"auth_url":"https://github.com/login/oauth/authorize","token_url":"https://github.com/login/oauth/access_token","default_scopes":"repo user"},
				"linear":{"auth_url":"https://linear.app/oauth/authorize","token_url":"https://api.linear.app/oauth/token","default_scopes":"read write"}
			}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/oauth/initiate":
			record()
			_, _ = w.Write([]byte(`{"auth_url":"https://provider.example/authorize?state=st_1","state":"st_1"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/oauth/exchange":
			record()
			_, _ = w.Write([]byte(`{"status":"ok","credential_id":"cred_stub_1"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/oauth/loopback":
			record()
			_, _ = w.Write([]byte(`{"auth_url":"https://provider.example/authorize?state=st_2","loopback_port":45231,"state":"st_2"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/oauth/discover":
			record()
			_, _ = w.Write([]byte(s.discoverBody))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/oauth/auto-connect":
			record()
			_, _ = w.Write([]byte(s.autoConnectBody))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/credentials/cred_stub_1":
			s.mu.Lock()
			s.credGets++
			status := s.credStatus
			gone := s.credGone
			transient := s.failNextCredGets > 0
			if transient {
				s.failNextCredGets--
			}
			s.mu.Unlock()
			switch {
			case gone:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"credential not found"}`))
			case transient:
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"upstream hiccup"}`))
			default:
				_, _ = w.Write([]byte(`{"id":"cred_stub_1","name":"linear-oauth","type":"OAUTH2","provider":"LINEAR","status":"` + status + `"}`))
			}

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/credentials":
			_, _ = w.Write([]byte(`[{"id":"cred_stub_1","name":"linear-oauth","type":"OAUTH2","provider":"LINEAR","status":"PENDING"}]`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/credentials":
			record()
			_, _ = w.Write([]byte(`{"id":"cred_stub_1","name":"linear-oauth"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/credentials/test":
			record()
			_, _ = w.Write([]byte(`{"valid":true,"supported":true}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// body returns the decoded POST body recorded for path, failing when nothing
// was posted there — a call that never reached the server would otherwise read
// as a row full of empty expectations.
func (s *oauthStubServer) body(t *testing.T, path string) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.posted[path]
	if !ok {
		t.Fatalf("POST %s was never called", path)
	}
	return b
}

func (s *oauthStubServer) setCredStatus(status string) {
	s.mu.Lock()
	s.credStatus = status
	s.mu.Unlock()
}

// hit reports whether a POST was recorded for path.
func (s *oauthStubServer) hit(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.posted[path]
	return ok
}

func (s *oauthStubServer) pollCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credGets
}

func oauthStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runOAuthCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runOAuthCLIStdin is runOAuthCLI with something on the binary's stdin —
// needed to drive the two flags that read it, --value-stdin and
// --oauth-client-secret-stdin, which is where the interesting conflict is.
func runOAuthCLIStdin(t *testing.T, cfgPath, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The provider catalogue is the entry point an agent reads before it decides
// which OAuth app to register, so it has to render both as a table and as JSON
// a script can index by provider slug.
func TestAcceptance_OAuthProviders(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "providers")
	if err != nil {
		t.Fatalf("providers: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"github", "linear", "https://linear.app/oauth/authorize"} {
		if !strings.Contains(out, want) {
			t.Errorf("provider table missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runOAuthCLI(t, cfg, "oauth", "providers", "--format", "json")
	if err != nil {
		t.Fatalf("providers --format json: %v\noutput: %s", err, jsonOut)
	}
	var rows []struct {
		Provider      string `json:"provider"`
		AuthURL       string `json:"auth_url"`
		TokenURL      string `json:"token_url"`
		DefaultScopes string `json:"default_scopes"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("providers --format json is not a JSON array: %v\noutput: %s", err, jsonOut)
	}
	byName := map[string]string{}
	for _, r := range rows {
		byName[r.Provider] = r.TokenURL
	}
	if byName["github"] != "https://github.com/login/oauth/access_token" {
		t.Errorf("github token_url = %q, want the catalogue value; rows=%+v", byName["github"], rows)
	}
}

// `oauth authorize` is the browser-redirect leg: it prints the URL and the
// state, because the state is what `oauth exchange` needs afterwards.
func TestAcceptance_OAuthAuthorizeSendsCredentialID(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	// By name, not id — the sibling credential commands resolve slug-or-id and
	// this one must too, or an agent has to run `credential list` first.
	out, err := runOAuthCLI(t, cfg, "oauth", "authorize", "linear-oauth")
	if err != nil {
		t.Fatalf("authorize: %v\noutput: %s", err, out)
	}

	body := stub.body(t, "/api/v1/oauth/initiate")
	if body["credential_id"] != "cred_stub_1" {
		t.Errorf("credential_id = %v, want the resolved id cred_stub_1", body["credential_id"])
	}
	if !strings.Contains(out, "https://provider.example/authorize") {
		t.Errorf("authorize output does not carry the auth URL:\n%s", out)
	}
	if !strings.Contains(out, "st_1") {
		t.Errorf("authorize output does not carry the state token, which `oauth exchange --state` needs:\n%s", out)
	}
}

// The whole reason this command exists: the state token must go back with the
// code so the server can recover the PKCE verifier it stored during initiate.
// The web UI omits it (components/features/mcp/components/oauth-form.tsx),
// which is why a PKCE-enforcing provider rejects that path.
func TestAcceptance_OAuthExchangeSendsStateSoPKCESurvives(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "exchange", "linear-oauth",
		"--code", "authcode-123", "--state", "st_1")
	if err != nil {
		t.Fatalf("exchange: %v\noutput: %s", err, out)
	}

	body := stub.body(t, "/api/v1/oauth/exchange")
	if body["credential_id"] != "cred_stub_1" {
		t.Errorf("credential_id = %v", body["credential_id"])
	}
	if body["code"] != "authcode-123" {
		t.Errorf("code = %v", body["code"])
	}
	if body["state"] != "st_1" {
		t.Errorf("state = %v, want st_1 — without it the server exchanges with no PKCE verifier", body["state"])
	}
}

// --code is not optional: an exchange with no code is a request the server
// answers 400, and a round trip to learn that is a round trip wasted.
func TestAcceptance_OAuthExchangeRequiresCode(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "exchange", "linear-oauth", "--state", "st_1")
	if err == nil {
		t.Fatalf("exchange with no --code succeeded\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	stub.mu.Lock()
	reached := stub.posted["/api/v1/oauth/exchange"]
	stub.mu.Unlock()
	if reached != nil {
		t.Errorf("a codeless exchange reached the server: %v", reached)
	}
}

// `oauth connect --no-wait` is the scripted half: fire the loopback, print
// where to point a browser, return immediately.
func TestAcceptance_OAuthConnectNoWaitPrintsAuthURLAndPort(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "connect", "linear-oauth", "--no-wait")
	if err != nil {
		t.Fatalf("connect --no-wait: %v\noutput: %s", err, out)
	}

	body := stub.body(t, "/api/v1/oauth/loopback")
	if body["credential_id"] != "cred_stub_1" {
		t.Errorf("credential_id = %v", body["credential_id"])
	}
	if !strings.Contains(out, "https://provider.example/authorize") {
		t.Errorf("connect --no-wait did not print the auth URL:\n%s", out)
	}
	if !strings.Contains(out, "45231") {
		t.Errorf("connect --no-wait did not print the loopback port:\n%s", out)
	}
	if stub.pollCount() != 0 {
		t.Errorf("--no-wait polled the credential %d times; it must return immediately", stub.pollCount())
	}
}

// The default is to wait, because "connected" is the state an agent actually
// needs, and the only signal for it is the credential flipping to ACTIVE.
func TestAcceptance_OAuthConnectWaitsForCredentialToGoActive(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	// The browser leg "completes" shortly after the command starts polling.
	// The sleep matters: a bare spin here pegs a core and contends with the
	// httptest handler for the same mutex for as long as the binary takes to
	// start, which is seconds.
	go func() {
		for stub.pollCount() < 1 {
			time.Sleep(20 * time.Millisecond)
		}
		stub.setCredStatus("ACTIVE")
	}()

	out, err := runOAuthCLI(t, cfg, "oauth", "connect", "linear-oauth",
		"--timeout", "20s", "--poll-interval", "100ms")
	if err != nil {
		t.Fatalf("connect: %v\noutput: %s", err, out)
	}
	if stub.pollCount() == 0 {
		t.Error("connect never polled the credential status")
	}
	if !strings.Contains(strings.ToLower(out), "connected") {
		t.Errorf("connect did not report the credential as connected:\n%s", out)
	}
}

// …and when it does not complete, it says so and exits non-zero. A tick over a
// flow that never finished is the failure mode this whole command exists to
// avoid: the tokens are simply not there, and the next agent run is the one
// that finds out.
func TestAcceptance_OAuthConnectTimeoutIsNotSuccess(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "connect", "linear-oauth",
		"--timeout", "1s", "--poll-interval", "100ms")
	if err == nil {
		t.Fatalf("connect reported success for a flow that never completed\noutput: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "connected successfully") {
		t.Errorf("connect claims a connection that never happened:\n%s", out)
	}
	if !strings.Contains(out, "PENDING") {
		t.Errorf("timeout message does not name the status the credential is stuck in:\n%s", out)
	}
}

// `--format quiet` still has to print the authorize URL. Formatter.AutoHuman
// routes quiet to the human closure, so treating it as a machine format here
// would suppress the one piece of output without which the flow cannot be
// completed at all — while still printing the success line afterwards.
func TestAcceptance_OAuthConnectQuietStillPrintsTheAuthURL(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "connect", "linear-oauth",
		"--no-wait", "--format", "quiet")
	if err != nil {
		t.Fatalf("connect --format quiet: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "https://provider.example/authorize") {
		t.Errorf("--format quiet swallowed the authorize URL, so the flow cannot be completed:\n%s", out)
	}
}

// A single transient failure mid-poll must not abandon the flow. The loopback
// listener is one-shot and its PKCE state lives only in the server goroutine's
// closure, so giving up here costs the operator the whole flow — including any
// consent they already granted — over one dropped connection.
func TestAcceptance_OAuthConnectSurvivesATransientPollFailure(t *testing.T) {
	stub := &oauthStubServer{failNextCredGets: 1}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	go func() {
		for stub.pollCount() < 2 {
			time.Sleep(20 * time.Millisecond)
		}
		stub.setCredStatus("ACTIVE")
	}()

	out, err := runOAuthCLI(t, cfg, "oauth", "connect", "linear-oauth",
		"--timeout", "20s", "--poll-interval", "100ms")
	if err != nil {
		t.Fatalf("one 502 mid-poll aborted the connect: %v\noutput: %s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "connected") {
		t.Errorf("connect did not report success after recovering:\n%s", out)
	}
}

// A credential that has been deleted or revoked mid-flow is not transient, and
// polling a 404 for the rest of the timeout helps nobody.
func TestAcceptance_OAuthConnectFailsFastOnAGoneCredential(t *testing.T) {
	stub := &oauthStubServer{credGone: true}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "connect", "linear-oauth",
		"--timeout", "30s", "--poll-interval", "100ms")
	if err == nil {
		t.Fatalf("connect succeeded against a credential the server 404s\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
	}
	if n := stub.pollCount(); n > 3 {
		t.Errorf("polled a 404 %d times; a gone credential is not a transient failure", n)
	}
}

// Discovery is read-only and reaches the provider's well-known documents, so
// the useful output is the endpoint triple plus whether DCR is on offer —
// that is what decides between `auto-connect` and registering an app by hand.
func TestAcceptance_OAuthDiscover(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "discover", "https://mcp.example/sse", "--format", "json")
	if err != nil {
		t.Fatalf("discover: %v\noutput: %s", err, out)
	}
	if body := stub.body(t, "/api/v1/oauth/discover"); body["mcp_url"] != "https://mcp.example/sse" {
		t.Errorf("mcp_url = %v", body["mcp_url"])
	}
	var got struct {
		AuthURL     string `json:"auth_url"`
		SupportsDCR bool   `json:"supports_dcr"`
		Source      string `json:"source"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("discover --format json is not JSON: %v\noutput: %s", err, out)
	}
	if got.AuthURL != "https://idp.example/authorize" || !got.SupportsDCR || got.Source != "discovery" {
		t.Errorf("discover = %+v", got)
	}
}

// auto-connect creates the credential itself when the provider supports
// Dynamic Client Registration, so the id it returns is the handle everything
// after it needs.
func TestAcceptance_OAuthAutoConnectReportsCredentialID(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "auto-connect", "https://mcp.example/sse",
		"--name", "example-mcp")
	if err != nil {
		t.Fatalf("auto-connect: %v\noutput: %s", err, out)
	}

	body := stub.body(t, "/api/v1/oauth/auto-connect")
	if body["mcp_url"] != "https://mcp.example/sse" {
		t.Errorf("mcp_url = %v", body["mcp_url"])
	}
	if body["server_name"] != "example-mcp" {
		t.Errorf("server_name = %v, want example-mcp", body["server_name"])
	}
	// provider_hint is deliberately never sent — see the note on the command.
	// Setting it makes the server skip discovery, which leaves it with no
	// registration endpoint, which makes DCR impossible, which makes the call
	// return needs_client_id every single time.
	if _, ok := body["provider_hint"]; ok {
		t.Errorf("provider_hint was sent; it can only make auto-connect fail: %v", body)
	}
	if !strings.Contains(out, "cred_auto_1") {
		t.Errorf("auto-connect output does not name the credential it created:\n%s", out)
	}
}

// --provider on auto-connect is a trap, not a feature, and must not exist.
// AutoConnect fills authURL from the catalogue when provider_hint matches
// (internal/api/oauth_creds.go:185-191), which makes the `if authURL == ""`
// discovery branch fall through, which leaves registrationEndpoint empty,
// which lands every such call in the no-DCR branch returning
// needs_client_id (:246). The flag could never produce a credential.
func TestAcceptance_OAuthAutoConnectHasNoProviderFlag(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "auto-connect", "https://mcp.example/sse",
		"--name", "example-mcp", "--provider", "linear")
	if err == nil {
		t.Fatalf("--provider was accepted on auto-connect, where it can only fail\noutput: %s", out)
	}
	if !strings.Contains(out, "unknown flag") {
		t.Errorf("expected cobra to reject the flag:\n%s", out)
	}
}

// The OAuth commands are only a usable contract if the credential they operate
// on can be created from the same binary. It could not: `credential create` had
// no way to set an OAuth app's client id or endpoints, so every OAUTH2 row had
// to be minted through the web UI or by hand against the API.
func TestAcceptance_CredentialCreateOAuth2FromProviderCatalogue(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "linear-oauth",
		"--type", "OAUTH2",
		"--oauth-provider", "linear",
		"--oauth-client-id", "client-abc",
		"--oauth-client-secret", "secret-xyz")
	if err != nil {
		t.Fatalf("create OAUTH2: %v\noutput: %s", err, out)
	}

	body := stub.body(t, "/api/v1/credentials")
	if body["type"] != "OAUTH2" {
		t.Errorf("type = %v", body["type"])
	}
	if body["oauth_client_id"] != "client-abc" {
		t.Errorf("oauth_client_id = %v", body["oauth_client_id"])
	}
	if body["oauth_client_secret"] != "secret-xyz" {
		t.Errorf("oauth_client_secret = %v", body["oauth_client_secret"])
	}
	// --oauth-provider fills the endpoints from the catalogue, so an operator
	// does not have to paste URLs the server already knows.
	if body["oauth_auth_url"] != "https://linear.app/oauth/authorize" {
		t.Errorf("oauth_auth_url = %v, want the catalogue value", body["oauth_auth_url"])
	}
	if body["oauth_token_url"] != "https://api.linear.app/oauth/token" {
		t.Errorf("oauth_token_url = %v, want the catalogue value", body["oauth_token_url"])
	}
	if body["oauth_scopes"] != "read write" {
		t.Errorf("oauth_scopes = %v, want the catalogue default", body["oauth_scopes"])
	}
	// No value: the row is PENDING until the flow lands tokens, and the server
	// substitutes its own sentinel. Sending one would be inventing a secret.
	if v, ok := body["value"]; ok && v != "" {
		t.Errorf("a value was sent for an OAUTH2 credential: %v", v)
	}
	// …and nothing was probed, because there is no key yet to probe.
	if stub.hit("/api/v1/credentials/test") {
		t.Error("POST /api/v1/credentials/test was called for a credential that holds no token yet")
	}
	if !strings.Contains(out, "oauth connect") {
		t.Errorf("create does not point at the command that finishes the flow:\n%s", out)
	}
}

// REGRESSION GUARD. `--type OAUTH2 --value <token>` with no OAuth app flags
// was already legal before the flags below existed — the server takes an
// OAUTH2 row with a value like any other. Demanding an --oauth-client-id from
// every OAUTH2 create would break it.
func TestAcceptance_CredentialCreateOAuth2WithValueAndNoAppFlags(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "preseeded-oauth", "--type", "OAUTH2", "--value", "already-a-token")
	if err != nil {
		t.Fatalf("OAUTH2 create with a bare value was refused: %v\noutput: %s", err, out)
	}

	body := stub.body(t, "/api/v1/credentials")
	if body["value"] != "already-a-token" {
		t.Errorf("value = %v, want the token verbatim", body["value"])
	}
	if _, ok := body["oauth_client_id"]; ok {
		t.Errorf("an oauth app was invented for a credential that asked for none: %v", body)
	}
}

// …and the two forms may not be mixed. Silently dropping the value — which is
// what folding an app onto a value-carrying create would do — would discard a
// token the operator explicitly passed.
func TestAcceptance_CredentialCreateOAuth2ValueAndAppFlagsConflict(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "both", "--type", "OAUTH2",
		"--oauth-client-id", "cid",
		"--oauth-auth-url", "https://idp.example/authorize",
		"--oauth-token-url", "https://idp.example/token",
		"--value", "a-token-that-would-be-dropped")
	if err == nil {
		t.Fatalf("--value alongside the OAuth app flags was accepted\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if stub.hit("/api/v1/credentials") {
		t.Error("the conflicting create reached the server")
	}
}

// …including when both halves of the conflict come off stdin, which is the
// case that used to escape.
//
// --value-stdin and --oauth-client-secret-stdin both read os.Stdin and there is
// only one stream. The conflict was checked against the RESOLVED value, by
// which point the secret had already eaten the piped line and `value` was ""
// — so the pair the operator was warned about sailed through and created an
// OAUTH2 row carrying app details and no token. Nothing said a word.
func TestAcceptance_CredentialCreateOAuth2ValueStdinAndSecretStdinConflict(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLIStdin(t, cfg, "one-line-for-two-readers\n",
		"credential", "create",
		"--name", "both-stdin", "--type", "OAUTH2",
		"--oauth-client-id", "cid",
		"--oauth-auth-url", "https://idp.example/authorize",
		"--oauth-token-url", "https://idp.example/token",
		"--oauth-client-secret-stdin",
		"--value-stdin")
	if err == nil {
		t.Fatalf("--value-stdin alongside --oauth-client-secret-stdin was accepted\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if !strings.Contains(out, "cannot be combined with the --oauth-* flags") {
		t.Errorf("the refusal did not name the conflict:\n%s", out)
	}
	if stub.hit("/api/v1/credentials") {
		t.Error("the conflicting create reached the server")
	}
}

// A provider slug the catalogue does not carry is caught locally: forwarding it
// would create a credential with no authorize URL, which `oauth connect` then
// 400s on with a message about the credential rather than the typo.
func TestAcceptance_CredentialCreateOAuth2UnknownProviderIsRejected(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "typo-oauth", "--type", "OAUTH2",
		"--oauth-provider", "linaer", "--oauth-client-id", "client-abc")
	if err == nil {
		t.Fatalf("unknown --oauth-provider was accepted\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if stub.hit("/api/v1/credentials") {
		t.Error("a credential with an unknown OAuth provider reached the server")
	}
	// The message has to name what IS available, or the operator is guessing.
	if !strings.Contains(out, "linear") {
		t.Errorf("error does not list the known providers:\n%s", out)
	}
}

// The CLI accepts --type oauth2 case-insensitively, so it must normalise before
// sending: the server compares req.Type == "OAUTH2" exactly
// (credentials_mutate.go:248), so a lower-case type skipped the OAUTH2 branch
// and came back `400 value is required` — an error naming a flag the operator
// deliberately omitted rather than the spelling that actually caused it.
func TestAcceptance_CredentialCreateOAuth2NormalizesTheTypeSpelling(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "lower-oauth", "--type", "oauth2",
		"--oauth-provider", "linear", "--oauth-client-id", "cid")
	if err != nil {
		t.Fatalf("create with --type oauth2: %v\noutput: %s", err, out)
	}
	if got := stub.body(t, "/api/v1/credentials")["type"]; got != "OAUTH2" {
		t.Errorf("type = %v, want the normalized OAUTH2 the server matches on", got)
	}
}

// The endpoints may also be given explicitly, for a provider the catalogue does
// not ship — a self-hosted GitLab, a private IdP.
func TestAcceptance_CredentialCreateOAuth2ExplicitEndpoints(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "self-hosted-oauth", "--type", "OAUTH2",
		"--oauth-client-id", "client-abc",
		"--oauth-auth-url", "https://gitlab.acme.internal/oauth/authorize",
		"--oauth-token-url", "https://gitlab.acme.internal/oauth/token",
		"--oauth-scopes", "api read_user")
	if err != nil {
		t.Fatalf("create OAUTH2 with explicit endpoints: %v\noutput: %s", err, out)
	}
	body := stub.body(t, "/api/v1/credentials")
	if body["oauth_auth_url"] != "https://gitlab.acme.internal/oauth/authorize" {
		t.Errorf("oauth_auth_url = %v", body["oauth_auth_url"])
	}
	if body["oauth_token_url"] != "https://gitlab.acme.internal/oauth/token" {
		t.Errorf("oauth_token_url = %v", body["oauth_token_url"])
	}
}

// An OAUTH2 credential with no client id has nothing to authorize with, and
// `oauth connect` would 400 on it. Caught before the row exists.
func TestAcceptance_CredentialCreateOAuth2NeedsClientID(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "half-oauth", "--type", "OAUTH2", "--oauth-provider", "linear")
	if err == nil {
		t.Fatalf("OAUTH2 credential with no client id was accepted\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if stub.hit("/api/v1/credentials") {
		t.Error("an OAUTH2 credential with no client id reached the server")
	}
}

// The OAuth flags belong to OAUTH2 and nothing else: on an API_KEY they would
// be silently dropped by the server, which is the worst of both worlds.
func TestAcceptance_CredentialCreateOAuthFlagsRejectedOnOtherTypes(t *testing.T) {
	stub := &oauthStubServer{}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "credential", "create",
		"--name", "plain", "--type", "API_KEY", "--provider", "GITHUB",
		"--value", "ghp_x", "--oauth-client-id", "client-abc")
	if err == nil {
		t.Fatalf("--oauth-client-id was accepted on --type API_KEY\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if stub.hit("/api/v1/credentials") {
		t.Error("the rejected credential still reached the server")
	}
}

// needs_client_id is a 200 that created nothing. Treating it as success is the
// exact shape of lie this repo's credential commands already guard against.
func TestAcceptance_OAuthAutoConnectNeedsClientIDIsNotSuccess(t *testing.T) {
	stub := &oauthStubServer{
		autoConnectBody: `{"status":"needs_client_id","auth_url":"https://idp.example/authorize",` +
			`"token_url":"https://idp.example/token","scopes":"read",` +
			`"message":"This provider requires a Client ID. Create an OAuth app in the provider's settings."}`,
	}
	srv := stub.start(t)
	cfg := oauthStubConfig(t, srv.URL)

	out, err := runOAuthCLI(t, cfg, "oauth", "auto-connect", "https://mcp.example/sse", "--name", "example-mcp")
	if err == nil {
		t.Fatalf("auto-connect treated needs_client_id as success\noutput: %s", out)
	}
	if !strings.Contains(out, "requires a Client ID") {
		t.Errorf("output does not relay the server's explanation:\n%s", out)
	}
	if strings.Contains(out, "cred_") {
		t.Errorf("output names a credential that was never created:\n%s", out)
	}
}
