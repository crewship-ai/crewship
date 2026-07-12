package orchestrator

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// BuildEnvVars constructs the environment variables for a container exec,
// including agent identity, credentials (when sidecar is not used), and
// provider-specific settings. Lives in exec_env.go since the multi-CLI
// adapter refactor (the per-CLI command building moved to adapter_*.go);
// this function is provider-neutral and stays here next to its sidecar
// counterpart BuildEnvVarsSidecar.
func BuildEnvVars(req AgentRunRequest, activeCred *Credential) []string {
	env := []string{
		fmt.Sprintf("HOME=/crew/agents/%s", req.AgentSlug),
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
		"CREWSHIP_CREW_SHARED=/crew/shared",
	}

	if activeCred != nil {
		envVar := resolveEnvVar(activeCred)
		env = append(env, envVar+"="+activeCred.PlainValue)
	}

	for _, cred := range req.Credentials {
		if activeCred != nil && cred.ID == activeCred.ID {
			continue
		}
		if cred.EnvVarName != "" && cred.PlainValue != "" {
			envVar := resolveEnvVar(&cred)
			alreadySet := false
			for _, e := range env {
				if len(e) > len(envVar) && e[:len(envVar)+1] == envVar+"=" {
					alreadySet = true
					break
				}
			}
			if !alreadySet {
				env = append(env, envVar+"="+cred.PlainValue)
			}
		}
	}

	if e, ok := localModelConfigEnv(req); ok {
		env = append(env, e)
	}

	return env
}

func injectMCPCredentialEnvVars(req AgentRunRequest, env []string) []string {
	// Collect env var names referenced anywhere in the MCP config — env
	// blocks, headers, top-level URL strings, and (for Codex) the
	// bearer_token_env_var TOML key referenced indirectly via Authorization
	// headers. Substring match on regex so "Bearer ${LINEAR_TOKEN}" gets
	// picked up — the pre-fix prefix-suffix check missed every header in
	// every adapter, causing all HTTP MCP servers to 401 in production.
	mcpEnvRefs := collectMCPEnvRefs(req.CrewMCPConfigJSON, req.AgentMCPConfigJSON)

	// Also collect from table-based MCPServers (after JSON blob migration).
	// Substring-aware scan covers values like "Bearer ${TOKEN}" and bare
	// $VAR, not just whole-string ${VAR} as before.
	for _, srv := range req.MCPServers {
		for _, v := range srv.Env {
			for _, name := range extractEnvRefs(v) {
				mcpEnvRefs[name] = true
			}
		}
	}

	if len(mcpEnvRefs) == 0 {
		return env
	}

	// Build set of already-set env var names
	existing := make(map[string]bool)
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			existing[e[:idx]] = true
		}
	}

	// Match credentials to MCP env var references
	for _, cred := range req.Credentials {
		if cred.EnvVarName == "" || cred.PlainValue == "" {
			continue
		}
		if _, needed := mcpEnvRefs[cred.EnvVarName]; !needed {
			continue
		}
		if existing[cred.EnvVarName] {
			continue
		}
		env = append(env, cred.EnvVarName+"="+cred.PlainValue)
		existing[cred.EnvVarName] = true
	}

	return env
}

// envRefScanRE matches ${VAR}, $VAR (POSIX), and ${env:VAR} (Cursor) — all
// three forms our writers may emit. Anywhere in the value, not just at start
// or end. Hoisted to package level so we compile once.
var envRefScanRE = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// extractEnvRefs returns every env-var name referenced anywhere in the input
// string. Handles three forms a CLI's MCP config might emit:
//   - ${VAR}        (POSIX curly form, used by Claude / Gemini / Cursor /
//     Droid / Codex)
//   - $VAR          (POSIX bare form, also accepted by most CLIs)
//   - ${env:VAR}    (Cursor-specific syntax)
//
// Substring-aware so headers like "Bearer ${LINEAR_TOKEN}" (the dominant real
// world case) get picked up.
func extractEnvRefs(s string) []string {
	matches := envRefScanRE.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		// Submatches: [1]=Cursor env: form, [2]=curly form, [3]=bare form
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				out = append(out, m[i])
				break
			}
		}
	}
	return out
}

