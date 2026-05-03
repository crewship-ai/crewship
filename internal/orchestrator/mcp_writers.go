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
			s := parseMCPServerJSON(name, blob)
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
// config flavour someone copy-pasted from.
func parseMCPServerJSON(name string, blob json.RawMessage) mcpSpec {
	var raw struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		HTTPURL string            `json:"httpUrl"`
		Headers map[string]string `json:"headers"`
	}
	_ = json.Unmarshal(blob, &raw)

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
	}
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
// schema verbatim (mcpServers map + command/args/env or url/headers), so we
// can serialise our normalised form directly without per-field renaming.
//
// Caveat: per cursor.com community reports (forum #143045 + #148397), MCP is
// currently broken in cursor-agent's --print / non-interactive mode — the
// servers are listed but never invoked. We still write the file so the moment
// upstream fixes the bug nothing else changes; until then the user-visible
// effect is "no MCP tools surface in chat".
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
		out.MCPServers[s.Name] = anthropicShapeServer(s)
	}
	body, _ := json.MarshalIndent(out, "", "  ")
	return writeFileViaContainer(ctx, container, containerID, workDir, ".cursor/mcp.json", string(body), logger)
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
	if err != nil || len(specs) == 0 {
		return err
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
	if err != nil || len(specs) == 0 {
		return err
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
	if err != nil || len(specs) == 0 {
		return err
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

// writeMCPCodex writes <workdir>/.codex/config.toml with [mcp_servers.X]
// sections. Codex is the only adapter using TOML — we hand-serialise rather
// than pull in a TOML library because the surface area is tiny (six possible
// keys per server) and the format is simple line-based.
//
// HTTP servers are configured via url + bearer_token_env_var (NOT a Headers
// map) — Codex's config schema is narrower than Claude's. We pull a Bearer
// token out of an "Authorization" header if present and translate it; other
// headers are not representable and get dropped with a warn log.
func writeMCPCodex(
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
	var b strings.Builder
	b.WriteString("# Generated by Crewship orchestrator. Do not edit by hand.\n\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "[mcp_servers.%s]\n", tomlSafeKey(s.Name))
		b.WriteString("enabled = true\n")
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
			if len(s.Env) > 0 {
				b.WriteString("env = { ")
				keys := make([]string, 0, len(s.Env))
				for k := range s.Env {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					if i > 0 {
						b.WriteString(", ")
					}
					fmt.Fprintf(&b, "%s = %s", tomlSafeKey(k), tomlString(s.Env[k]))
				}
				b.WriteString(" }\n")
			}
		} else if s.URL != "" {
			fmt.Fprintf(&b, "url = %s\n", tomlString(s.URL))
			if auth := s.Headers["Authorization"]; auth != "" {
				// Try to extract "Bearer ${VAR}" → bearer_token_env_var = "VAR"
				if v, ok := bearerEnvVarFromHeader(auth); ok {
					fmt.Fprintf(&b, "bearer_token_env_var = %s\n", tomlString(v))
				} else {
					if logger != nil {
						logger.Warn("codex MCP cannot represent literal Authorization header; user must set bearer_token_env_var manually",
							"server", s.Name)
					}
				}
			}
		}
		b.WriteString("\n")
	}
	return writeFileViaContainer(ctx, container, containerID, workDir, ".codex/config.toml", b.String(), logger)
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
