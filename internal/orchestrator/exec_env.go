package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
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

	return env
}

func injectMCPCredentialEnvVars(req AgentRunRequest, env []string) []string {
	// Collect env var names referenced in crew/agent MCP configs
	mcpEnvRefs := collectMCPEnvRefs(req.CrewMCPConfigJSON, req.AgentMCPConfigJSON)

	// Also collect from table-based MCPServers (after JSON blob migration).
	// Only add the var name when the value is an explicit ${VAR}
	// reference. A literal value like Env: {"GH_TOKEN": "abc123"} is the
	// caller's authoritative choice; we must not silently shadow it with
	// a same-named credential below.
	for _, srv := range req.MCPServers {
		for _, v := range srv.Env {
			if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
				mcpEnvRefs[v[2:len(v)-1]] = true
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

// collectMCPEnvRefs parses MCP config JSONs and returns env var names
// referenced as ${VAR} in the "env" blocks of server definitions.
func collectMCPEnvRefs(configs ...string) map[string]bool {
	refs := make(map[string]bool)
	for _, cfg := range configs {
		if cfg == "" {
			continue
		}
		var wrapper struct {
			MCPServers map[string]struct {
				Env map[string]string `json:"env"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(cfg), &wrapper); err != nil {
			continue
		}
		for _, srv := range wrapper.MCPServers {
			for _, val := range srv.Env {
				// Match ${VAR_NAME} pattern
				if len(val) > 3 && val[0] == '$' && val[1] == '{' && val[len(val)-1] == '}' {
					refs[val[2:len(val)-1]] = true
				}
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

	return env
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
		// OpenCode is BYOK across providers — accept any of the three so the
		// user can route via whichever provider their opencode.json chose.
		return map[string]struct{}{
			"ANTHROPIC_API_KEY": {},
			"OPENAI_API_KEY":    {},
			"GOOGLE_API_KEY":    {},
		}
	case "CURSOR_CLI":
		return map[string]struct{}{"CURSOR_API_KEY": {}}
	case "FACTORY_DROID":
		return map[string]struct{}{"FACTORY_API_KEY": {}}
	default:
		// CLAUDE_CODE, FACTORY_DROID, unknown — no overrides; sidecar handles
		// Anthropic, Droid is out of scope this wave.
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
