//go:build integration

package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/crewship-ai/crewship/internal/credpolicy"
)

// Deterministic local providers drive actual CLI shell tools without paid model
// calls or inherited logins. This fixture intentionally isolates env filtering
// from the separately tested container sandbox preflight.
func TestNativeCLIShellEnvironment(t *testing.T) {
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE"} {
		t.Run(adapter, func(t *testing.T) {
			for _, mode := range []string{"direct", "tmux", "fallback"} {
				t.Run(mode, func(t *testing.T) { testNativeCLIShellEnvironment(t, adapter, mode) })
			}
		})
	}
}

func testNativeCLIShellEnvironment(t *testing.T, adapter, mode string, vault ...func([]Credential) []Credential) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	home := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "synthetic CLI fixture")
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(block))
	// Exercise all legacy text shapes through actual native shell tools.
	if adapter == "CLAUDE_CODE" {
		privateKey = strings.ReplaceAll(privateKey, "\n", "\r\n")
	} else if adapter == "CODEX_CLI" {
		privateKey = strings.TrimSuffix(privateKey, "\n")
	}
	port := startNativeSSHFixture(t, signer)
	known := filepath.Join(home, "fixture-known-hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{fmt.Sprintf("[127.0.0.1]:%d", port)}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	generic := "fixture-file-$HOME\r\nopaque-final-LF\n"
	password := "fixture-password-$HOME\n"
	basicPassword := "fixture-curl-password-$HOME"
	basic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			if r.Header.Get("PRIVATE-TOKEN") != "fixture-glab-token" && r.Header.Get("Authorization") != "Bearer fixture-glab-token" {
				http.Error(w, "fixture GitLab authentication required", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"username":"fixture-glab-user"}`)
			return
		}
		if r.URL.Path == "/token" {
			if r.Header.Get("Authorization") != "Bearer fixture-token-value" {
				http.Error(w, "fixture token required", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, "fixture-token-authenticated")
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "fixture-user" || password != basicPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "fixture authentication required", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "fixture-basic-authenticated")
	}))
	defer basic.Close()
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err := credpolicy.ValidateFileValue("CERTIFICATE", certPEM); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCert)
	mtls := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "verified client required", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "fixture-mtls-authenticated")
	}))
	mtls.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots}
	mtls.StartTLS()
	defer mtls.Close()
	caFile := filepath.Join(home, "fixture-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mtls.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	req := AgentRunRequest{AgentSlug: "fixture", RunID: "fixture-run", CLIAdapter: adapter, ToolProfile: "FULL", UserMessage: "Run the fixture shell tool once and report its result.", LLMModel: "gpt-5-mini", Credentials: []Credential{
		{ID: "gitlab-token", Type: "CLI_TOKEN", EnvVarName: "GITLAB_TOKEN", PlainValue: "fixture-glab-token"},
		{ID: "token", Type: "CLI_TOKEN", EnvVarName: "NATIVE_TEST_TOKEN", PlainValue: "fixture-token-value"},
		{ID: "file", Type: "GENERIC_SECRET", EnvVarName: "NATIVE_TEST_SECRET_FILE", PlainValue: generic},
		{ID: "key", Type: "SSH_KEY", EnvVarName: "NATIVE_TEST_SSH_KEY", PlainValue: privateKey},
		{ID: "login", Type: "USERPASS", EnvVarName: "NATIVE_TEST_LOGIN", Username: "fixture-user", PlainValue: password},
		{ID: "basic-login", Type: "USERPASS", EnvVarName: "NATIVE_TEST_BASIC_LOGIN", Username: "fixture-user", PlainValue: basicPassword},
		{ID: "certificate", Type: "CERTIFICATE", EnvVarName: "NATIVE_TEST_CERT", PlainValue: strings.ReplaceAll(certPEM, "\n", "\r\n"), Fields: []CredentialField{{EnvVar: "NATIVE_TEST_CERT_KEY_PEM", Value: keyPEM, IsSecret: true}}},
	}}
	if len(vault) > 0 {
		req.Credentials = vault[0](req.Credentials)
	}
	script, _, _, err := buildCredFileScript(req.Credentials, home, true)
	if err != nil {
		t.Fatal(err)
	}
	writer := exec.CommandContext(ctx, "sh")
	writer.Stdin = strings.NewReader(script)
	if err := writer.Run(); err != nil {
		t.Fatal(err)
	}
	env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, home, true)
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`set -eu
test -z "${NATIVE_REVOKED_TOKEN+x}"
test "$NATIVE_TEST_TOKEN" = fixture-token-value
test "$(sha256sum "$NATIVE_TEST_SECRET_FILE" | cut -d ' ' -f1)" = %x
test -f "$NATIVE_TEST_LOGIN_USERNAME" && test -f "$NATIVE_TEST_LOGIN_PASSWORD"
test "$(cat "$NATIVE_TEST_LOGIN_USERNAME")" = fixture-user
test "$(sha256sum "$NATIVE_TEST_LOGIN_PASSWORD" | cut -d ' ' -f1)" = %x
test "$(stat -c %%a "$NATIVE_TEST_SSH_KEY_PATH")" = 600
test "$(stat -c %%a "$NATIVE_TEST_LOGIN_PASSWORD")" = 400
test "$(stat -c %%a "$NATIVE_TEST_CERT_PATH")" = 400
test "$(stat -c %%a "$NATIVE_TEST_CERT_KEY_PEM")" = 400
sh -c 'test "$NATIVE_TEST_TOKEN" = fixture-token-value && test -f "$NATIVE_TEST_SECRET_FILE" && test -f "$NATIVE_TEST_LOGIN_PASSWORD" && test -f "$NATIVE_TEST_SSH_KEY_PATH"'
test "$(ssh -F /dev/null -i "$NATIVE_TEST_SSH_KEY_PATH" -o IdentityAgent=none -o IdentitiesOnly=yes -o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o ClearAllForwardings=yes -o ConnectTimeout=3 -o UserKnownHostsFile="$NATIVE_TEST_KNOWN_HOSTS" -p %d fixture@127.0.0.1 whoami)" = 'fixture\native-user'
test "$(curl --silent --show-error --fail --noproxy '*' --connect-timeout 3 --max-time 5 --cacert "$NATIVE_TEST_CA_FILE" --cert "$NATIVE_TEST_CERT_PATH" --key "$NATIVE_TEST_CERT_KEY_PEM" "$NATIVE_TEST_MTLS_URL")" = fixture-mtls-authenticated
# curl reads this synthetic login through its native stdin configuration;
# neither the password nor its expansion is passed as a process argument.
test "$(printf 'user = "%%s:%%s"\n' "$(cat "$NATIVE_TEST_BASIC_LOGIN_USERNAME")" "$(cat "$NATIVE_TEST_BASIC_LOGIN_PASSWORD")" | curl --config - --silent --show-error --fail --noproxy '*' --connect-timeout 3 --max-time 5 "$NATIVE_TEST_BASIC_URL")" = fixture-basic-authenticated
test "$(printf 'Authorization: Bearer %%s\n' "$NATIVE_TEST_TOKEN" | curl --header @- --silent --show-error --fail --noproxy '*' --connect-timeout 3 --max-time 5 "$NATIVE_TEST_TOKEN_URL")" = fixture-token-authenticated
test "$(glab api --hostname "$NATIVE_TEST_GITLAB_URL" user)" = '{"id":1,"username":"fixture-glab-user"}'
printf '%%s%%s' NATIVE_ENV_ OK`, sha256.Sum256([]byte(generic)), sha256.Sum256([]byte(password)), port)
	var passed atomic.Bool
	var calls atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"input_tokens":10}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		followup := false
		if adapter == "CODEX_CLI" {
			input, _ := body["input"].([]any)
			for _, raw := range input {
				item, _ := raw.(map[string]any)
				if item["type"] == "function_call_output" {
					followup = true
					encoded, _ := json.Marshal(item["output"])
					if strings.Contains(string(encoded), "NATIVE_ENV_OK") {
						passed.Store(true)
					}
				}
			}
		} else {
			messages, _ := body["messages"].([]any)
			for _, raw := range messages {
				message, _ := raw.(map[string]any)
				if message["role"] == "tool" {
					followup = true
					encoded, _ := json.Marshal(message["content"])
					if strings.Contains(string(encoded), "NATIVE_ENV_OK") {
						passed.Store(true)
					}
				}
				content, _ := message["content"].([]any)
				for _, rawPart := range content {
					part, _ := rawPart.(map[string]any)
					if part["type"] == "tool_result" {
						followup = true
						encoded, _ := json.Marshal(part["content"])
						if strings.Contains(string(encoded), "NATIVE_ENV_OK") {
							passed.Store(true)
						}
					}
				}
			}
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event string, value any) {
			encoded, _ := json.Marshal(value)
			if event != "" {
				fmt.Fprintf(w, "event: %s\n", event)
			}
			fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		if adapter == "CLAUDE_CODE" {
			send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "fixture-message", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0}}})
			if !followup {
				send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "fixture-tool", "name": "Bash", "input": map[string]any{}}})
				args, _ := json.Marshal(map[string]any{"command": command, "description": "Check fixture credential environment"})
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
			} else {
				send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": "FIXTURE_DONE"}})
			}
			send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			reason := "tool_use"
			if followup {
				reason = "end_turn"
			}
			send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 10}})
			send("message_stop", map[string]any{"type": "message_stop"})
			return
		}
		if adapter == "OPENCODE" {
			delta := map[string]any{"role": "assistant"}
			finish := "tool_calls"
			if !followup {
				args, _ := json.Marshal(map[string]any{"command": command, "description": "Check fixture credential environment"})
				delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "fixture-tool", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}
			} else {
				delta["content"] = "FIXTURE_DONE"
				finish = "stop"
			}
			send("", map[string]any{"id": "fixture-chat", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
			send("", map[string]any{"id": "fixture-chat", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		var item map[string]any
		if !followup {
			args, _ := json.Marshal(map[string]any{"cmd": command, "yield_time_ms": 1000, "max_output_tokens": 300})
			item = map[string]any{"type": "function_call", "id": "fixture-call", "call_id": "fixture-tool", "name": "exec_command", "arguments": string(args), "status": "completed"}
		} else {
			item = map[string]any{"type": "message", "id": "fixture-final", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "FIXTURE_DONE", "annotations": []any{}}}}
		}
		response := map[string]any{"id": "fixture-response", "status": "completed", "model": "gpt-5-mini", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}
		send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		send("response.completed", map[string]any{"type": "response.completed", "response": response})
	}))
	defer fixture.Close()
	env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	env = overrideEnv(env, "HOME", home)
	env = overrideEnv(env, "NATIVE_TEST_KNOWN_HOSTS", known)
	env = overrideEnv(env, "NATIVE_TEST_CA_FILE", caFile)
	env = overrideEnv(env, "NATIVE_TEST_MTLS_URL", mtls.URL)
	env = overrideEnv(env, "NATIVE_TEST_BASIC_URL", basic.URL)
	env = overrideEnv(env, "NATIVE_TEST_TOKEN_URL", basic.URL+"/token")
	env = overrideEnv(env, "NATIVE_TEST_GITLAB_URL", basic.URL)
	env = overrideEnv(env, "GLAB_CONFIG_DIR", filepath.Join(home, "glab"))
	env = append(env, "GLAB_CHECK_UPDATE=false", "GLAB_SEND_TELEMETRY=false", "GLAB_NO_PROMPT=true")
	if err := os.MkdirAll(filepath.Join(home, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	env = overrideEnv(env, "CODEX_HOME", filepath.Join(home, "codex"))
	env = overrideEnv(env, "ANTHROPIC_API_KEY", "synthetic-fixture-key")
	env = overrideEnv(env, "ANTHROPIC_BASE_URL", fixture.URL)
	env = append(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_CONFIG_PROJECT_DISABLE=1", "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"))
	var argv []string
	switch adapter {
	case "CLAUDE_CODE":
		req.LLMModel = "claude-sonnet-5-5"
		argv = (claudeCodeAdapter{}).BuildCommand(req)
		mcp := filepath.Join(home, ".mcp.json")
		if err := os.WriteFile(mcp, []byte(`{"mcpServers":{}}`), 0600); err != nil {
			t.Fatal(err)
		}
		for i := range argv {
			if argv[i] == "--mcp-config" {
				argv[i+1] = mcp
			}
		}
	case "CODEX_CLI":
		argv = (codexAdapter{}).BuildCommand(req)
		for i := range argv {
			if argv[i] == "workspace-write" {
				argv[i] = "danger-full-access"
			}
		}
		argv = append([]string{argv[0], "--config", `model_provider="fixture"`, "--config", `model_providers.fixture.name="Fixture"`, "--config", fmt.Sprintf("model_providers.fixture.base_url=%q", fixture.URL), "--config", `model_providers.fixture.wire_api="responses"`, "--config", `model_providers.fixture.requires_openai_auth=false`}, argv[1:]...)
	case "OPENCODE":
		req.LLMModel = "fixture/fixture"
		configuration := map[string]any{"provider": map[string]any{"fixture": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Fixture", "options": map[string]any{"baseURL": fixture.URL + "/v1", "apiKey": "synthetic-fixture-key"}, "models": map[string]any{"fixture": map[string]any{"name": "Fixture", "limit": map[string]any{"context": 200000, "output": 1000}}}}}, "permission": map[string]any{"*": "allow"}}
		encoded, _ := json.Marshal(configuration)
		env = overrideEnv(env, "OPENCODE_CONFIG_CONTENT", string(encoded))
		argv = (opencodeAdapter{}).BuildCommand(req)
	}
	glabVersion := exec.CommandContext(ctx, "glab", "version")
	glabVersion.Env = env
	if out, err := glabVersion.Output(); err != nil {
		t.Fatal("required native glab unavailable", err)
	} else {
		t.Logf("native GitLab CLI version: %s", strings.TrimSpace(string(out)))
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		t.Fatal("required native CLI unavailable", err)
	}
	version := exec.CommandContext(ctx, path, "--version")
	version.Env = env
	if out, err := version.Output(); err == nil {
		t.Logf("native CLI version: %s", strings.TrimSpace(string(out)))
	} else {
		t.Fatal("cannot identify native CLI version", err)
	}
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Env = env
	cmd.Dir = home
	if mode != "direct" {
		cmd = nativeTmuxCLICommand(t, ctx, append([]string{path}, argv[1:]...), env, home, mode == "fallback")
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !passed.Load() {
		t.Fatalf("native %s shell did not verify credential delivery (calls=%d): %v\n%s", adapter, calls.Load(), err, out)
	}
	if mode == "fallback" && !strings.Contains(string(out), "tmux: not found") {
		t.Fatal("fixture did not observe the intended native tmux failure")
	}
}
