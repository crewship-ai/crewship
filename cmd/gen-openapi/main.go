// Command gen-openapi extracts the registered HTTP routes from
// internal/api/router_*.go and writes a minimal OpenAPI 3.0 document to
// internal/api/openapi.gen.json, embedded and served at GET /openapi.json
// (internal/api/openapi.go, internal/server/routes.go).
//
// This is a source scan, not a reflection- or runtime-based generator: it
// regex-matches the handful of call shapes this codebase actually uses to
// register a route —
//
//	r.mux.Handle("METHOD /path", ...)
//	r.mux.HandleFunc("METHOD /path", ...)
//	r.authedMut("METHOD", "/path", role, ...)
//	r.authedSelfMut("METHOD", "/path", ...)
//	r.authedAdmin("METHOD", "/path", ...)
//
// It deliberately does NOT infer schemas from handler readJSON/writeJSON
// calls. The response schemas below cover the highest-value read-only
// resource surfaces whose shapes are stable and exercised by the API's live
// contract tests; other operations retain a generic fallback until audited.
//
// It also deliberately EXCLUDES every /api/v1/internal/* route (see
// addRoute) — that surface is sidecar-only, X-Internal-Token authenticated,
// and never called by an external client. GET /openapi.json itself carries
// no auth, so documenting internal routes there would publish a route map of
// the one part of the API deliberately kept non-public.
//
// Run via `go generate ./internal/api/` or directly:
//
//	go run ./cmd/gen-openapi
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	routerDir  = "internal/api"
	outputPath = "internal/api/openapi.gen.json"
)

// combinedPattern matches r.mux.Handle("METHOD /path", ...) / HandleFunc.
var combinedPattern = regexp.MustCompile(`r\.mux\.Handle(?:Func)?\(\s*"([A-Z]+) (/[^"]*)"`)

// splitPattern matches r.authedMut/authedSelfMut/authedAdmin("METHOD", "/path", ...).
var splitPattern = regexp.MustCompile(`r\.authed(?:Mut|SelfMut|Admin)\(\s*"([A-Z]+)"\s*,\s*"(/[^"]*)"`)

type route struct {
	method string
	path   string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-openapi:", err)
		os.Exit(1)
	}
}

func run() error {
	files, err := filepath.Glob(filepath.Join(routerDir, "router_*.go"))
	if err != nil {
		return err
	}
	// router.go itself (not router_*.go) registers no routes directly today,
	// but scan it too in case that changes — cheap and future-proof.
	files = append(files, filepath.Join(routerDir, "router.go"))

	seen := map[route]bool{}
	var routes []route
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %s: %w", f, err)
		}
		src := string(data)
		for _, m := range combinedPattern.FindAllStringSubmatch(src, -1) {
			addRoute(seen, &routes, m[1], m[2])
		}
		for _, m := range splitPattern.FindAllStringSubmatch(src, -1) {
			addRoute(seen, &routes, m[1], m[2])
		}
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})

	doc := buildDocument(routes)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.WriteFile(outputPath, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("gen-openapi: wrote %d routes to %s\n", len(routes), outputPath)
	return nil
}

// addRoute excludes:
//   - the generic /exposed/{token} reverse-proxy mount — it has no fixed
//     method/response shape (it forwards to an arbitrary user app), so it
//     isn't a real documented API operation.
//   - everything under /api/v1/internal/ — the sidecar-only, X-Internal-Token
//     authenticated surface (see docs/api-reference/internal.mdx). This spec
//     is served publicly and unauthenticated at GET /openapi.json; publishing
//     a machine-readable, always-current route map of the internal surface
//     there would hand an unauthenticated caller a ready-made target list for
//     the one part of the API that's deliberately not public, undoing the
//     effect of #1308's internal-detail scrub for no benefit to a real API
//     consumer (who has no use for endpoints they can't call anyway).
func addRoute(seen map[route]bool, routes *[]route, method, path string) {
	if strings.HasPrefix(path, "/exposed/") || strings.HasPrefix(path, "/api/v1/internal/") {
		return
	}
	rt := route{method: method, path: path}
	if seen[rt] {
		return
	}
	seen[rt] = true
	*routes = append(*routes, rt)
}

// pathParamPattern finds Go 1.22 ServeMux path parameters ({id}, {id...}).
// OpenAPI's {param} syntax is the same for a normal segment; a trailing "..."
// wildcard (e.g. {token...}) has no OpenAPI equivalent, so it's rendered as a
// plain {token} segment — an approximation, not a precise match.
var pathParamPattern = regexp.MustCompile(`\{([A-Za-z0-9_]+)(\.\.\.)?\}`)

