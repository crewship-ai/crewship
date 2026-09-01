package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/credpolicy"
	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/llmroute"
)

// baseAgentEnv returns the agent-identity env entries shared by every exec
// (with and without sidecar): HOME plus the CREWSHIP_* identity variables.
// Order matters — BuildEnvVars and BuildEnvVarsSidecar both start from this
// exact sequence.
func baseAgentEnv(req AgentRunRequest) []string {
	return []string{
		fmt.Sprintf("HOME=/crew/agents/%s", req.AgentSlug),
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
		"CREWSHIP_CREW_SHARED=/crew/shared",
	}
}

// BuildEnvVars constructs the environment variables for a container exec,
// including agent identity, credentials (when sidecar is not used), and
// provider-specific settings. Lives in exec_env.go since the multi-CLI
// adapter refactor (the per-CLI command building moved to adapter_*.go);
// this function is provider-neutral and stays here next to its sidecar
// counterpart BuildEnvVarsSidecar.
//
// #2092/#2246: this is the non-sidecar delivery path — taken both when the
// sidecar is disabled instance-wide and when a worker sub-agent is dispatched
// with SkipSidecar=true (the crew's sidecar is already running in the shared
// container; this exec just doesn't start a new one). Every credential
// written here is gated by credEnvDeliverable first, same as every selector
// in BuildEnvVarsSidecar, so an unclassified type (credpolicy fallback
// Delivery: DeliveryNone) is withheld here too — otherwise the same
// credential in the same Keeper state would be withheld from a parent agent's
// sidecar-built env and delivered anyway to a sub-agent taking this path.
// This function has never taken a keeperEnabled parameter and that is
// unchanged: DeliveryNone means no channel in EITHER Keeper state, so the
// credpolicy gate alone is sufficient here without threading Keeper state
// through. Extending this path to also honour KeeperGated for classified
// types (SECRET) is a separate, larger behaviour change — this path has
// historically delivered SECRET plaintext directly regardless of Keeper, and
// that is out of scope for the #2092 fix, which is specifically about the
// unclassified-type fail-safe promise, not about widening Keeper enforcement
// to a path that never had it.
func BuildEnvVars(req AgentRunRequest, activeCred *Credential) []string {
	env := baseAgentEnv(req)

	if activeCred != nil {
		// #2092/#2246: a further review found this selector still unguarded
		// — the non-sidecar path never consulted credpolicy at
		// all, so an unclassified credential (fallback Delivery: DeliveryNone)
		// reached the agent env here even though every selector in
		// BuildEnvVarsSidecar now withholds it. A worker sub-agent dispatched
		// with SkipSidecar=true takes exactly this path, so the same
		// credential in the same Keeper state was withheld from the parent
		// agent and delivered to its sub-agent. Gating on credEnvDeliverable
		// alone (not keeperEnabled) matches what DeliveryNone means: no
		// channel at all, in either Keeper state — this function has never
		// taken a keeperEnabled parameter and pre-existing SECRET/Keeper
		// behaviour on this path is unchanged; only the unclassified-type gap
		// is closed here.
		if credEnvDeliverable(*activeCred) {
			envVar := resolveEnvVar(activeCred)
			env = append(env, envVar+"="+activeCred.PlainValue)
			env = appendCredentialFields(env, *activeCred, true)
		} else {
			slog.Default().Warn("credential withheld from agent env: type has no credpolicy delivery row",
				"agent_slug", req.AgentSlug, "cred_type", activeCred.Type,
				"hint", "add a credpolicy.TypePolicy row for this type in internal/credpolicy")
		}
	}

	for _, cred := range req.Credentials {
		if activeCred != nil && cred.ID == activeCred.ID {
			continue
		}
		if cred.EnvVarName != "" && cred.PlainValue != "" {
			// #2092/#2246: same gate as above, applied to every OTHER
			// credential this request carries — the loop that previously wrote
			// every credential's plaintext unconditionally.
			if !credEnvDeliverable(cred) {
				slog.Default().Warn("credential withheld from agent env: type has no credpolicy delivery row",
					"agent_slug", req.AgentSlug, "env_var", cred.EnvVarName, "cred_type", cred.Type,
					"hint", "add a credpolicy.TypePolicy row for this type in internal/credpolicy")
				continue
			}
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
			// The credential's parts go wherever its value goes. There is no
			// sidecar on this path, so no isolation to respect and no
			// secret/identifier distinction to make.
			env = appendCredentialFields(env, cred, true)
		}
	}

	// viaSidecar=false: there is no sidecar on this path, so nothing can be
	// proxy-routed and the generated block keeps pointing straight at the
	// endpoint, auth material and all.
	if e, ok := localModelConfigEnv(req, false); ok {
		env = append(env, e)
	}

	return env
}

// appendCredentialFields adds a credential's named parts (PRD-CREDENTIALS-V2
// §2.2) to an env block. secretsToo decides whether the parts that ARE
// credential material come along; identifier parts always do.
//
// Why the split. A non-secret part is a region, an account id, a host — stored
// cleartext by design, and there is no channel here that could carry it for us:
// the sidecar reverse proxy injects a credential into an outbound HTTP request,
// it cannot inject a region into the agent's environment. Withholding it would
// mean the AWS-shaped credential the whole feature exists for arrives without
// the one part that says which account it is. A SECRET part, by contrast, is the
// same kind of thing as the credential's value, so it goes exactly where the
// value goes and nowhere else.
//
// A part NEVER overwrites a name already in the block. The API tier resolved
// collisions against everything it could see (internal/api/credential_field_delivery.go),
// but it cannot see what this package adds at mount time — HOME, the proxy
// fence, the dummy provider keys — so this is the last-line check, and it must
// stay a check rather than becoming an override.
func appendCredentialFields(env []string, cred Credential, secretsToo bool) []string {
	for _, f := range cred.Fields {
		if f.EnvVar == "" || f.Value == "" {
			continue
		}
		if f.IsSecret && !secretsToo {
			continue
		}
		if envHasName(env, f.EnvVar) {
			continue
		}
		env = append(env, f.EnvVar+"="+f.Value)
	}
	return env
}

