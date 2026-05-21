package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// envRefRE matches ${VAR} (curly form) or $VAR (bare form) anywhere in a
// string. Used by translateEnvRefsToOpenCode so substrings like
// "Bearer ${LINEAR_TOKEN}" get rewritten to "Bearer {env:LINEAR_TOKEN}".
var envRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// mcpSpec is the orchestrator-internal canonical form for an MCP server,
// produced by normaliseMCPInputs from the crew/agent JSON blobs +
// req.MCPServers list. Each per-CLI WriteMCPConfig consumes []mcpSpec and
// re-serialises it into that CLI's expected schema.
//
// We model both stdio (command/args/env) and remote (url/headers) transports
// in one struct because most CLIs key the discriminator by which fields are
// populated rather than an explicit "type" — by carrying both we let each
// adapter pick the convention its CLI expects.
type mcpSpec struct {
	Name      string
	Command   string            // stdio: binary to spawn
	Args      []string          // stdio: command arguments
	Env       map[string]string // stdio: env vars (values may be ${VAR} refs)
	URL       string            // remote: HTTP/SSE endpoint
	Headers   map[string]string // remote: extra HTTP headers
	Transport string            // explicit transport hint when known: "stdio" | "http" | "sse"
}

// normaliseMCPInputs flattens the three MCP sources we accept on
// AgentRunRequest into a sorted []mcpSpec. Order: raw JSON wins over the
// resolved server list (matches the precedence in the legacy setupMCPConfig).
// Empty inputs return nil + nil error so adapters can early-exit.
func normaliseMCPInputs(req AgentRunRequest) ([]mcpSpec, error) {
	merged := req.CrewMCPConfigJSON
	if req.AgentMCPConfigJSON != "" {
		var err error
		merged, err = mergeMCPConfigs(req.CrewMCPConfigJSON, req.AgentMCPConfigJSON)
		if err != nil {
			return nil, fmt.Errorf("merge MCP configs: %w", err)
		}
	}

	specs := make(map[string]mcpSpec)

	if merged != "" {
		var raw struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(merged), &raw); err != nil {
			return nil, fmt.Errorf("unmarshal merged MCP JSON: %w", err)
		}
		for name, blob := range raw.MCPServers {
			s, err := parseMCPServerJSON(name, blob)
			if err != nil {
				return nil, err
			}
			specs[name] = s
		}
	}

	// Resolved list (legacy per-binding model). Only fill in entries the
	// raw JSON did not already cover so explicit raw config wins.
	for _, srv := range req.MCPServers {
		if srv.Name == "" {
			continue
		}
		if _, exists := specs[srv.Name]; exists {
			continue
		}
		specs[srv.Name] = mcpSpec{
			Name:      srv.Name,
			Command:   srv.Command,
			Args:      srv.Args,
			Env:       srv.Env,
			URL:       srv.Endpoint,
			Transport: srv.Transport,
		}
	}

	if len(specs) == 0 {
		return nil, nil
	}

	out := make([]mcpSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parseMCPServerJSON extracts our canonical form from one Claude-style
// .mcp.json server entry. Field-name accommodation for forward compat:
// command/url/httpUrl all coexist in the wild depending on which CLI's
// config flavour someone copy-pasted from. Returns an error so the caller
// can log + skip a malformed entry instead of silently producing an empty
// mcpSpec that downstream writers would render as broken or no-op config.
func parseMCPServerJSON(name string, blob json.RawMessage) (mcpSpec, error) {
	var raw struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		HTTPURL string            `json:"httpUrl"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(blob, &raw); err != nil {
		return mcpSpec{Name: name}, fmt.Errorf("malformed MCP server %q: %w", name, err)
	}

	url := raw.URL
	if url == "" {
		url = raw.HTTPURL
	}
	transport := raw.Type
	if transport == "" {
		if raw.Command != "" {
			transport = "stdio"
		} else if url != "" {
			transport = "http"
		}
	}
	return mcpSpec{
		Name:      name,
		Command:   raw.Command,
		Args:      raw.Args,
		Env:       raw.Env,
		URL:       url,
		Headers:   raw.Headers,
		Transport: transport,
	}, nil
}

// writeMCPClaude writes /crew/agents/<slug>/.mcp.json — the format the
// existing setupMCPConfig already produces. We delegate to that function so
// the existing npx-filtering, OAuth-token-injection, and credential-env
// expansion all keep working without duplication.
func writeMCPClaude(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	_ string,
	logger *slog.Logger,
) error {
	return setupMCPConfig(ctx, container, containerID, req.AgentSlug,
		req.CrewMCPConfigJSON, req.AgentMCPConfigJSON, req.MCPServers, logger)
}

// writeMCPCursor writes <workdir>/.cursor/mcp.json. Cursor reuses Anthropic's
// schema for fields (mcpServers map + command/args/env or url/headers) BUT
// uses ${env:VAR} (NOT ${VAR}) for env-var interpolation. We translate the
// canonical ${VAR} form in env + headers values into Cursor's syntax so user
// configs written in the standard form still resolve at runtime.
//
// Headless MCP works in --print mode when paired with cursor-agent's
// --approve-mcps flag (set by adapter_cursor.go BuildCommand when any MCP
// source is configured) — a non-obvious requirement that several community
// forum threads (#143045, #148397) called out as "MCP doesn't work in CLI".
//
// PR-A F1 NOTE: this writer is currently unreachable (adapter_cursor.go
// SupportsMCP returns false) so we DO NOT inject the crewship-memory
// server here — doing so would mislead operators flipping SupportsMCP=true
// into believing memory works in Cursor headless when it does not. The
// moment headless MCP is fixed upstream, flip SupportsMCP AND add an
// injectMemoryMCP call here in lockstep.
func writeMCPCursor(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	specs, err := normaliseMCPInputs(req)
	if err != nil || len(specs) == 0 {
		return err
	}
	out := struct {
		MCPServers map[string]any `json:"mcpServers"`
	}{MCPServers: map[string]any{}}
	for _, s := range specs {
		entry := anthropicShapeServer(s)
		// Translate ${VAR} → ${env:VAR} in env + headers values so Cursor
		// expands them at runtime instead of seeing the literal token.
		if envMap, ok := entry["env"].(map[string]string); ok {
			entry["env"] = translateEnvRefsToCursor(envMap)
		}
		if hdrMap, ok := entry["headers"].(map[string]string); ok {
			entry["headers"] = translateEnvRefsToCursor(hdrMap)
		}
		out.MCPServers[s.Name] = entry
	}
	body, _ := json.MarshalIndent(out, "", "  ")
	return writeFileViaContainer(ctx, container, containerID, workDir, ".cursor/mcp.json", string(body), logger)
}

// translateEnvRefsToCursor rewrites ${VAR} and $VAR placeholders to Cursor's
// ${env:VAR} syntax — including substrings inside header values like
// "Bearer ${LINEAR_TOKEN}" which is the dominant real-world case.
// translateEnvRefsToOpenCode's existing test coverage is mirrored.
func translateEnvRefsToCursor(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = envRefRE.ReplaceAllStringFunc(v, func(match string) string {
			parts := envRefRE.FindStringSubmatch(match)
			name := parts[1]
			if name == "" {
				name = parts[2]
			}
			return "${env:" + name + "}"
		})
	}
	return out
}

// writeMCPDroid writes <workdir>/.factory/mcp.json. Schema is again Anthropic-
// compatible mcpServers map; Droid additionally requires an explicit "type"
// discriminator (stdio | http).
func writeMCPDroid(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	specs, err := normaliseMCPInputs(req)
	if err != nil {
		return err
	}
	// PR-A F1: auto-inject sidecar-hosted memory MCP server. See codex writer
	// for rationale + invariants.
	specs = injectMemoryMCP(specs)
	if len(specs) == 0 {
		return nil
	}
	out := struct {
		MCPServers map[string]any `json:"mcpServers"`
	}{MCPServers: map[string]any{}}
	for _, s := range specs {
		entry := anthropicShapeServer(s)
		// Droid REQUIRES a "type" field; default to stdio when command is set,
		// http when url is set. Don't infer "sse" here even when transport says
		// so — Droid docs only list stdio + http.
		if s.Command != "" {
			entry["type"] = "stdio"
		} else if s.URL != "" {
			entry["type"] = "http"
		}
		out.MCPServers[s.Name] = entry
	}
	body, _ := json.MarshalIndent(out, "", "  ")
	return writeFileViaContainer(ctx, container, containerID, workDir, ".factory/mcp.json", string(body), logger)
}

// writeMCPGemini writes <workdir>/.gemini/settings.json with mcpServers
// nested under it. Transport discriminator is implicit: command → stdio,
// httpUrl → HTTP streaming, url → SSE (legacy). We always emit httpUrl when
// possible because SSE is being phased out upstream.
func writeMCPGemini(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	specs, err := normaliseMCPInputs(req)
	if err != nil {
		return err
	}
	// PR-A F1: auto-inject sidecar-hosted memory MCP server. See codex writer
	// for rationale.
	specs = injectMemoryMCP(specs)
	if len(specs) == 0 {
		return nil
	}
	servers := map[string]any{}
	for _, s := range specs {
		entry := map[string]any{}
		if s.Command != "" {
			entry["command"] = s.Command
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
		}
		if s.URL != "" {
			// Gemini differentiates: httpUrl = streamable HTTP (preferred),
			// url = SSE (deprecated). When the spec doesn't tell us which the
			// upstream is, prefer httpUrl.
			if strings.EqualFold(s.Transport, "sse") {
				entry["url"] = s.URL
			} else {
				entry["httpUrl"] = s.URL
			}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
		}
		servers[s.Name] = entry
	}
	out := map[string]any{"mcpServers": servers}
	body, _ := json.MarshalIndent(out, "", "  ")
	return writeFileViaContainer(ctx, container, containerID, workDir, ".gemini/settings.json", string(body), logger)
}

// writeMCPOpenCode writes <workdir>/opencode.json. Schema differs the most
// from Claude Code:
//   - Top-level key is "mcp" (NOT "mcpServers")
//   - "type": "local" (stdio) | "remote" (http/sse)
//   - command for local is an ARRAY ["binary","arg1",...] (NOT split into
//     command + args)
//   - env field is "environment" (NOT "env")
//   - Env-var interpolation uses opencode-specific {env:VAR} syntax — we
//     translate ${VAR} and $VAR in spec.Env values to {env:VAR} so users can
//     write the standard form once
func writeMCPOpenCode(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	specs, err := normaliseMCPInputs(req)
	if err != nil {
		return err
	}
	// PR-A F1: auto-inject sidecar-hosted memory MCP server. See codex writer
	// for rationale.
	specs = injectMemoryMCP(specs)
	if len(specs) == 0 {
		return nil
	}
	mcp := map[string]any{}
	for _, s := range specs {
		entry := map[string]any{"enabled": true}
		if s.Command != "" {
			cmdArr := append([]string{s.Command}, s.Args...)
			entry["type"] = "local"
			entry["command"] = cmdArr
			if len(s.Env) > 0 {
				entry["environment"] = translateEnvRefsToOpenCode(s.Env)
			}
		} else if s.URL != "" {
			entry["type"] = "remote"
			entry["url"] = s.URL
			if len(s.Headers) > 0 {
				entry["headers"] = translateEnvRefsToOpenCode(s.Headers)
			}
		} else {
			// Skip entries with neither command nor url — opencode would error.
			continue
		}
		mcp[s.Name] = entry
	}
	out := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp":     mcp,
	}
	body, _ := json.MarshalIndent(out, "", "  ")
	return writeFileViaContainer(ctx, container, containerID, workDir, "opencode.json", string(body), logger)
}

// writeMCPCodex writes Codex's MCP config. Two important realities discovered
// during third-wave validation:
//
//  1. **File location**: Codex's project-scoped config (.codex/config.toml in
//     cwd) is only loaded for "trusted" projects, and trust is established
//     interactively. Headless invocations skip it silently. We write to
//     /crew/agents/<slug>/.codex/config.toml (HOME) so Codex picks it up
//     without needing trust ceremony.
//
//  2. **Env interpolation**: Codex does NOT expand ${VAR} placeholders in
//     env blocks — emitting `env = { LINEAR_TOKEN = "${LINEAR_TOKEN}" }`
//     OVERRIDES the inherited container env with the literal string
//     "${LINEAR_TOKEN}", causing 401 from MCP servers. We omit env entries
//     whose values are pure ${VAR} references so Codex inherits them from
//     the parent process env (where injectMCPCredentialEnvVars puts them).
//     Literal env values (set by user, not references) are still written.
//
// HTTP servers use bearer_token_env_var for ${VAR}-style auth and
// http_headers / env_http_headers for everything else. Generic header
// support added in this revision; the previous behaviour silently dropped
// non-Bearer headers with a warn log.
func writeMCPCodex(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	_ = workDir // unused — Codex MCP config goes to HOME, not workDir
	specs, err := normaliseMCPInputs(req)
	if err != nil {
		return err
	}
	// PR-A F1: auto-inject the sidecar-hosted memory MCP server so the model
	// gets native function calling for memory.read / write / search /
	// append_daily — same wire contract every other MCP-capable adapter
	// exposes. injectMemoryMCP is a no-op if the user already declared a
	// server named "crewship-memory" (override path).
	specs = injectMemoryMCP(specs)
	if len(specs) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Generated by Crewship orchestrator. Do not edit by hand.\n\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "[mcp_servers.%s]\n", tomlSafeKey(s.Name))
		if s.Command != "" {
			fmt.Fprintf(&b, "command = %s\n", tomlString(s.Command))
			if len(s.Args) > 0 {
				b.WriteString("args = [")
				for i, a := range s.Args {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(tomlString(a))
				}
				b.WriteString("]\n")
			}
			// Only emit env entries with LITERAL values. ${VAR} references
			// must NOT be written — they would override the inherited env.
			literalEnv := map[string]string{}
			for k, v := range s.Env {
				if len(extractEnvRefs(v)) == 0 {
					literalEnv[k] = v
				}
			}
			if len(literalEnv) > 0 {
				b.WriteString("env = { ")
				keys := make([]string, 0, len(literalEnv))
				for k := range literalEnv {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					if i > 0 {
						b.WriteString(", ")
					}
					fmt.Fprintf(&b, "%s = %s", tomlSafeKey(k), tomlString(literalEnv[k]))
				}
				b.WriteString(" }\n")
			}
		} else if s.URL != "" {
			fmt.Fprintf(&b, "url = %s\n", tomlString(s.URL))
			// Two header-handling paths:
			//   - Authorization: Bearer ${VAR}  → bearer_token_env_var = "VAR"
			//   - X-API-Key: ${VAR}  → env_http_headers = { "X-API-Key" = "VAR" }
			//   - X-Foo: literal     → http_headers = { "X-Foo" = "literal" }
			envHeaders := map[string]string{}
			literalHeaders := map[string]string{}
			for k, v := range s.Headers {
				if k == "Authorization" {
					if envName, ok := bearerEnvVarFromHeader(v); ok {
						fmt.Fprintf(&b, "bearer_token_env_var = %s\n", tomlString(envName))
						continue
					}
				}
				// Generic env-header path: ${VAR} → env_http_headers entry.
				// CRITICAL: env_http_headers in Codex's TOML schema substitutes
				// the env var's value as the WHOLE header value. So we may only
				// promote a header into env_http_headers when the value is
				// EXACTLY ${VAR} or $VAR (with optional surrounding whitespace).
				// "Bearer ${TOKEN}" or "prefix $KEY suffix" would silently drop
				// the literal prefix/suffix and send only the env value.
				refs := extractEnvRefs(v)
				trimmed := strings.TrimSpace(v)
				wholeValue := len(refs) == 1 && (trimmed == "${"+refs[0]+"}" || trimmed == "$"+refs[0])
				switch {
				case wholeValue:
					envHeaders[k] = refs[0]
				case len(refs) == 0:
					literalHeaders[k] = v
				default:
					// Mixed literal+env refs OR multiple env refs — Codex
					// has no representation for either, so we drop with a
					// loud warning rather than silently sending a wrong
					// header at runtime.
					if logger != nil {
						logger.Warn("codex MCP HTTP header is not representable (mixed literal/env or multiple refs)",
							"server", s.Name, "header", k)
					}
				}
			}
			if len(envHeaders) > 0 {
				b.WriteString("env_http_headers = { ")
				keys := make([]string, 0, len(envHeaders))
				for k := range envHeaders {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					if i > 0 {
						b.WriteString(", ")
					}
					fmt.Fprintf(&b, "%s = %s", tomlString(k), tomlString(envHeaders[k]))
				}
				b.WriteString(" }\n")
			}
			if len(literalHeaders) > 0 {
				b.WriteString("http_headers = { ")
				keys := make([]string, 0, len(literalHeaders))
				for k := range literalHeaders {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					if i > 0 {
						b.WriteString(", ")
					}
					fmt.Fprintf(&b, "%s = %s", tomlString(k), tomlString(literalHeaders[k]))
				}
				b.WriteString(" }\n")
			}
		}
		b.WriteString("\n")
	}
	// HOME-relative path so Codex loads it without project-trust ceremony.
	homeDir := fmt.Sprintf("/crew/agents/%s", req.AgentSlug)
	return writeFileViaContainer(ctx, container, containerID, homeDir, ".codex/config.toml", b.String(), logger)
}

// anthropicShapeServer renders the canonical mcpSpec back to Claude/Cursor's
// shared schema — useful for adapters that reuse it (currently Cursor +
// partly Droid).
func anthropicShapeServer(s mcpSpec) map[string]any {
	entry := map[string]any{}
	if s.Command != "" {
		entry["command"] = s.Command
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
	}
	if s.URL != "" {
		entry["url"] = s.URL
		if len(s.Headers) > 0 {
			entry["headers"] = s.Headers
		}
	}
	return entry
}

// translateEnvRefsToOpenCode rewrites ${VAR} and $VAR placeholders ANYWHERE
// in the value to OpenCode's {env:VAR} syntax — including substrings like
// "Bearer ${LINEAR_TOKEN}" which is the common form for HTTP Authorization
// headers. Literal values (no interpolation markers) pass through unchanged.
func translateEnvRefsToOpenCode(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = envRefRE.ReplaceAllStringFunc(v, func(match string) string {
			// Submatches: [0]=full, [1]=curly-form name, [2]=bare-form name
			parts := envRefRE.FindStringSubmatch(match)
			name := parts[1]
			if name == "" {
				name = parts[2]
			}
			return "{env:" + name + "}"
		})
	}
	return out
}

// bearerEnvVarFromHeader picks "VAR" out of a value like "Bearer ${VAR}".
// Returns false when the header is a literal token (Codex cannot represent
// those — only env-var references are allowed).
func bearerEnvVarFromHeader(v string) (string, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "Bearer ")
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return v[2 : len(v)-1], true
	}
	if strings.HasPrefix(v, "$") && len(v) > 1 {
		return v[1:], true
	}
	return "", false
}

// tomlString quotes a string for TOML — basic-string form with backslash
// escapes for the characters TOML treats specially.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlSafeKey returns a TOML bare key when the input matches the bare-key
// regex (alpha-numeric + underscore + dash), otherwise a quoted key. MCP
// server names are user-supplied so we cannot assume bare-key compatibility.
func tomlSafeKey(s string) string {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return tomlString(s)
	}
	return s
}