func openAPIPath(p string) string {
	return pathParamPattern.ReplaceAllString(p, "{$1}")
}

func pathParams(p string) []string {
	var names []string
	for _, m := range pathParamPattern.FindAllStringSubmatch(p, -1) {
		names = append(names, m[1])
	}
	return names
}

func buildDocument(routes []route) map[string]any {
	genericSchema := map[string]any{"type": "object"}
	paths := map[string]any{}
	components := responseComponents()

	for _, rt := range routes {
		opPath := openAPIPath(rt.path)
		opsForPath, ok := paths[opPath].(map[string]any)
		if !ok {
			opsForPath = map[string]any{}
			paths[opPath] = opsForPath
		}

		var params []map[string]any
		for _, name := range pathParams(rt.path) {
			params = append(params, map[string]any{
				"name":     name,
				"in":       "path",
				"required": true,
				"schema":   map[string]any{"type": "string"},
			})
		}

		op := map[string]any{
			"operationId": operationID(rt.method, rt.path),
			"tags":        []string{tagFor(rt.path)},
			"responses": map[string]any{
				"200": map[string]any{
					"description": "OK",
					"content": map[string]any{
						"application/json": map[string]any{"schema": responseSchema(rt)},
					},
				},
			},
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		switch rt.method {
		case "POST", "PUT", "PATCH":
			op["requestBody"] = map[string]any{
				"content": map[string]any{
					"application/json": map[string]any{"schema": genericSchema},
				},
			}
		}

		opsForPath[strings.ToLower(rt.method)] = op
	}

	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title": "Crewship API",
			"description": "Generated from internal/api/router_*.go route registrations (cmd/gen-openapi). " +
				"Paths and methods are exact; audited read-only resource responses use component schemas and " +
				"remaining bodies use a generic fallback — see cmd/gen-openapi/main.go.",
			"version": "generated",
		},
		"paths":      paths,
		"components": components,
	}
}