// envHasName reports whether the block already assigns name.
func envHasName(env []string, name string) bool {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// envHasAssignment reports whether the block assigns exactly name=value.
//
// This is how the sidecar path decides whether a credential's SECRET parts may
// be delivered: they go to the agent env if and only if the credential's own
// plaintext already did. Asking the finished env block, rather than re-deriving
// the rules (OAuth vs proxy-injected vs CLI token vs Keeper), means a future
// change to those rules carries the parts with it automatically. The
// value-equality check is the whole point — ANTHROPIC_API_KEY is present for a
// proxy-isolated credential too, holding the dummy, and a name-only check would
// read that as "the real key is exposed, so its parts may be as well".
func envHasAssignment(env []string, name, value string) bool {
	assignment := name + "=" + value
	for _, e := range env {
		if e == assignment {
			return true
		}
	}
	return false
}

// credEnvDeliverable reports whether cred's TYPE has any delivery channel to
// the agent environment at all, per credpolicy. Every site in this file that
// is about to write a credential's plaintext into the agent's env — no
// matter which selection rule matched it (an OAuth-shaped value, an MCP
// config's ${VAR} reference, an adapter's recognized API-key env-var name,
// the CLI_TOKEN type, the legacy Keeper-off SECRET fallback, or the plain
// no-sidecar BuildEnvVars path) — must gate on this first.
//
// #2092/#2246: a type with no explicit credpolicy row carries the fail-safe
// fallback {Delivery: DeliveryNone, KeeperGated: true}. Only one of six
// selectors across BuildEnvVarsSidecar and BuildEnvVars originally consulted
// credpolicy at all (the legacy fallback loop), so an unclassified credential
// could still reach the agent env — in the OAuth-shaped case even with
// Keeper ON, and via BuildEnvVars regardless of Keeper state since that
// function never took a keeperEnabled parameter at all — through whichever
// selector matched it by value shape or env-var name instead of by type.
// Routing every selector through one predicate closes the whole class at
// once, instead of each selector remembering its own copy of the same guard
// (the shape of bug that produced this in the first place).
func credEnvDeliverable(cred Credential) bool {
	return credpolicy.For(cred.Type).Delivery != credpolicy.DeliveryNone
}

// injectMCPCredentialEnvVars adds the actual credential values for env vars
// referenced by the MCP config (${VAR}) into the exec env, so Claude Code and
// the other CLIs can expand "Bearer ${TOKEN}" style references at MCP startup.
//
// #1362 — Keeper gate: this is the THIRD credential-delivery path (alongside the
// file path and BuildEnvVarsSidecar), and it must honour the same SECRET-vs-
// Keeper withholding contract. When keeperEnabled is true, a SECRET-typed
// credential is NOT injected here — even if an MCP config references it — because
// the Keeper promise ("the value is not in your environment") would otherwise be
// bypassed via /proc/self/environ. The MCP process runs inside the agent
// container and cannot call /keeper/request itself, so a genuinely-needed value
// must be delivered via a non-SECRET credential type or without Keeper-gating
// that secret (see docs/guides/credentials.mdx). Non-SECRET types (API_KEY,
// CLI_TOKEN, OAUTH2, GENERIC_SECRET, …) and Keeper-off are unchanged.
func injectMCPCredentialEnvVars(req AgentRunRequest, env []string, keeperEnabled bool, logger *slog.Logger) []string {
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
		// #2092/#2246: an unclassified type has no delivery channel at all
		// (credpolicy fallback Delivery: DeliveryNone) — withheld regardless
		// of Keeper state, not just when Keeper is on. Checked before the
		// Keeper gate below so it applies in both states.
		if !credEnvDeliverable(cred) {
			if logger != nil {
				logger.Warn("credential referenced by MCP config withheld: type has no credpolicy delivery row, so it cannot be delivered by any path",
					"env_var", cred.EnvVarName, "cred_type", cred.Type,
					"hint", "add a credpolicy.TypePolicy row for this type in internal/credpolicy")
			}
			continue
		}
		// #1362/#1364: fail-closed withholding under Keeper. A Keeper-gated
		// credential referenced by an MCP config must not be written into the
		// agent env when Keeper is on — that would leak it via
		// /proc/self/environ and bypass the /keeper/request audit gate. Gated
		// set is table-driven (credpolicy), not a SECRET-only special case.
		if keeperEnabled && credpolicy.IsKeeperGated(cred.Type) {
			if logger != nil {
				logger.Warn("SECRET referenced by MCP config withheld under Keeper; the MCP server will not receive it — route it via the Keeper API or use a non-SECRET credential type",
					"env_var", cred.EnvVarName)
			}
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

// SidecarProxyEnv is the canonical proxy environment for a process running
// inside a crew container: all outbound HTTP goes through the sidecar on
// 127.0.0.1:9119, which is where the crew egress allowlist is enforced.
//
// This exists as one exported source because the allowlist is only a boundary
// for processes that actually carry these variables, and there is more than one
// way into a crew container. #1473: routine `script` steps built their exec
// environment from the step's own inputs alone, so they ran with no proxy at
// all — a `restricted` crew allowlisted to one host reached the open internet
// from a script step with a plain curl. Not a bypass of the fence; they never
// met it. Any new code path that execs into a crew container must append this.
//
// Both cases are set deliberately: Go reads the upper-case pair, most CLIs
// (curl, wget) read the lower-case one, and a script step is exactly where a
// bare curl runs.
//
// NO_PROXY keeps loopback direct. Without it a request to 127.0.0.1 — a health
// check, or the sidecar's own API — would be proxied through the sidecar
// itself and recurse.
func SidecarProxyEnv() []string {
	return []string{
		"HTTP_PROXY=http://127.0.0.1:9119",
		"HTTPS_PROXY=http://127.0.0.1:9119",
		"http_proxy=http://127.0.0.1:9119",
		"https_proxy=http://127.0.0.1:9119",
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
	}
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
		if !isOAuth || cred.PlainValue == "" {
			continue
		}
		// #2092/#2246: the second disjunct above matches on the VALUE's
		// shape, not the credential's type — an unclassified credential
		// whose value happens to look like an Anthropic OAuth token must
		// still be refused. This check does NOT depend on keeperEnabled: an
		// unclassified type's fallback row (Delivery: DeliveryNone) has no
		// delivery channel in EITHER Keeper state, and this loop previously
		// ran unconditionally regardless of Keeper, so a shape collision
		// leaked even with Keeper on.
		if !credEnvDeliverable(cred) {
			slog.Default().Warn("credential withheld from agent env: OAuth-shaped value but type has no credpolicy delivery row",
				"agent_slug", req.AgentSlug, "cred_type", cred.Type,
				"hint", "add a credpolicy.TypePolicy row for this type in internal/credpolicy")
			continue
		}
		hasOAuth = true
		oauthToken = cred.PlainValue
		break
	}

	env := append(baseAgentEnv(req), SidecarProxyEnv()...)

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

	// #1030: Codex routes its OpenAI traffic through the sidecar reverse-proxy
	// by pointing OPENAI_BASE_URL at the /openai prefix on the sidecar port.
	// The dummy OPENAI_API_KEY set above stays (Codex needs a syntactically
	// valid key to send); the sidecar swaps it for the real value from the
	// CredStore mid-flight, so the real key lives only in the sidecar heap.
	// Scoped to Codex — OpenCode's multi-provider BYOK driver dials providers
	// directly and must NOT be force-routed here.
	if req.CLIAdapter == "CODEX_CLI" {
		env = append(env, "OPENAI_BASE_URL=http://127.0.0.1:9119/openai/v1")
	}

	// #1030: the Gemini CLI routes its Google traffic through the sidecar
	// reverse-proxy by pointing GOOGLE_GEMINI_BASE_URL at the /gemini prefix
	// on the sidecar port (the @google/genai SDK appends /v1beta/... to it —
	// the same path-suffixed base-URL shape gateway deployments use). The
	// dummy GOOGLE_API_KEY set above stays, and GEMINI_API_KEY (the CLI's
	// canonical AI Studio var) gets the same dummy so auth-type selection
	// still sees a syntactically-valid key; the sidecar swaps it for the real
	// value from the CredStore mid-flight, so the real key lives only in the
	// sidecar heap. Scoped to GEMINI_CLI — OpenCode's multi-provider BYOK
	// driver dials providers directly and must NOT be force-routed here.
	if req.CLIAdapter == "GEMINI_CLI" {
		env = append(env,
			"GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:9119/gemini",
			"GEMINI_API_KEY=dummy-crewship-sidecar",
		)
	}

	// Multi-CLI BYO API key path. The sidecar reverse-proxy now injects keys
	// for api.anthropic.com (Claude Code), api.openai.com (Codex, #1030) and
	// generativelanguage.googleapis.com (Gemini, #1030); OpenCode/Cursor/
	// Factory still talk to their upstream over HTTPS CONNECT through the
	// sidecar (no key injection). Override the dummy provider keys above with
	// real values from req.Credentials — but only for env vars that THIS
	// adapter's CLI actually reads. This preserves the sidecar isolation
	// guarantee for cross-adapter scenarios (e.g. a Claude Code agent in a
	// workspace that also has an OpenAI key configured — that key stays out
	// of env).
	//
	// Residual (#1030): Cursor cannot join the reverse-proxy model — the
	// cursor-agent configuration surface has no endpoint / base-URL override
	// (cursor.com/docs/cli/reference/configuration), so there is no way to
	// point it at the sidecar; its key stays on the env path until upstream
	// ships an override. OpenCode is BYOK-by-design across 75+ providers and
	// Factory has no override either.
	//
	// The one exception is a provider this run is actually PROXY-ROUTED to:
	// OpenCode reads its endpoint from the generated OPENCODE_CONFIG_CONTENT
	// block, so when that block points at the sidecar the CLI never needs the
	// real key and it is left out of /proc/<pid>/environ. The set is deliberately
	// per-run rather than per-adapter — an OpenCode crew whose model is NOT
	// routed still needs its key here, so apiKeyEnvVarsForAdapter keeps every
	// entry it has today and the filter below is what narrows it.
	allowed := apiKeyEnvVarsForAdapter(req.CLIAdapter)
	if len(allowed) > 0 {
		routed, isRouted := resolveRoutedProvider(req, true)
		for _, cred := range req.Credentials {
			if cred.PlainValue == "" {
				continue
			}
			if _, ok := allowed[cred.EnvVarName]; !ok {
				continue
			}
			if isRouted && credentialRoutesTo(cred, routed.Spec) {
				// The sidecar CredStore holds this one; the config block sends
				// the CLI to 127.0.0.1 and the proxy injects it mid-flight.
				continue
			}
			// #2092/#2246: this selector matches by EnvVarName only, never by
			// type — an unclassified credential simply named e.g.
			// OPENAI_API_KEY would otherwise reach the env regardless of its
			// credpolicy row.
			if !credEnvDeliverable(cred) {
				slog.Default().Warn("credential withheld from agent env: env var recognized by adapter but type has no credpolicy delivery row",
					"agent_slug", req.AgentSlug, "env_var", cred.EnvVarName, "cred_type", cred.Type, "adapter", req.CLIAdapter,
					"hint", "add a credpolicy.TypePolicy row for this type in internal/credpolicy")
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

	// Keeper-gated credentials (SECRET today): when Keeper is enabled, agents
	// must request them via the Keeper API (/keeper/request), enforcing access
	// control + audit trail — so they are not injected here. When Keeper is
	// disabled, inject them directly as env vars (legacy mode). Table-driven
	// (credpolicy) rather than a SECRET-only special case.
	//
	// #2092: both halves of the row are consulted, not just KeeperGated. A
	// type with no explicit credpolicy row gets the fail-safe fallback
	// {Delivery: DeliveryNone, KeeperGated: true} — KeeperGated alone would
	// route it down this "legacy fallback" path exactly like SECRET, which
	// contradicts DeliveryNone's promise that an unclassified type is not
	// delivered to the agent AT ALL, by any channel. Only a type that
	// actually has a delivery channel takes the legacy env path here.
	//
	// An unclassified type withheld this way has no /keeper/request path
	// either (Keeper is off), so it becomes undeliverable rather than merely
	// gated — a behaviour change for any legacy row relying on the old
	// (wrong) fallback-as-SECRET treatment. WARN so an operator can find out
	// why the credential vanished and add the missing credpolicy row.
	if !keeperEnabled {
		for _, cred := range req.Credentials {
			if cred.EnvVarName == "" || cred.PlainValue == "" {
				continue
			}
			if !credpolicy.For(cred.Type).KeeperGated {
				continue
			}
			if !credEnvDeliverable(cred) {
				slog.Default().Warn("credential withheld from agent env: unclassified type has no credpolicy delivery row, so it cannot be delivered by any path with Keeper off",
					"agent_slug", req.AgentSlug, "env_var", cred.EnvVarName, "cred_type", cred.Type,
					"hint", "add a credpolicy.TypePolicy row for this type in internal/credpolicy")
				continue
			}
			env = append(env, cred.EnvVarName+"="+cred.PlainValue)
		}
	}

	// Multi-part credentials (PRD-CREDENTIALS-V2 §2.2), delivered LAST so every
	// runtime name above — the proxy fence, the dummy provider keys, the OAuth
	// token, the identity block — is already claimed and a part cannot take one.
	//
	// A SECRET part is delivered if and only if its credential's own plaintext
	// reached the env, which is asked of the finished block rather than
	// re-derived from the OAuth / proxy / CLI-token / Keeper rules above. That
	// keeps the two in step by construction: a change to which credentials land
	// in env moves their parts with them, with nothing to remember.
	//
	// Identifier parts (region, account id, host) always come. They are not
	// credential material — cleartext at rest by design — and no channel here
	// can carry them: the reverse proxy injects a key into a request, it cannot
	// tell the agent which region its account lives in.
	for i := range req.Credentials {
		cred := req.Credentials[i]
		valueReachedEnv := cred.PlainValue != "" &&
			envHasAssignment(env, resolveEnvVar(&cred), cred.PlainValue)
		env = appendCredentialFields(env, cred, valueReachedEnv)
	}

	if e, ok := localModelConfigEnv(req, true); ok {
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

// localModelDisplayName is the generated provider block's human label. Unchanged
// on the routed path: it is the same operator endpoint either way, only reached
// through the sidecar instead of directly.
const localModelDisplayName = "Ollama (local)"

// openAICompatProviderID is llmroute.Spec.ID for the generic OpenAI-compatible
// endpoint — the descriptor the operator's own ENDPOINT_URL credential is
// delivered under. A constant rather than an import of internal/sidecar: this
// package must not depend on the sidecar to name a provider.
const openAICompatProviderID = "OPENAI_COMPAT"

// sidecarProxyOrigin is the loopback origin a proxy-routed provider block points
// at — the sidecar's own port, the one NO_PROXY in SidecarProxyEnv keeps direct.
const sidecarProxyOrigin = "http://127.0.0.1:9119"

// routedProviderDummyKey is what a routed provider block puts in options.apiKey.
// The @ai-sdk/openai-compatible driver refuses to send a request without one, so
// the slot cannot simply be left empty; the sidecar overwrites the header it
// produces with the real credential from the CredStore. Same shape and same
// reason as the dummy ANTHROPIC_API_KEY / GEMINI_API_KEY above.
const routedProviderDummyKey = "dummy-crewship-sidecar"

// bindLLMRouteToken embeds the already-agent-visible, HMAC-authenticated
// CREWSHIP_AGENT_TOKEN into each provider's dummy API key. The sidecar replaces
// the dummy before forwarding, but first uses the embedded token to attribute
// cost and to reject a stale concurrent run whose shared sidecar has since
// restarted for another agent's credential set.
//
// With an empty token callers leave the legacy byte shape untouched. This is
// important for deployments that explicitly run without internal auth and for
// the byte-identity fixtures which pin pre-existing provider behaviour.
func bindLLMRouteToken(env []string, token, configFingerprint string) []string {
	if token == "" || configFingerprint == "" {
		return env
	}
	routeIdentity := token + internaltoken.RouteFingerprintDelimiter + configFingerprint
	replacements := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-dummy-crewship-sidecar",
		"OPENAI_API_KEY":    "sk-dummy-crewship-sidecar",
		"GOOGLE_API_KEY":    "dummy-crewship-sidecar",
		"GEMINI_API_KEY":    "dummy-crewship-sidecar",
	}
	for i, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if dummy, found := replacements[name]; found && value == dummy {
			env[i] = name + "=" + dummy + "." + routeIdentity
			continue
		}
		if name == "OPENCODE_CONFIG_CONTENT" {
			old := `"apiKey":"` + routedProviderDummyKey + `"`
			newValue := `"apiKey":"` + routedProviderDummyKey + "." + routeIdentity + `"`
			env[i] = name + "=" + strings.ReplaceAll(value, old, newValue)
		}
	}
	return env
}

// localEndpointModel reports whether this run targets the operator's resolved
// OpenAI-compatible endpoint, returning the model id with the ollama/ prefix
// stripped. It is the #944 activation condition on its own — separate from
// whether the traffic is proxy-routed, which localEndpointModel deliberately
// says nothing about.
func localEndpointModel(req AgentRunRequest) (string, bool) {
	if req.CLIAdapter != "OPENCODE" || req.LocalModelBaseURL == "" {
		return "", false
	}
	modelID := strings.TrimPrefix(req.LLMModel, localModelPrefix)
	if modelID == req.LLMModel || modelID == "" {
		return "", false // not an ollama/… model
	}
	return modelID, true
}

// routedProvider is the OpenCode provider block for a run whose LLM traffic goes
// through the sidecar reverse proxy instead of straight out of the container.
type routedProvider struct {
	Spec llmroute.Spec
	// ProviderID is the OpenCode config key, which is also the prefix of the
	// model string OpenCode routes on. Always from a closed set — either the
	// ollama/ literal or a descriptor ID lowercased — never free text.
	ProviderID string
	// ModelID is the model as OpenCode must see it under that key: the run's
	// LLMModel with the provider prefix removed.
	ModelID string
	Label   string
	// UpstreamHost is the host the SIDECAR will dial for this run, and it is
	// only non-empty for a spec whose upstream comes from the credential.
	//
	// It exists because two places used to answer "where does this run talk to"
	// independently and could disagree: the router picked the provider, while
	// proxiedEndpointDomains built the egress allowlist from
	// req.LocalModelBaseURL. When an assigned OPENAI_COMPAT credential won over
	// the synthetic endpoint, the sidecar dialled the credential's host and the
	// allowlist named the other one — a 403 on every call, for a crew whose
	// credentials were all valid. Carrying it on the routing decision means the
	// allowlist is derived from the same answer the proxy acts on.
	UpstreamHost string
}

// proxyRoutable reports whether a descriptor owns one of the reserved /llm/…
// sidecar routes. The three grandfathered providers (/v1, /openai, /gemini) are
// mounted for the CLIs that were built around them and are not on this path:
// OpenCode reaches Anthropic/OpenAI/Google over an HTTPS CONNECT tunnel, which
// the proxy cannot inject into.
func proxyRoutable(s llmroute.Spec) bool {
	return strings.HasPrefix(s.PathPrefix, "/llm/")
}

// credentialRoutesTo reports whether a delivered credential is the one the
// sidecar's CredStore will hold for spec s.
//
// It mirrors credTypeToProvider (exec_sidecar.go), which consults a credential's
// agent-facing env-var name BEFORE its provider column. So the provider column
// has to name the spec, and the env-var name must not be claimed by a different
// provider — a credential carrying somebody else's variable lands in the
// CredStore under THAT provider and would never answer for this one. Being
// conservative here is the safe direction: a "no" leaves the run on the path it
// takes today, while a wrong "yes" withholds the key from the env AND finds no
// credential at the proxy, which is a 503 where there used to be a working run.
func credentialRoutesTo(cred Credential, s llmroute.Spec) bool {
	byColumn, ok := llmroute.LookupProvider(cred.Provider)
	if !ok || byColumn.ID != s.ID {
		return false
	}
	// "Carries auth material", not "carries a token". A bring-your-own endpoint
	// can authenticate entirely through a custom header, and such a credential
	// has an EMPTY PlainValue by construction — the whole secret is in Headers.
	// Requiring a token here was the last of the gates that kept those endpoints
	// unrouted, and therefore kept their secret in the agent's own config where
	// the agent can read it.
	//
	// Headers count ONLY for a spec whose upstream comes from the credential.
	// Every other provider authenticates through a slot this package fills from
	// the token, so a token-less credential there would route to a real vendor
	// host and arrive unauthenticated. Nothing populates Headers for those specs
	// today — the field is written by the endpoint split alone — but the check
	// costs one condition and removes the question.
	if cred.PlainValue == "" && !(s.UpstreamFromCredential && len(cred.Headers) > 0) {
		return false
	}
	if cred.EnvVarName == "" {
		return true
	}
	// A spec that declares NO KeyEnvVars is reached through the provider column
	// alone — that is what OPENAI_COMPAT's registry comment says, because a
	// bring-your-own endpoint has no conventional variable name an agent CLI
	// reads it from. Falling through to the loop below made that promise
	// unkeepable: a credential row always carries an env_var_name, the loop over
	// an empty slice never matches, and every such credential was refused. The
	// provider column had already identified it by then.
	if len(s.KeyEnvVars) == 0 {
		return true
	}
	for _, name := range s.KeyEnvVars {
		if name == cred.EnvVarName {
			return true
		}
	}
	return false
}

// resolveRoutedProvider picks the descriptor this run's model traffic is proxied
// through, or ok=false when it is not proxied at all. viaSidecar is false on the
// no-sidecar exec path, where there is no proxy to route to.
//
// Routing is opt-in per run and never speculative: each branch below refuses
// unless the credential the sidecar would need is demonstrably on its way there.
// An unrouted run is byte-identical to its pre-phase-2 self.
func resolveRoutedProvider(req AgentRunRequest, viaSidecar bool) (routedProvider, bool) {
	// OpenCode and Codex both expose a per-provider base-URL surface. Gemini is
	// routed only by its global GOOGLE_GEMINI_BASE_URL above; Cursor and Factory
	// have no endpoint override at all (#1030 residual).
	if !viaSidecar || (req.CLIAdapter != "OPENCODE" && req.CLIAdapter != "CODEX_CLI") {
		return routedProvider{}, false
	}

	// The operator's own OpenAI-compatible endpoint (#961). Routed only when it
	// carries auth material: a bare Ollama box has no secret to isolate, and
	// routing it anyway would put a RequireCredential 503 in front of a path
	// that works today. When there IS auth material the API tier delivers the
	// same resolved endpoint to the CredStore as an OPENAI_COMPAT credential
	// (internal/api/agent_config.go), which is what fills the slot the dummy
	// key below occupies.
	if modelID, ok := localEndpointModel(req); ok {
		// Auth material of EITHER kind. This used to demand a bearer token
		// specifically, because llmroute.ApplyAuth dropped custom headers on an
		// empty token and routing a headers-only endpoint would have sent the
		// request unauthenticated. ApplyAuth writes them independently now, so
		// the endpoint whose only secret lives in a header is isolated in the
		// sidecar rather than left in the agent's own config for the agent to
		// read. A bare endpoint with no auth at all still stays unrouted: there
		// is nothing to isolate, and OPENAI_COMPAT is RequireCredential.
		if req.LocalModelAPIKey == "" && len(req.LocalModelHeaders) == 0 {
			return routedProvider{}, false
		}
		s, ok := llmroute.Lookup(openAICompatProviderID)
		if !ok {
			return routedProvider{}, false
		}
		// Auth material alone is NOT enough to route. The API tier withholds
		// the CredStore delivery in cases where the endpoint key still travels
		// on the env path — a privileged crew without
		// allow_privileged_credentials is the live one (#1032 fails closed on
		// loading anything into a CredStore whose memory the agent can read).
		// Routing on the key's presence alone would point OpenCode at a
		// RequireCredential provider the proxy has nothing for, turning a
		// working run into a 503. Ask for the credential itself.
		cred, ok := credentialFor(req, s)
		if !ok {
			return routedProvider{}, false
		}
		rp := routedProvider{
			Spec:       s,
			ProviderID: strings.TrimSuffix(localModelPrefix, "/"),
			ModelID:    modelID,
			Label:      localModelDisplayName,
		}
		// The upstream is the CREDENTIAL's, not req.LocalModelBaseURL's, and on
		// this branch the two can disagree: when a crew has both a resolved
		// local endpoint and an assigned OPENAI_COMPAT credential,
		// appendProxiedEndpointCredential logs the collision and delivers the
		// ASSIGNED one. Leaving UpstreamHost empty here sent
		// proxiedEndpointDomains back to the synthetic URL, so a restricted
		// crew allowlisted one host while the sidecar dialled another — a 403
		// on every model call with every credential valid. Same failure the
		// field was added to prevent on the model-prefix branch below; it just
		// had two callers and one of them was never wired.
		if strings.TrimSpace(cred.BaseURL) != "" {
			rp.UpstreamHost = cred.BaseURL
		}
		return rp, true
	}

	// A provider with a reserved /llm/… route of its own, selected the way
	// OpenCode selects providers: by the model string's prefix.
	prefix, model, found := strings.Cut(req.LLMModel, "/")
	if !found {
		// API clients may send provider and model as separate fields. OpenCode's
		// command builder qualifies that pair later, but routing is decided before
		// command construction; consume the same pair here so a pasted credential
		// does not depend on the UI having duplicated the provider into LLMModel.
		prefix, model = strings.TrimSpace(req.LLMProvider), strings.TrimSpace(req.LLMModel)
	}
	if prefix == "" || model == "" {
		return routedProvider{}, false
	}
	s, ok := llmroute.Lookup(strings.ToUpper(prefix))
	if !ok || !proxyRoutable(s) {
		return routedProvider{}, false
	}
	cred, ok := credentialFor(req, s)
	if !ok {
		return routedProvider{}, false
	}
	// A spec that takes its upstream from the credential is routable here too,
	// but ONLY once that credential actually supplies one. This branch used to
	// refuse every UpstreamFromCredential spec outright, which made the whole
	// operator-facing OPENAI_COMPAT surface inert: the credential stored,
	// validated and delivered fine, nothing ever dialled /llm/openai-compat,
	// and the documentation described a call that could not happen. Refusing
	// when BaseURL is empty is still right — the proxy would have nowhere to
	// send the request and would 503 a run that has no other way to work.
	if s.UpstreamFromCredential && strings.TrimSpace(cred.BaseURL) == "" {
		return routedProvider{}, false
	}
	rp := routedProvider{Spec: s, ProviderID: prefix, ModelID: model, Label: s.DisplayName}
	if s.UpstreamFromCredential {
		rp.UpstreamHost = cred.BaseURL
	}
	return rp, true
}

// credentialFor returns the delivered credential the sidecar's CredStore will
// hold for spec s — i.e. whether the proxy can actually answer for it, and with
// what. It is the guard that keeps routing from ever being speculative.
//
// Both branches of resolveRoutedProvider need the credential itself and not
// merely its existence: a spec that takes its upstream from the credential can
// only be routed to the host THAT credential names, and the boolean-only
// wrapper this replaced is exactly how one branch came to route without ever
// learning where it was routing to.
//
// It mirrors the sidecar's CredStore.Select as far as a host-side caller can:
// Select picks the LOWEST Priority and round-robins within that tier, so
// returning the first slice match could name a credential the proxy will never
// choose. Ties keep slice order, which is Select's own pass-2 order.
//
// Select's third dimension — the acting agent (#2052) — needs no mirror here,
// and that is a property of the input rather than an omission. req.Credentials
// IS one agent's delivery, and the API tier guarantees the delivering agent is
// among a credential's grantees (credential_grantees.go, grantedTo), so every
// entry in this slice is one the sidecar would serve to THIS run. What changed
// is on the other side: the store may now refuse a peer's credential it would
// once have handed over, which can only make the set the proxy chooses from
// smaller than the set this function sees.
func credentialFor(req AgentRunRequest, s llmroute.Spec) (Credential, bool) {
	now := time.Now()
	var best Credential
	found := false
	for _, cred := range req.Credentials {
		if !credentialRoutesTo(cred, s) || credentialLeaseLapsed(cred, now) {
			continue
		}
		if !found || cred.Priority < best.Priority {
			best, found = cred, true
		}
	}
	return best, found
}

// credentialLeaseLapsed mirrors the sidecar CredStore's own lease gate
// (internal/sidecar/credstore.go). The API tier applies the lease filter at
// delivery-query time, so a credential whose grant expires between that query
// and the exec is still sitting in req.Credentials — the sidecar will refuse it
// and this side must not count on it. Selecting a lapsed credential here routes
// the run to a provider the proxy then has nothing for (503), and for a spec
// whose upstream comes from the credential it also names an UpstreamHost the
// sidecar will never dial.
//
// An unparseable deadline reads as LAPSED, not as standing: the server always
// writes a fixed-width RFC3339 UTC value, so anything else is corruption, and
// the safe reading of "I cannot tell when this expires" for a security control
// is "it already did". Same convention as leaseEpochSentinel on the other side.
func credentialLeaseLapsed(cred Credential, now time.Time) bool {
	if cred.LeaseExpiresAt == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, cred.LeaseExpiresAt)
	if err != nil {
		return true
	}
	return !now.Before(deadline)
}

// credentialsFor returns EVERY credential this run carries that routes to s.
//
// One answer is not enough for the egress allowlist. CredStore.Select
// round-robins within the top priority tier, so a crew holding two
// OPENAI_COMPAT credentials with different base URLs has the sidecar dialling a
// different host on alternating calls. Allowlisting only the one credentialFor
// happened to return would 403 every other request, with both credentials valid
// — the same failure as naming the wrong host, arriving intermittently, which is
// harder to diagnose than never working at all.
//
// Still EVERY one after #2052 scoped Select by acting agent. The rotation this
// exists for happens within one agent's own eligible set, and this slice is one
// agent's delivery, so the hosts it names remain exactly the ones the sidecar
// can dial for this run. Scoping can only remove a host from the sidecar's
// reach, never add one, so the allowlist stays a superset — the safe direction:
// a host on the list that is never dialled costs nothing, a host dialled that is
// not on the list is a 403 on a valid credential.
func credentialsFor(req AgentRunRequest, s llmroute.Spec) []Credential {
	now := time.Now()
	var out []Credential
	for _, cred := range req.Credentials {
		if credentialRoutesTo(cred, s) && !credentialLeaseLapsed(cred, now) {
			out = append(out, cred)
		}
	}
	return out
}

// localModelConfigEnv builds the OPENCODE_CONFIG_CONTENT entry (#944): an
// OPENCODE agent selecting an "ollama/…" model on a server with a resolved
// endpoint, or a model whose prefix names a descriptor with a reserved sidecar
// route, gets a generated provider block pointing OpenCode's openai-compatible
// driver at it. The JSON is always marshalled from a fixed struct — no
// user-controlled JSON reaches the env, so a hostile model name can't smuggle
// extra config keys.
//
// viaSidecar decides whether the block may point at the proxy. On the routed
// branch the endpoint becomes 127.0.0.1:9119 and NO credential material goes in:
// options.apiKey holds a dummy and options.headers is omitted entirely, so the
// real values live only in the sidecar heap. On the unrouted branch the block is
// exactly what it has always been, auth material included, because there is
// nothing between the driver and the endpoint to inject on its behalf.
func localModelConfigEnv(req AgentRunRequest, viaSidecar bool) (string, bool) {
	routed, isRouted := resolveRoutedProvider(req, viaSidecar)
	modelID, localActive := localEndpointModel(req)
	if !isRouted && !localActive {
		return "", false
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
	p := providerCfg{NPM: "@ai-sdk/openai-compatible"}
	providerID := strings.TrimSuffix(localModelPrefix, "/")

	if isRouted {
		providerID = routed.ProviderID
		modelID = routed.ModelID
		p.Name = routed.Label
		// #974 S2 closed: the driver dials the sidecar, which owns the real
		// credential. Headers are omitted rather than forwarded — a custom
		// header on an authenticated endpoint is credential material too, and
		// the proxy re-adds it from the CredStore entry.
		p.Options.BaseURL = sidecarProxyOrigin + routed.Spec.PathPrefix
		p.Options.APIKey = routedProviderDummyKey
	} else {
		p.Name = localModelDisplayName
		p.Options.BaseURL = req.LocalModelBaseURL
		// #961: optional auth for an authenticated endpoint. apiKey → the
		// @ai-sdk/openai-compatible driver auto-adds `Authorization: Bearer`;
		// headers is the escape hatch for Basic/custom-header/non-bearer
		// schemes. OPENCODE_CONFIG_CONTENT is itself an agent env var, so on
		// this branch they DO land in the agent environment — reported by
		// AgentEnvCredentialExposures and redacted from logs by the scrubber's
		// (case-insensitive) apiKey pattern.
		p.Options.APIKey = req.LocalModelAPIKey
		if len(req.LocalModelHeaders) > 0 {
			p.Options.Headers = req.LocalModelHeaders
		}
	}

	p.Models = map[string]struct {
		Name string `json:"name"`
	}{modelID: {Name: modelID}}
	cfg := map[string]any{"provider": map[string]providerCfg{providerID: p}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		// Statically-shaped struct — marshal cannot realistically fail; treat
		// as "path disabled" rather than plumbing an error into env building.
		return "", false
	}
	return "OPENCODE_CONFIG_CONTENT=" + string(raw), true
}

// allowPrivateEndpointsEnvVar is the instance-level ceiling for private-network
// model endpoints (#974 S5). Default off: a per-crew allow_private_endpoints
// opt-in only takes effect when the operator has also enabled it host-wide, so
// a workspace admin cannot self-grant RFC1918/loopback egress on a shared or
// cloud host. Mirrors CREWSHIP_HOOKS_ALLOW_PRIVATE (internal/hooks/http.go).
const allowPrivateEndpointsEnvVar = "CREWSHIP_ALLOW_PRIVATE_ENDPOINTS"

// instanceAllowsPrivateEndpoints reports whether the host operator has enabled
// private-network model endpoints instance-wide. ANDed with the per-crew flag.
func instanceAllowsPrivateEndpoints() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowPrivateEndpointsEnvVar))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// InstanceAllowsPrivateEndpoints is the exported form for callers that need to
// REPORT the ceiling rather than enforce it (the admin security-posture
// endpoint). Exported deliberately instead of re-reading the env var at the
// reporting site: a posture that parses the flag its own way can drift from the
// one that gates traffic, and then it reassures an operator about a state the
// runtime doesn't actually have.
func InstanceAllowsPrivateEndpoints() bool { return instanceAllowsPrivateEndpoints() }

// effectiveAllowPrivateEndpoints ANDs the per-crew opt-in with the instance-
// level ceiling (#974 S5). Private-network egress requires BOTH.
func effectiveAllowPrivateEndpoints(crewFlag bool) bool {
	return crewFlag && instanceAllowsPrivateEndpoints()
}

// proxiedEndpointDomains returns the operator endpoint's host when this run
// uses it, so restricted network mode auto-allowlists the traffic the operator
// explicitly enabled (same pattern as mcpStdioDomains). Empty in every other
// case — the exception never widens egress for crews that don't use one.
//
// The host is needed whether or not the run is proxy-routed, but for different
// reasons, and the gate is deliberately the endpoint's activation condition
// rather than the routing decision. Unrouted, the agent dials the endpoint
// itself and meets the crew allowlist on the way out. Routed, the agent only
// ever dials 127.0.0.1 (which NO_PROXY keeps direct) and it is the SIDECAR that
// dials the endpoint — and the sidecar checks the same crew allowlist before it
// does, so a routed run that dropped the host here would egress-block itself.
//
// Providers with a fixed upstream (OpenRouter et al.) contribute nothing: the
// sidecar's allowlist check is scoped to credential-supplied upstreams, and
// their hosts are already in egressallow's defaults.
func proxiedEndpointDomains(req AgentRunRequest) []string {
	// Ask the router where this run actually goes, rather than assuming
	// req.LocalModelBaseURL. When an assigned OPENAI_COMPAT credential wins
	// over the synthetic endpoint, the sidecar dials the CREDENTIAL's host —
	// and an allowlist built from the other URL produced a 403 on every call
	// for a crew whose credentials were all valid. One question, one answer.
	// EVERY base URL the sidecar might dial, not one. CredStore.Select
	// round-robins within the top priority tier, so a crew holding two
	// endpoint credentials alternates hosts between calls; allowlisting one of
	// them 403s every other request with both credentials valid.
	var bases []string
	if rp, routed := resolveRoutedProvider(req, true); routed && rp.UpstreamHost != "" {
		for _, cred := range credentialsFor(req, rp.Spec) {
			if b := strings.TrimSpace(cred.BaseURL); b != "" {
				bases = append(bases, b)
			}
		}
		if len(bases) == 0 {
			bases = []string{rp.UpstreamHost}
		}
	} else if _, ok := localEndpointModel(req); ok {
		bases = []string{req.LocalModelBaseURL}
	} else {
		return nil
	}

	var hosts []string
	seen := map[string]bool{}
	for _, base := range bases {
		u, err := url.Parse(base)
		if err != nil || u.Hostname() == "" {
			continue
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
		//
		// Per host, not per call: one blocked address must not drop a sibling
		// that is perfectly reachable, or a single bad credential takes the
		// whole crew offline. It is simply not allowlisted, and the
		// deny-by-default sidecar emits the loud network.egress entry for it.
		if ip := net.ParseIP(host); ip != nil {
			if httpsafe.IsBlockedIPForEndpoint(ip, req.AllowPrivateEndpoints) {
				continue
			}
		}
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	return hosts
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

	// Credentials whose plaintext lands in the env. BuildEnvVarsSidecar
	// delivers a credential's SECRET parts on exactly that condition, so the
	// same set drives the part exposures appended at the end. nil until a
	// branch below actually fires — the no-exposure case must still allocate
	// nothing.
	var exposedCreds map[string]bool
	markExposed := func(id string) {
		if id == "" {
			return
		}
		if exposedCreds == nil {
			exposedCreds = map[string]bool{}
		}
		exposedCreds[id] = true
	}

	// OAuth: BuildEnvVarsSidecar injects only the FIRST matching token as
	// CLAUDE_CODE_OAUTH_TOKEN and stops; mirror that so we don't over-report.
	// #2092/#2246: also mirror the credEnvDeliverable gate — an unclassified
	// type whose value merely looks like an OAuth token is no longer
	// injected, so it must not be reported here either.
	for _, cred := range req.Credentials {
		isOAuth := cred.Type == "AI_CLI_TOKEN" || strings.HasPrefix(cred.PlainValue, "sk-ant-oat")
		if !isOAuth || cred.PlainValue == "" || !credEnvDeliverable(cred) {
			continue
		}
		out = append(out, CredentialEnvExposure{
			EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN",
			Type:       "AI_CLI_TOKEN",
			Reason:     "OAuth token authenticates inside an HTTPS CONNECT tunnel the sidecar cannot inject into, so it must live in the agent env",
		})
		markExposed(cred.ID)
		break
	}

	// BYO API keys: CONNECT-tunneled adapters reach their upstream over an HTTPS
	// CONNECT tunnel and get the real key written into the env, because the sidecar
	// reverse-proxy only injects for providers it has a route for (the proxy-injected
	// adapter returns an empty set and stays isolated). Mirror BuildEnvVarsSidecar's
	// allowed-override loop exactly — including its routed-provider skip — so one
	// exposure is reported per credential that actually lands in the env.
	if allowed := apiKeyEnvVarsForAdapter(req.CLIAdapter); len(allowed) > 0 {
		routed, isRouted := resolveRoutedProvider(req, true)
		for _, cred := range req.Credentials {
			if cred.PlainValue == "" {
				continue
			}
			if _, ok := allowed[cred.EnvVarName]; !ok {
				continue
			}
			if isRouted && credentialRoutesTo(cred, routed.Spec) {
				continue // isolated: the sidecar holds it, the env does not
			}
			// #2092/#2246: mirror the credEnvDeliverable gate — an
			// unclassified credential named after an adapter-recognized env
			// var is no longer injected, so it must not be reported here.
			if !credEnvDeliverable(cred) {
				continue
			}
			out = append(out, CredentialEnvExposure{
				EnvVarName: cred.EnvVarName,
				Type:       "API_KEY",
				Reason:     "adapter " + req.CLIAdapter + " reaches this provider over an HTTPS CONNECT tunnel, so the real API key is written to env; only providers the sidecar has a reverse-proxy route for can be isolated",
			})
			markExposed(cred.ID)
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
			markExposed(cred.ID)
		}
	}

	// Local-model endpoint auth (#961/#974 S2): the apiKey/headers are embedded
	// in OPENCODE_CONFIG_CONTENT (an agent env var) whenever the generated block
	// still points straight at the endpoint. Not actionable via config (it is
	// the endpoint's own auth).
	//
	// Gate on the endpoint's active condition (OPENCODE adapter + base URL +
	// ollama model), not just the presence of auth material: the auth is
	// resolved for every agent in a workspace that has an authed ENDPOINT_URL
	// credential, but OPENCODE_CONFIG_CONTENT is only actually placed in the
	// env for the OpenCode/ollama path. Otherwise we'd report a phantom
	// exposure for a Claude/mismatched-adapter run.
	//
	// !isEndpointRouted is the phase-2 half: when the block points at the
	// sidecar instead, the token and the headers are replaced by a dummy and an
	// omission, and there is nothing here to report. This branch stays because
	// the routing is conditional — a run that does not qualify still carries the
	// key, and an operator must be told which of the two they got.
	if _, active := localEndpointModel(req); active && (req.LocalModelAPIKey != "" || len(req.LocalModelHeaders) > 0) {
		if _, isEndpointRouted := resolveRoutedProvider(req, true); !isEndpointRouted {
			out = append(out, CredentialEnvExposure{
				EnvVarName: "OPENCODE_CONFIG_CONTENT",
				Type:       "ENDPOINT_URL",
				Reason:     "the local-model endpoint auth token/headers are embedded in the OpenCode config env var; this run is not proxy-routed, so the openai-compatible driver dials the endpoint directly and the sidecar cannot isolate them",
			})
		}
	}

	// Keeper-gated credentials (SECRET today): isolated behind the Keeper
	// request/execute flow when Keeper is enabled, but injected to env as a
	// legacy fallback when it is off. This is the one exposure an operator can
	// close, so flag it actionable. Table-driven (credpolicy).
	//
	// #2092: mirrors BuildEnvVarsSidecar's selector exactly — KeeperGated AND
	// an actual delivery channel (Delivery != DeliveryNone). An unclassified
	// type (the fail-safe fallback row) is no longer injected there, so it
	// must not be reported as exposed here either, or this posture view
	// over-reports a credential that was actually withheld.
	if !keeperEnabled {
		for _, cred := range req.Credentials {
			if cred.EnvVarName == "" || cred.PlainValue == "" {
				continue
			}
			if credpolicy.For(cred.Type).KeeperGated && credEnvDeliverable(cred) {
				out = append(out, CredentialEnvExposure{
					EnvVarName: cred.EnvVarName,
					Type:       cred.Type,
					Reason:     "Keeper is disabled; enable it (set KEEPER_MODEL / KEEPER_OLLAMA_URL) to isolate this credential behind /keeper/request",
					Actionable: true,
				})
				markExposed(cred.ID)
			}
		}
	}

	// SECRET parts of an exposed credential are exposed with it — same env
	// block, same /proc/self/environ. Reported per part so an operator reading
	// the posture sees the actual variable names present, not just the
	// credential's own.
	//
	// Identifier parts are NOT reported. They are in the env too, but this list
	// is the inverse of the credential-isolation guarantee, and a region or an
	// account id is not credential material — the same reason
	// credentials.username has never appeared here. Padding it with identifiers
	// makes the exposures an operator MUST act on harder to see.
	for _, cred := range req.Credentials {
		if !exposedCreds[cred.ID] {
			continue
		}
		for _, f := range cred.Fields {
			if !f.IsSecret || f.EnvVar == "" || f.Value == "" {
				continue
			}
			out = append(out, CredentialEnvExposure{
				EnvVarName: f.EnvVar,
				Type:       cred.Type,
				Reason:     "a secret part of a credential whose value is already in the agent env; it is delivered on exactly the same terms",
			})
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
		// #1030: Codex's OpenAI key is now isolated by the sidecar reverse-
		// proxy (OPENAI_BASE_URL routes it through /openai → api.openai.com,
		// where the real key is injected from the CredStore). So — exactly
		// like CLAUDE_CODE for Anthropic — Codex no longer needs the real
		// OPENAI_API_KEY written to its env; the dummy stays and the sidecar
		// swaps it mid-flight. Returning nil keeps the real key out of
		// /proc/<pid>/environ. (The CredStore still receives it via
		// credTypeToProvider, independent of this set.)
		return nil
	case "GEMINI_CLI":
		// #1030: Gemini's Google key is now isolated by the sidecar reverse-
		// proxy (GOOGLE_GEMINI_BASE_URL routes it through /gemini →
		// generativelanguage.googleapis.com, where the real key is injected
		// from the CredStore). So — exactly like CLAUDE_CODE/CODEX_CLI —
		// Gemini no longer needs the real GOOGLE_API_KEY/GEMINI_API_KEY
		// written to its env; the dummies stay and the sidecar swaps them
		// mid-flight. Returning nil keeps the real key out of
		// /proc/<pid>/environ. (The CredStore still receives it via
		// credTypeToProvider, independent of this set.)
		return nil
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
		// #1030 residual: cursor-agent has no endpoint / base-URL override in
		// its configuration surface (cursor.com/docs/cli/reference/
		// configuration documents only CURSOR_CONFIG_DIR, proxy vars and CA
		// certs), so the sidecar reverse-proxy cannot be interposed — the CLI
		// reaches api.cursor.sh/api2.cursor.sh over an HTTPS CONNECT tunnel
		// and needs the real key in env. Revisit if Cursor ships an override.
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