// collectMCPEnvRefs parses MCP config JSONs and returns env var names
// referenced ANYWHERE in the server definitions: env blocks, headers blocks,
// url strings (rare but possible). Substring-aware.
//
// Pre-fix scope was env blocks only with prefix-suffix matching — meaning
// every HTTP MCP server's Authorization header (like "Bearer ${LINEAR_TOKEN}")
// was silently missed and the bearer token never got injected, so all HTTP
// MCP servers hit upstream with literal "${LINEAR_TOKEN}" as the credential.
// Production-blocking gap; this rewrite closes it.
func collectMCPEnvRefs(configs ...string) map[string]bool {
	refs := make(map[string]bool)
	for _, cfg := range configs {
		if cfg == "" {
			continue
		}
		var wrapper struct {
			MCPServers map[string]struct {
				Env     map[string]string `json:"env"`
				Headers map[string]string `json:"headers"`
				URL     string            `json:"url"`
				HTTPURL string            `json:"httpUrl"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(cfg), &wrapper); err != nil {
			continue
		}
		for _, srv := range wrapper.MCPServers {
			for _, v := range srv.Env {
				for _, name := range extractEnvRefs(v) {
					refs[name] = true
				}
			}
			for _, v := range srv.Headers {
				for _, name := range extractEnvRefs(v) {
					refs[name] = true
				}
			}
			for _, name := range extractEnvRefs(srv.URL) {
				refs[name] = true
			}
			for _, name := range extractEnvRefs(srv.HTTPURL) {
				refs[name] = true
			}
		}
	}
	return refs
}

// BuildEnvVarsSidecar builds env vars for the agent when sidecar mode is active.
// API key credentials are NOT included -- the sidecar proxy injects them into HTTP requests.
// OAuth tokens (AI_CLI_TOKEN) are injected directly as CLAUDE_CODE_OAUTH_TOKEN because
// the sidecar cannot use them for x-api-key injection.
// When keeperEnabled is true, SECRET credentials are NOT included -- agents must
// request them via the Keeper API (/keeper/request on the sidecar).
// When keeperEnabled is false, SECRET credentials are injected as env vars directly.
// The agent gets dummy API keys and proxy configuration pointing to the sidecar.
func BuildEnvVarsSidecar(req AgentRunRequest, keeperEnabled bool) []string {
	// Check if we have an OAuth token -- this changes the env var strategy.
	// OAuth tokens use HTTPS CONNECT tunnel (sidecar just allowlists the domain).
	// Claude Code sets Authorization: Bearer itself inside the encrypted tunnel.
	// IMPORTANT: When OAuth is present, we must NOT set ANTHROPIC_API_KEY or
	// ANTHROPIC_BASE_URL because Claude Code prioritizes API key auth over OAuth
	// when both are present, and the dummy key causes authentication failure.
	hasOAuth := false
	var oauthToken string
	for _, cred := range req.Credentials {
		isOAuth := cred.Type == "AI_CLI_TOKEN" || strings.HasPrefix(cred.PlainValue, "sk-ant-oat")
		if isOAuth && cred.PlainValue != "" {
			hasOAuth = true
			oauthToken = cred.PlainValue
			break
		}
	}

	env := []string{
		fmt.Sprintf("HOME=/crew/agents/%s", req.AgentSlug),
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
		"CREWSHIP_CREW_SHARED=/crew/shared",
		// Proxy config -- all outbound HTTP goes through the sidecar
		"HTTP_PROXY=http://127.0.0.1:9119",
		"HTTPS_PROXY=http://127.0.0.1:9119",
		"http_proxy=http://127.0.0.1:9119",
		"https_proxy=http://127.0.0.1:9119",
		// SECURITY: NO_PROXY prevents infinite proxy loops for localhost health checks
		// and internal sidecar communication. Without this, curl/wget/Python requests
		// would try to proxy requests to 127.0.0.1 through the proxy itself.
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
	}

	if hasOAuth {
		// OAuth mode: Claude Code authenticates via HTTPS CONNECT tunnel.
		// The sidecar allowlists api.anthropic.com and passes the tunnel through.
		// No ANTHROPIC_BASE_URL (let Claude Code use the default HTTPS endpoint).
		// No dummy ANTHROPIC_API_KEY (would override OAuth authentication).
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
		// Still set dummy keys for other providers (OpenAI, Google) for sidecar injection
		env = append(env, "OPENAI_API_KEY=sk-dummy-crewship-sidecar")
		env = append(env, "GOOGLE_API_KEY=dummy-crewship-sidecar")
		// Tell the sidecar this exec is on a flat-rate subscription. Sidecar
		// uses this to tag cost_ledger rows correctly — flat-rate calls land
		// with cost=0 + confidence=unknown rather than fake $ figures, and
		// $-budget enforcement is skipped.
		env = append(env, "CREWSHIP_BILLING_MODE=flat_rate")
		env = append(env, "CREWSHIP_SUBSCRIPTION_PLAN=Anthropic Max")
	} else {
		// API key mode: use reverse proxy via ANTHROPIC_BASE_URL for credential injection.
		// The sidecar intercepts plain HTTP requests and injects the real API key.
		env = append(env,
			"ANTHROPIC_BASE_URL=http://127.0.0.1:9119",
			"ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar",
			"OPENAI_API_KEY=sk-dummy-crewship-sidecar",
			"GOOGLE_API_KEY=dummy-crewship-sidecar",
			// Metered: provider returns usage and ratecard pricing applies.
			"CREWSHIP_BILLING_MODE=metered",
		)
	}

	// Multi-CLI BYO API key path. The sidecar reverse-proxy is wired only for
	// api.anthropic.com today; Codex/Gemini/OpenCode/Cursor talk to their
	// upstream over HTTPS CONNECT through the sidecar (no x-api-key
	// injection). Override the dummy provider keys above with real values
	// from req.Credentials — but only for env vars that THIS adapter's CLI
	// actually reads. This preserves the sidecar isolation guarantee for
	// cross-adapter scenarios (e.g. a Claude Code agent in a workspace that
	// also has an OpenAI key configured — that key stays out of env).
	//
	// Future work: extend the sidecar reverse-proxy to api.openai.com,
	// generativelanguage.googleapis.com and api.cursor.sh so this leak path
	// can collapse back into the same x-api-key injection model the Anthropic
	// path uses today. Tracked in plan: t-m-ukulem-bude-purring-cray.md.
	allowed := apiKeyEnvVarsForAdapter(req.CLIAdapter)
	if len(allowed) > 0 {
		for _, cred := range req.Credentials {
			if cred.PlainValue == "" {
				continue
			}
			if _, ok := allowed[cred.EnvVarName]; !ok {
				continue
			}
			env = overrideEnv(env, cred.EnvVarName, cred.PlainValue)
			// gemini-cli reads either GOOGLE_API_KEY or GEMINI_API_KEY; mirror
			// the value into both so config differences across versions don't
			// stop authentication.
			if cred.EnvVarName == "GOOGLE_API_KEY" {
				env = overrideEnv(env, "GEMINI_API_KEY", cred.PlainValue)
			}
			if cred.EnvVarName == "GEMINI_API_KEY" {
				env = overrideEnv(env, "GOOGLE_API_KEY", cred.PlainValue)
			}
		}
	}

	// CLI_TOKEN credentials: injected as direct env vars (agent sees them).
	// CLI tools (gh, glab, vercel...) read credentials from env vars, not HTTP proxy.
	// The sidecar proxy cannot inject credentials into HTTPS CONNECT tunnels.
	for _, cred := range req.Credentials {
		if cred.Type == "CLI_TOKEN" && cred.EnvVarName != "" && cred.PlainValue != "" {
			env = append(env, cred.EnvVarName+"="+cred.PlainValue)
		}
	}

	// SECRET credentials: when Keeper is enabled, agents must request them via
	// the Keeper API (/keeper/request), enforcing access control + audit trail.
	// When Keeper is disabled, inject them directly as env vars (legacy mode).
	if !keeperEnabled {
		for _, cred := range req.Credentials {
			if cred.Type == "SECRET" && cred.EnvVarName != "" && cred.PlainValue != "" {
				env = append(env, cred.EnvVarName+"="+cred.PlainValue)
			}
		}
	}

	if e, ok := localModelConfigEnv(req); ok {
		env = append(env, e)
	}

	return env
}

// effectiveLocalModelBaseURL applies the #955 precedence: a URL resolved from
// an ENDPOINT_URL credential (already on the request) wins; the deprecated
// server-global CREWSHIP_LOCAL_MODEL_BASE_URL env value is used only when the
// credential path produced nothing. Returns the chosen URL and whether the
// env fallback was taken (so the caller can emit the one-time deprecation).
func effectiveLocalModelBaseURL(fromCredential, fromEnv string) (string, bool) {
	if fromCredential != "" {
		return fromCredential, false
	}
	if fromEnv != "" {
		return fromEnv, true
	}
	return "", false
}

// localModelPrefix marks an LLMModel as targeting the operator's local
// OpenAI-compatible endpoint. Mirrors isLocalModel in lib/cli-adapters.ts —
// keep both in sync.
const localModelPrefix = "ollama/"

// localModelConfigEnv builds the OPENCODE_CONFIG_CONTENT entry for the
// local-model path (#944): an OPENCODE agent selecting an "ollama/…" model on
// a server with cfg.LocalModels.BaseURL configured gets a generated provider
// block pointing OpenCode's openai-compatible driver at that endpoint. The
// JSON is always marshalled from a fixed struct — no user-controlled JSON
// reaches the env, so a hostile model name can't smuggle extra config keys.
func localModelConfigEnv(req AgentRunRequest) (string, bool) {
	if req.CLIAdapter != "OPENCODE" || req.LocalModelBaseURL == "" {
		return "", false
	}
	modelID := strings.TrimPrefix(req.LLMModel, localModelPrefix)
	if modelID == req.LLMModel || modelID == "" {
		return "", false // not an ollama/… model
	}
	type providerCfg struct {
		NPM     string `json:"npm"`
		Name    string `json:"name"`
		Options struct {
			BaseURL string            `json:"baseURL"`
			APIKey  string            `json:"apiKey,omitempty"`
			Headers map[string]string `json:"headers,omitempty"`
		} `json:"options"`
		Models map[string]struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	p := providerCfg{
		NPM:  "@ai-sdk/openai-compatible",
		Name: "Ollama (local)",
	}
	p.Options.BaseURL = req.LocalModelBaseURL
	// #961: optional auth for an authenticated endpoint. apiKey → the
	// @ai-sdk/openai-compatible driver auto-adds `Authorization: Bearer`;
	// headers is the escape hatch for Basic/custom-header/non-bearer schemes.
	// NOTE (#974 S2): OPENCODE_CONFIG_CONTENT is itself an agent env var, so
	// apiKey/headers DO land in the agent environment — the openai-compatible
	// driver dials the endpoint directly, so the sidecar proxy can't isolate
	// them. They are reported by AgentEnvCredentialExposures and redacted from
	// logs by the scrubber's (case-insensitive) apiKey pattern.
	p.Options.APIKey = req.LocalModelAPIKey
	if len(req.LocalModelHeaders) > 0 {
		p.Options.Headers = req.LocalModelHeaders
	}
	p.Models = map[string]struct {
		Name string `json:"name"`
	}{modelID: {Name: modelID}}
	cfg := map[string]any{"provider": map[string]providerCfg{"ollama": p}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		// Statically-shaped struct — marshal cannot realistically fail; treat
		// as "path disabled" rather than plumbing an error into env building.
		return "", false
	}
	return "OPENCODE_CONFIG_CONTENT=" + string(raw), true
}

// localModelExtraDomains returns the local endpoint's host when the
// local-model path is active, so restricted network mode auto-allowlists the
// traffic the operator explicitly enabled (same pattern as mcpStdioDomains).
// Empty in every other case — the exception never widens egress for crews
// that don't use a local model.
func localModelExtraDomains(req AgentRunRequest) []string {
	if _, ok := localModelConfigEnv(req); !ok {
		return nil
	}
	u, err := url.Parse(req.LocalModelBaseURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host := u.Hostname()
	// SSRF fence (#961): if the endpoint host is a literal IP, gate it here
	// before it ever reaches the sidecar allowlist. Hard-blocked ranges
	// (link-local/metadata/reserved) are refused unconditionally; RFC1918/
	// loopback are refused unless the crew opted into private-endpoint egress.
	// A non-literal hostname (e.g. host.docker.internal, which may resolve only
	// inside the container's network) is passed through — the sidecar does the
	// authoritative resolve-then-pin check at dial time, where it can actually
	// resolve the name. This keeps the host-side check synchronous and correct
	// for names crewshipd itself can't resolve.
	if ip := net.ParseIP(host); ip != nil {
		if httpsafe.IsBlockedIPForEndpoint(ip, req.AllowPrivateEndpoints) {
			// Refuse silently here — not added to the allowlist, so the
			// deny-by-default sidecar blocks it and emits the loud
			// network.egress journal entry the operator sees.
			return nil
		}
	}
	return []string{host}
}

// CredentialEnvExposure describes a credential whose plaintext value is placed
// directly into the agent container's environment by BuildEnvVarsSidecar, and is
// therefore readable by the agent process (e.g. via `env` or /proc/self/environ).
// It is the inverse of the isolation guarantee. API keys for the proxy-injected
// adapter are isolated by the sidecar reverse-proxy and never appear here, but the
// following DO land in the env: OAuth tokens (HTTPS CONNECT tunnels can't be
// proxied), BYO API keys for CONNECT-tunneled adapters, CLI tokens (read from env
// by the CLI tooling), and SECRET credentials with Keeper disabled. Surfacing these
// lets operators see and act on the credential-isolation gap rather than
// discovering it only by reading the code.
type CredentialEnvExposure struct {
	EnvVarName string
	Type       string
	// Reason explains why the value is in the env and, when Actionable, how to
	// close the gap.
	Reason string
	// Actionable is true when the operator can remediate the exposure through
	// configuration (today: enabling Keeper isolates SECRET credentials). OAuth
	// and CLI tokens are structurally un-isolatable behind the proxy, so they are
	// reported as informational (Actionable=false).
	Actionable bool
}

// AgentEnvCredentialExposures reports the credentials that BuildEnvVarsSidecar
// injects as plaintext into the agent environment, mirroring its injection logic
// exactly. The caller is expected to log the result so the isolation gap is
// observable instead of silent. It performs no logging and allocates only when an
// exposure actually exists.
func AgentEnvCredentialExposures(req AgentRunRequest, keeperEnabled bool) []CredentialEnvExposure {
	var out []CredentialEnvExposure

	// OAuth: BuildEnvVarsSidecar injects only the FIRST matching token as
	// CLAUDE_CODE_OAUTH_TOKEN and stops; mirror that so we don't over-report.
	for _, cred := range req.Credentials {
		isOAuth := cred.Type == "AI_CLI_TOKEN" || strings.HasPrefix(cred.PlainValue, "sk-ant-oat")
		if isOAuth && cred.PlainValue != "" {
			out = append(out, CredentialEnvExposure{
				EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN",
				Type:       "AI_CLI_TOKEN",
				Reason:     "OAuth token authenticates inside an HTTPS CONNECT tunnel the sidecar cannot inject into, so it must live in the agent env",
			})
			break
		}
	}

	// BYO API keys: CONNECT-tunneled adapters reach their upstream over an HTTPS
	// CONNECT tunnel and get the real key written into the env, because the sidecar
	// reverse-proxy only injects for the proxy-injected endpoint (the proxy-injected
	// adapter returns an empty set and stays isolated). Mirror BuildEnvVarsSidecar's
	// allowed-override loop exactly — one exposure per matching credential, keyed by
	// its own EnvVarName.
	if allowed := apiKeyEnvVarsForAdapter(req.CLIAdapter); len(allowed) > 0 {
		for _, cred := range req.Credentials {
			if cred.PlainValue == "" {
				continue
			}
			if _, ok := allowed[cred.EnvVarName]; !ok {
				continue
			}
			out = append(out, CredentialEnvExposure{
				EnvVarName: cred.EnvVarName,
				Type:       "API_KEY",
				Reason:     "adapter " + req.CLIAdapter + " reaches its upstream over an HTTPS CONNECT tunnel, so the real API key is written to env (the sidecar reverse-proxy only injects for api.anthropic.com)",
			})
		}
	}

	// CLI tokens: always injected to env — CLI tooling reads credentials from env
	// vars, which the HTTPS CONNECT proxy cannot rewrite.
	for _, cred := range req.Credentials {
		if cred.Type == "CLI_TOKEN" && cred.EnvVarName != "" && cred.PlainValue != "" {
			out = append(out, CredentialEnvExposure{
				EnvVarName: cred.EnvVarName,
				Type:       "CLI_TOKEN",
				Reason:     "CLI tools read credentials from env vars, which cannot be proxied",
			})
		}
	}

	// Local-model endpoint auth (#961/#974 S2): the apiKey/headers are embedded
	// in OPENCODE_CONFIG_CONTENT (an agent env var). The openai-compatible
	// driver dials the endpoint directly, so the sidecar reverse-proxy cannot
	// inject them — they are exposed to the agent process, like a CONNECT-
	// tunneled API key. Not actionable via config (it is the endpoint's auth).
	if req.LocalModelAPIKey != "" || len(req.LocalModelHeaders) > 0 {
		out = append(out, CredentialEnvExposure{
			EnvVarName: "OPENCODE_CONFIG_CONTENT",
			Type:       "ENDPOINT_URL",
			Reason:     "the local-model endpoint auth token/headers are embedded in the OpenCode config env var; the openai-compatible driver dials the endpoint directly, so the sidecar proxy cannot isolate them",
		})
	}

	// SECRET credentials: isolated behind the Keeper request/execute flow when
	// Keeper is enabled, but injected to env as a legacy fallback when it is off.
	// This is the one exposure an operator can close, so flag it actionable.
	if !keeperEnabled {
		for _, cred := range req.Credentials {
			if cred.Type == "SECRET" && cred.EnvVarName != "" && cred.PlainValue != "" {
				out = append(out, CredentialEnvExposure{
					EnvVarName: cred.EnvVarName,
					Type:       "SECRET",
					Reason:     "Keeper is disabled; enable it (set KEEPER_MODEL / KEEPER_OLLAMA_URL) to isolate SECRET credentials behind /keeper/request",
					Actionable: true,
				})
			}
		}
	}

	return out
}

// apiKeyEnvVarsForAdapter returns the set of env-var names whose presence the
// given CLI adapter's binary genuinely needs in order to authenticate. Used
// by BuildEnvVarsSidecar to decide which dummy provider keys to overwrite with
// real values from req.Credentials.
//
// Returning an empty / nil map means "this adapter relies on the sidecar
// reverse-proxy to inject credentials" — Claude Code's path. Returning a
// populated map means "this CLI talks directly to its upstream over HTTPS
// CONNECT and needs the real key in env".
func apiKeyEnvVarsForAdapter(adapter string) map[string]struct{} {
	switch adapter {
	case "CODEX_CLI":
		return map[string]struct{}{"OPENAI_API_KEY": {}}
	case "GEMINI_CLI":
		return map[string]struct{}{"GOOGLE_API_KEY": {}, "GEMINI_API_KEY": {}}
	case "OPENCODE":
		// OpenCode is BYOK across 75+ providers via models.dev. Accept all
		// the common provider env vars so users can route to whichever
		// upstream their opencode.json chose without us blocking the cred at
		// the sidecar layer. The list is the union of the most-deployed
		// providers in the wild — Anthropic, OpenAI, Google, plus the
		// alternative model gateways (OpenRouter, xAI, Groq, DeepSeek) and
		// Cursor's BYO key for users routing through Cursor.
		return map[string]struct{}{
			"ANTHROPIC_API_KEY":  {},
			"OPENAI_API_KEY":     {},
			"GOOGLE_API_KEY":     {},
			"GEMINI_API_KEY":     {},
			"OPENROUTER_API_KEY": {},
			"XAI_API_KEY":        {},
			"GROQ_API_KEY":       {},
			"DEEPSEEK_API_KEY":   {},
			// #944: remaining providers the OPENCODE model registry
			// advertises (lib/cli-adapters.ts) — env-var names follow the
			// models.dev/AI-SDK provider conventions OpenCode reads.
			"MOONSHOT_API_KEY": {},
			"ZAI_API_KEY":      {},
			"MINIMAX_API_KEY":  {},
		}
	case "CURSOR_CLI":
		return map[string]struct{}{"CURSOR_API_KEY": {}}
	case "FACTORY_DROID":
		return map[string]struct{}{"FACTORY_API_KEY": {}}
	default:
		// CLAUDE_CODE — sidecar's Anthropic reverse-proxy handles credential
		// injection (the dummy ANTHROPIC_API_KEY in env never reaches
		// api.anthropic.com; the proxy swaps it for the real value mid-flight).
		// Unknown adapters (e.g. malformed agent record) — defensive nil so
		// stale credentials don't leak into env.
		return nil
	}
}

// overrideEnv replaces (or appends) `key=value` in env, returning the updated
// slice. Used by BuildEnvVarsSidecar to swap dummy provider keys for the real
// values when a BYO API key is present in req.Credentials.
func overrideEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// resolveEnvVar returns the correct env var name for a credential.
// OAuth tokens (type AI_CLI_TOKEN or value prefix sk-ant-oat) must be set as
// CLAUDE_CODE_OAUTH_TOKEN -- Claude Code ignores them in ANTHROPIC_API_KEY.
func resolveEnvVar(cred *Credential) string {
	if cred.Type == "AI_CLI_TOKEN" || strings.HasPrefix(cred.PlainValue, "sk-ant-oat") {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return cred.EnvVarName
}

// DefaultEnvVarForProvider returns the conventional env var name for a CLI tool provider.
// Used by the UI to auto-suggest the env var when assigning a credential.
func DefaultEnvVarForProvider(provider string) string {
	switch provider {
	case "GITHUB":
		return "GH_TOKEN"
	case "GITLAB":
		return "GITLAB_TOKEN"
	case "VERCEL":
		return "VERCEL_TOKEN"
	case "AWS":
		return "AWS_ACCESS_KEY_ID"
	case "KUBERNETES":
		return "KUBECONFIG"
	default:
		return ""
	}
}