// responseSchema returns the schema for an audited read-only endpoint. Keep
// this keyed by method/path rather than operation id: it makes accidental
// changes to a route's public shape visible in the generated document.
func responseSchema(rt route) map[string]any {
	if rt.method != "GET" {
		return map[string]any{"type": "object"}
	}
	name := auditedResponseName(rt.path)
	if name == "" {
		return map[string]any{"type": "object"}
	}
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func auditedResponseName(path string) string {
	return map[string]string{
		"/api/v1/workspaces":               "WorkspaceList",
		"/api/v1/workspaces/{workspaceId}": "Workspace",
		"/api/v1/crews":                    "CrewList",
		"/api/v1/crews/{crewId}":           "Crew",
		"/api/v1/agents":                   "AgentList",
		"/api/v1/agents/{agentId}":         "Agent",
		"/api/v1/projects":                 "ProjectList",
		"/api/v1/projects/{projectId}":     "ProjectListItem",
		"/api/v1/issues":                   "IssueList",
		"/api/v1/issues/{identifier}":      "Issue",
		"/api/v1/skills":                   "SkillList",
		"/api/v1/skills/{skillId}":         "Skill",
		"/api/v1/runs":                     "RunList",
		"/api/v1/runs/{id}":                "Run",
	}[path]
}

func ref(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

func scalar(typ string) map[string]any { return map[string]any{"type": typ} }

func nullable(typ string) map[string]any {
	return map[string]any{"type": typ, "nullable": true}
}

func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func object(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties}
}

func responseComponents() map[string]any {
	stringProps := func(names ...string) map[string]any {
		p := make(map[string]any, len(names))
		for _, name := range names {
			p[name] = scalar("string")
		}
		return p
	}

	workspace := object(map[string]any{
		"id": scalar("string"), "name": scalar("string"), "slug": scalar("string"),
		"logo_url": nullable("string"), "preferred_language": nullable("string"),
		"created_at": scalar("string"), "updated_at": scalar("string"),
		"currentUserRole": nullable("string"), "currentUserCapabilities": array(scalar("string")),
		"allow_privileged_credentials": scalar("boolean"), "run_retention_days": nullable("integer"),
		"_count": ref("WorkspaceCounts"), "_count_crews": scalar("integer"),
		"_count_agents": scalar("integer"), "_count_members": scalar("integer"),
	})
	crew := object(map[string]any{
		"id": scalar("string"), "workspace_id": scalar("string"), "name": scalar("string"), "slug": scalar("string"),
		"description": nullable("string"), "color": nullable("string"), "icon": nullable("string"), "avatar_style": nullable("string"),
		"container_memory_mb": scalar("integer"), "container_cpus": scalar("number"), "container_ttl_hours": nullable("integer"),
		"network_mode": scalar("string"), "network_mode_enforced": scalar("boolean"), "network_mode_unenforced_reason": scalar("string"),
		"allowed_domains": array(scalar("string")), "allow_private_endpoints": scalar("boolean"),
		"mcp_config_json": nullable("string"), "escalation_config": nullable("string"), "runtime_image": nullable("string"),
		"devcontainer_config": nullable("string"), "mise_config": nullable("string"), "services_json": nullable("string"),
		"cached_image": nullable("string"), "config_hash": nullable("string"), "issue_prefix": nullable("string"),
		"max_ephemeral_agents": scalar("integer"), "created_at": scalar("string"), "updated_at": scalar("string"),
		"_count": ref("CrewCounts"),
	})
	agent := object(map[string]any{
		"id": scalar("string"), "crew_id": nullable("string"), "workspace_id": scalar("string"), "name": scalar("string"), "slug": scalar("string"),
		"description": nullable("string"), "role_title": nullable("string"), "agent_role": scalar("string"), "lead_mode": nullable("string"), "status": scalar("string"),
		"cli_adapter": scalar("string"), "llm_provider": nullable("string"), "llm_model": nullable("string"), "system_prompt": nullable("string"),
		"avatar_seed": nullable("string"), "avatar_style": nullable("string"), "avatar_url": nullable("string"), "timeout_seconds": scalar("integer"),
		"tool_profile": scalar("string"), "memory_enabled": scalar("boolean"), "cli_tools": nullable("string"), "schedule_cron": nullable("string"),
		"schedule_prompt": nullable("string"), "schedule_enabled": scalar("boolean"), "schedule_last_run": nullable("string"), "schedule_next_run": nullable("string"),
		"webhook_require_timestamp": scalar("boolean"), "webhook_secret_set": nullable("boolean"), "mcp_config_json": nullable("string"),
		"created_at": scalar("string"), "updated_at": scalar("string"), "crew": ref("AgentCrew"), "_count": ref("AgentCounts"),
		"created_by_user_id": scalar("string"), "ephemeral": scalar("boolean"), "expires_at": nullable("string"), "expired_at": nullable("string"),
		"parent_lead_id": nullable("string"), "hire_reason": nullable("string"),
	})
	project := object(map[string]any{
		"id": scalar("string"), "workspace_id": scalar("string"), "name": scalar("string"), "slug": scalar("string"),
		"description": nullable("string"), "icon": nullable("string"), "color": scalar("string"), "status": scalar("string"),
		"priority": scalar("string"), "health": scalar("string"), "lead_type": nullable("string"), "lead_id": nullable("string"),
		"lead_name": nullable("string"), "start_date": nullable("string"), "target_date": nullable("string"), "created_at": scalar("string"),
		"updated_at": scalar("string"), "issue_count": scalar("integer"), "done_count": scalar("integer"), "progress": scalar("integer"),
	})
	issue := object(map[string]any{
		"id": scalar("string"), "workspace_id": scalar("string"), "crew_id": scalar("string"), "crew_name": scalar("string"), "crew_slug": scalar("string"),
		"number": nullable("integer"), "identifier": nullable("string"), "title": scalar("string"), "description": nullable("string"),
		"status": scalar("string"), "priority": scalar("string"), "assignee_type": nullable("string"), "assignee_id": nullable("string"), "assignee_name": nullable("string"),
		"due_date": nullable("string"), "sort_order": scalar("number"), "mission_type": scalar("string"), "lead_agent_id": scalar("string"),
		"created_at": scalar("string"), "updated_at": scalar("string"), "completed_at": nullable("string"), "labels": array(ref("Label")),
		"project_id": nullable("string"), "project_name": nullable("string"), "estimate": nullable("integer"), "parent_issue_id": nullable("string"),
		"milestone_id": nullable("string"), "sub_issues_count": scalar("integer"), "comment_count": scalar("integer"), "routine_id": nullable("string"),
		"routine_slug": nullable("string"), "routine_name": nullable("string"), "created_by": ref("IssueCreator"), "authored_via": nullable("string"),
	})
	skill := object(map[string]any{
		"id": scalar("string"), "name": scalar("string"), "slug": scalar("string"), "display_name": scalar("string"), "description": nullable("string"),
		"version": scalar("string"), "author": nullable("string"), "category": scalar("string"), "source": scalar("string"), "icon": nullable("string"),
		"verification": scalar("string"), "downloads": scalar("integer"), "rating_avg": nullable("number"), "rating_count": scalar("integer"),
		"tags": nullable("string"), "featured": scalar("boolean"), "pricing_tier": scalar("string"), "tool_count": nullable("integer"), "vendor": nullable("string"),
		"homepage": nullable("string"), "spdx_license": nullable("string"), "runtime": scalar("string"), "maturity": scalar("string"),
		"scan_status": scalar("string"), "description_quality": nullable("string"), "created_at": scalar("string"), "updated_at": scalar("string"),
		"installed_on": array(ref("InstalledSkillAgent")),
	})
	run := object(map[string]any{
		"id": scalar("string"), "agent_id": scalar("string"), "chat_id": nullable("string"), "workspace_id": scalar("string"), "triggered_by": nullable("string"),
		"trigger_type": scalar("string"), "status": scalar("string"), "started_at": nullable("string"), "finished_at": nullable("string"), "error_message": nullable("string"),
		"exit_code": nullable("integer"), "metadata": map[string]any{"type": "object", "additionalProperties": true}, "model": nullable("string"), "created_at": scalar("string"),
		"agent_name": nullable("string"), "agent_slug": nullable("string"), "crew_name": nullable("string"),
	})
	schemas := map[string]any{
		"Workspace": workspace, "WorkspaceList": array(ref("Workspace")), "WorkspaceCounts": object(map[string]any{"crews": scalar("integer"), "agents": scalar("integer"), "members": scalar("integer")}),
		"Crew": crew, "CrewList": array(ref("Crew")), "CrewCounts": object(map[string]any{"agents": scalar("integer"), "members": scalar("integer")}),
		"Agent": agent, "AgentList": array(ref("Agent")), "AgentCrew": object(map[string]any{"name": scalar("string"), "slug": scalar("string"), "color": nullable("string"), "avatar_style": nullable("string")}), "AgentCounts": object(map[string]any{"skills": scalar("integer"), "credentials": scalar("integer"), "chats": scalar("integer")}),
		"ProjectListItem": project, "ProjectList": array(ref("ProjectListItem")), "Issue": issue, "IssueList": array(ref("Issue")),
		"Label":        object(map[string]any{"id": scalar("string"), "name": scalar("string"), "color": scalar("string"), "label_group": nullable("string")}),
		"IssueCreator": object(map[string]any{"type": scalar("string"), "id": scalar("string"), "name": scalar("string")}),
		"Skill":        skill, "SkillList": array(ref("Skill")), "InstalledSkillAgent": object(stringProps("agent_id", "agent_slug", "agent_name", "avatar_url", "crew_id", "crew_slug", "crew_name", "crew_color", "crew_icon", "crew_avatar_style")),
		"Run": run, "RunList": object(map[string]any{"data": array(ref("Run")), "stats": ref("RunStats"), "pagination": ref("Pagination")}),
		"RunStats":   object(map[string]any{"running": scalar("integer"), "today": scalar("integer"), "failed": scalar("integer")}),
		"Pagination": object(map[string]any{"page": scalar("integer"), "limit": scalar("integer"), "total": scalar("integer"), "total_pages": scalar("integer")}),
	}
	return map[string]any{"schemas": schemas}
}

// operationID builds a stable, readable id like "get_agents_id" from
// "GET /api/v1/agents/{id}" for tooling that wants one (schemathesis,
// client generators).
func operationID(method, path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
		s = strings.TrimSuffix(s, "...")
		segs[i] = s
	}
	return strings.ToLower(method) + "_" + strings.Join(segs, "_")
}

// tagFor groups operations for the spec's tag list — the path segment right
// after /api/v1/ (or /api/v1/internal/, since "internal" alone isn't a
// useful grouping), falling back to the first segment for anything else
// (bootstrap/auth endpoints living directly under /api/v1/).
func tagFor(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if s == "v1" && i+1 < len(segs) {
			if segs[i+1] == "internal" && i+2 < len(segs) {
				return segs[i+2]
			}
			return segs[i+1]
		}
	}
	if len(segs) > 0 {
		return segs[0]
	}
	return "misc"
}
