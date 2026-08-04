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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var routerDir = "internal/api"
var outputPath = "internal/api/openapi.gen.json"

// combinedPattern matches r.mux.Handle("METHOD /path", ...) / HandleFunc.
var combinedPattern = regexp.MustCompile(`r\.mux\.Handle(?:Func)?\(\s*"([A-Z]+) (/[^"]*)"`)

// splitPattern matches r.authedMut/authedSelfMut/authedAdmin("METHOD", "/path", ...).
var splitPattern = regexp.MustCompile(`r\.authed(?:Mut|SelfMut|Admin)\(\s*"([A-Z]+)"\s*,\s*"(/[^"]*)"`)

type route struct {
	method string
	path   string
	source string
	start  int
	call   string
	annot  string
	auth   bool
	ws     bool
}

type handlerInfo struct {
	query    map[string]string
	statuses map[string]bool
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
	if len(files) == 0 {
		// go:generate runs with the package directory as its working directory,
		// while direct invocation runs from the repository root.
		if wd, _ := os.Getwd(); filepath.Base(wd) == "api" {
			routerDir = "../../internal/api"
			outputPath = "../../internal/api/openapi.gen.json"
			files, err = filepath.Glob(filepath.Join(routerDir, "router_*.go"))
			if err != nil {
				return err
			}
		}
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
		for _, m := range combinedPattern.FindAllStringSubmatchIndex(src, -1) {
			addRoute(seen, &routes, src[m[2]:m[3]], src[m[4]:m[5]], f, m[0], src)
		}
		for _, m := range splitPattern.FindAllStringSubmatchIndex(src, -1) {
			addRoute(seen, &routes, src[m[2]:m[3]], src[m[4]:m[5]], f, m[0], src)
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
func addRoute(seen map[route]bool, routes *[]route, method, path, file string, start int, src string) {
	if strings.HasPrefix(path, "/exposed/") || strings.HasPrefix(path, "/api/v1/internal/") || strings.HasPrefix(path, "/api/auth/") {
		return
	}
	call := registrationCall(src, start)
	rt := route{
		method: method, path: path, source: file, start: start, call: call,
		annot: routeAnnotation(src, start), auth: strings.Contains(call, "authed"),
		ws: strings.Contains(call, "wsCtx") || strings.Contains(call, "RequireWorkspace"),
	}
	key := route{method: method, path: path}
	if seen[key] {
		return
	}
	seen[key] = true
	*routes = append(*routes, rt)
}

var annotationPattern = regexp.MustCompile(`(?m)^\s*//\s*openapi:\s*(.+)$`)

func routeAnnotation(src string, start int) string {
	begin := start - 400
	if begin < 0 {
		begin = 0
	}
	matches := annotationPattern.FindAllStringSubmatch(src[begin:start], -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// registrationCall returns the complete registration expression. Keeping this
// small amount of source context lets the generator associate a route with a
// handler without requiring the application to expose reflection metadata.
func registrationCall(src string, start int) string {
	open := strings.IndexByte(src[start:], '(')
	if open < 0 {
		return src[start:]
	}
	open += start
	depth := 0
	inString := false
	escaped := false
	for i := open; i < len(src); i++ {
		c := src[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return src[start:]
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
	paths := map[string]any{}
	components := responseComponents()
	schemas := components["schemas"].(map[string]any)
	_, crewWorkspaceComponentsV1 := crewWorkspaceGETSchemaCatalogV1()
	for _, catalog := range []map[string]any{
		coreResourceSchemas(), issueSkillCredentialSchemaComponents(), executionSchemaComponents(), crewWorkspaceComponentsV1,
	} {
		for name, schema := range catalog {
			// Domain catalogs are the audited source of truth.  They intentionally
			// replace the older fallback entries with the same component name.
			schemas[name] = schema
		}
	}
	components["securitySchemes"] = map[string]any{
		"bearerAuth":          map[string]any{"type": "http", "scheme": "bearer"},
		"sessionCookie":       map[string]any{"type": "apiKey", "in": "cookie", "name": "next-auth.session-token"},
		"secureSessionCookie": map[string]any{"type": "apiKey", "in": "cookie", "name": "__Secure-authjs.session-token"},
	}

	for _, rt := range routes {
		opPath := openAPIPath(rt.path)
		opsForPath, ok := paths[opPath].(map[string]any)
		if !ok {
			opsForPath = map[string]any{}
			paths[opPath] = opsForPath
		}

		info := inferHandlerInfo(rt)
		applyAnnotation(&info, rt.annot)
		var params []map[string]any
		for _, name := range pathParams(rt.path) {
			params = append(params, map[string]any{
				"name":     name,
				"in":       "path",
				"required": true,
				"schema":   map[string]any{"type": "string"},
			})
		}
		for name, typ := range info.query {
			params = append(params, map[string]any{
				"name": name, "in": "query", "required": false,
				"schema": map[string]any{"type": typ},
			})
		}
		sort.Slice(params, func(i, j int) bool {
			if params[i]["in"] != params[j]["in"] {
				return params[i]["in"].(string) < params[j]["in"].(string)
			}
			return params[i]["name"].(string) < params[j]["name"].(string)
		})

		responses := map[string]any{}
		for status := range info.statuses {
			responses[status] = map[string]any{"description": httpStatusDescription(status)}
		}
		if len(responses) == 0 {
			responses["200"] = map[string]any{"description": "OK"}
		}
		// Error responses use the shared RFC 7807 envelope. Success responses
		// retain the generic schema until endpoint-specific schemas are added.
		for status, response := range responses {
			if status[0] != '2' {
				response.(map[string]any)["content"] = map[string]any{"application/problem+json": map[string]any{"schema": problemSchema()}}
			}
			if status[0] == '2' {
				response.(map[string]any)["content"] = responseContentForRoute(rt)
			}
		}
		op := map[string]any{
			"operationId": operationID(rt.method, rt.path),
			"tags":        []string{tagFor(rt.path)},
			"responses":   responses,
		}
		if rt.auth {
			op["security"] = []map[string][]string{{"bearerAuth": {}}, {"sessionCookie": {}}, {"secureSessionCookie": {}}}
		} else {
			op["security"] = []map[string][]string{}
		}
		if rt.ws {
			params = append(params, map[string]any{
				"name": "X-Workspace-ID", "in": "header", "required": false,
				"description": "Workspace ID or slug.", "schema": map[string]any{"type": "string"},
			})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		switch rt.method {
		case "POST", "PUT", "PATCH":
			request := requestSchema(rt)
			op["requestBody"] = map[string]any{
				"content": map[string]any{
					"application/json": map[string]any{"schema": request},
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
				"Paths, methods, parameters, status branches, and audited read response schemas are source-derived; " +
				"remaining request/response bodies use a generic fallback — see cmd/gen-openapi/main.go.",
			"version": "generated",
		},
		"paths":      paths,
		"components": components,
	}
}

func routeSchemaCatalog() map[string]DomainSchema {
	result := map[string]DomainSchema{}
	for _, domain := range operationalDomainSchemaCatalog() {
		for key, schema := range domain {
			result[key] = schema
		}
	}
	for key, schema := range schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources() {
		result[key] = schema
	}
	for _, domain := range publicActivitySchemaCatalog() {
		for key, schema := range domain {
			result[key] = schema
		}
	}
	for key, schema := range observabilityPaymentsSchemaCatalog() {
		result[key] = schema
	}
	crewWorkspaceCatalogV1, _ := crewWorkspaceGETSchemaCatalogV1()
	for _, domain := range crewWorkspaceCatalogV1 {
		for key, schema := range domain {
			result[key] = schema
		}
	}
	for key, name := range executionResponseSchemas() {
		result[key] = DomainSchema{Response: ref(name)}
	}
	for key, name := range executionRequestSchemas() {
		schema := result[key]
		schema.Request = ref(name)
		result[key] = schema
	}
	return result
}

func responseSchemaName(path string) string {
	return map[string]string{
		"GET /api/v1/issues": "IssueList", "GET /api/v1/issues/{identifier}": "Issue",
		"GET /api/v1/skills": "SkillList", "GET /api/v1/skills/{skillId}": "Skill",
		"GET /api/v1/credentials": "CredentialList", "GET /api/v1/credentials/{credentialId}": "Credential",
		"GET /api/v1/credentials/{credentialId}/fields": "CredentialFieldList",
		"GET /api/v1/credentials/bindings":              "CredentialBindingList",
	}[path]
}

func requestSchema(rt route) map[string]any {
	if schema, ok := routeSchemaCatalog()[rt.method+" "+rt.path]; ok && schema.Request != nil {
		return schema.Request
	}
	name := map[string]string{
		"POST /api/v1/workspaces": "WorkspaceCreateRequest", "PATCH /api/v1/workspaces/{workspaceId}": "WorkspaceUpdateRequest",
		"POST /api/v1/crews": "CrewCreateRequest", "PATCH /api/v1/crews/{crewId}": "CrewUpdateRequest", "PUT /api/v1/crews/{crewId}": "CrewUpdateRequest",
		"POST /api/v1/agents": "AgentCreateRequest", "PATCH /api/v1/agents/{agentId}": "AgentUpdateRequest",
		"POST /api/v1/projects": "ProjectCreateRequest", "PATCH /api/v1/projects/{projectId}": "ProjectUpdateRequest",
		"POST /api/v1/agents/hire": "HireRequest", "POST /api/v1/labels": "LabelCreateRequest", "PATCH /api/v1/labels/{labelId}": "LabelUpdateRequest",
		"POST /api/v1/credentials": "CredentialCreateRequest", "POST /api/v1/credentials/bindings": "CredentialBindingRequest",
		"POST /api/v1/workspaces/{workspaceId}/skills/import": "SkillImportRequest",
	}[rt.method+" "+rt.path]
	if name != "" {
		return ref(name)
	}
	return map[string]any{"type": "object"}
}

func responseSchemaForRoute(rt route) map[string]any {
	if schema, ok := routeSchemaCatalog()[rt.method+" "+rt.path]; ok && schema.Response != nil {
		return schema.Response
	}
	if name := responseSchemaName(rt.method + " " + rt.path); name != "" {
		return ref(name)
	}
	return responseSchema(rt)
}

func responseContentForRoute(rt route) map[string]any {
	if schema, ok := routeSchemaCatalog()[rt.method+" "+rt.path]; ok && len(schema.ResponseMedia) > 0 {
		content := map[string]any{}
		for _, media := range schema.ResponseMedia {
			content[media] = map[string]any{"schema": schema.Response}
		}
		return content
	}
	return responseContent(rt.path, responseSchemaForRoute(rt))
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
func problemSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"type", "title", "status", "detail"}, "properties": map[string]any{
		"type": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
		"status": map[string]any{"type": "integer"}, "detail": map[string]any{"type": "string"},
	}}
}

// An annotation is intentionally small and local to a route. Example:
//
//	// openapi: query page:integer limit:integer; responses 200,400,401,403,500
//
// It is an escape hatch for handlers whose query parsing is delegated to a
// helper, or whose status is produced outside the handler body.
func applyAnnotation(info *handlerInfo, annotation string) {
	if annotation == "" {
		return
	}
	if i := strings.Index(annotation, "query "); i >= 0 {
		part := annotation[i+len("query "):]
		if end := strings.Index(part, ";"); end >= 0 {
			part = part[:end]
		}
		for _, field := range strings.Fields(part) {
			bits := strings.Split(field, ":")
			typ := "string"
			if len(bits) > 1 {
				typ = bits[1]
			}
			switch typ {
			case "integer", "number", "boolean", "string":
				info.query[bits[0]] = typ
			}
		}
	}
	if i := strings.Index(annotation, "responses "); i >= 0 {
		part := strings.TrimSpace(annotation[i+len("responses "):])
		for _, code := range strings.Split(strings.Fields(part)[0], ",") {
			if _, err := strconv.Atoi(code); err == nil {
				info.statuses[code] = true
			}
		}
	}
}

var statusNames = map[string]string{"StatusOK": "200", "StatusCreated": "201", "StatusAccepted": "202", "StatusNoContent": "204", "StatusBadRequest": "400", "StatusUnauthorized": "401", "StatusForbidden": "403", "StatusNotFound": "404", "StatusConflict": "409", "StatusTooManyRequests": "429", "StatusInternalServerError": "500", "StatusNotImplemented": "501", "StatusBadGateway": "502", "StatusServiceUnavailable": "503", "StatusGatewayTimeout": "504"}
var selectorPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)`)
var functionPattern = regexp.MustCompile(`func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
var queryPattern = regexp.MustCompile(`(?:URL\.Query\(\)|\b[A-Za-z_][A-Za-z0-9_]*)\.(?:Get|Has|Values)\(\s*"([^"]+)"`)

func inferHandlerInfo(rt route) handlerInfo {
	info := handlerInfo{query: map[string]string{}, statuses: map[string]bool{"200": true}}
	srcBytes, err := os.ReadFile(rt.source)
	if err != nil {
		return info
	}
	src := string(srcBytes)
	// Middleware failures are part of every wrapped operation's wire contract.
	if strings.Contains(rt.call, "authed") {
		info.statuses["401"] = true
	}
	if strings.Contains(rt.call, "authedAdmin") || strings.Contains(rt.call, "authedMut") || strings.Contains(rt.call, "authedSelfMut") || strings.Contains(rt.call, "wsCtx") {
		info.statuses["403"] = true
	}
	if strings.Contains(rt.call, "wsCtx") {
		info.statuses["400"] = true
	}
	selectors := selectorPattern.FindAllStringSubmatch(rt.call, -1)
	if len(selectors) == 0 {
		return info
	}
	receiver := selectors[len(selectors)-1][1]
	method := selectors[len(selectors)-1][2]
	// Prefer the concrete handler type declared beside the registration (for
	// example `audit := NewAuditHandler(...)`). This avoids merging the query
	// parameters of every `Get` method in the package.
	typeName := ""
	decl := regexp.MustCompile(`\b` + regexp.QuoteMeta(receiver) + `\s*:=\s*(?:&?)([A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(src)
	if len(decl) == 2 {
		typeName = strings.TrimPrefix(decl[1], "New")
	}
	files, _ := filepath.Glob(filepath.Join(routerDir, "*.go"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		candidate := string(data)
		functionRE := functionPattern
		if typeName != "" {
			functionRE = regexp.MustCompile(`func\s+\([^)]*\*` + regexp.QuoteMeta(typeName) + `\)\s*` + regexp.QuoteMeta(method) + `\s*\(`)
		}
		for _, m := range functionRE.FindAllStringIndex(candidate, -1) {
			body := functionBody(candidate, m[1])
			if body == "" {
				continue
			}
			for _, q := range queryPattern.FindAllStringSubmatch(body, -1) {
				info.query[q[1]] = "string"
			}
			for name, code := range statusNames {
				if strings.Contains(body, "http."+name) {
					info.statuses[code] = true
				}
			}
			for _, n := range regexp.MustCompile(`(?:writeJSON|WriteHeader)\([^\n]*?\b(\d{3})\b`).FindAllStringSubmatch(body, -1) {
				info.statuses[n[1]] = true
			}
		}
	}
	return info
}

func functionBody(src string, from int) string {
	open := strings.IndexByte(src[from:], '{')
	if open < 0 {
		return ""
	}
	open += from
	depth := 0
	for i := open; i < len(src); i++ {
		if src[i] == '{' {
			depth++
		}
		if src[i] == '}' {
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return ""
}

func httpStatusDescription(code string) string {
	n, _ := strconv.Atoi(code)
	return http.StatusText(n)
}

func responseContent(path string, schema map[string]any) map[string]any {
	types := []string{"application/json"}
	switch {
	case path == "/api/v1/journal/stream":
		types = []string{"text/event-stream"}
	case path == "/api/v1/memory/export":
		types = []string{"application/json", "application/zip"}
	case path == "/api/v1/admin/backups/download":
		types = []string{"application/zstd"}
	case strings.HasSuffix(path, "/memory/versions/{id}/content"):
		types = []string{"text/markdown", "application/octet-stream"}
	case strings.HasSuffix(path, "/files/download"):
		types = []string{"application/octet-stream"}
	case strings.HasSuffix(path, "/avatar") && !strings.HasSuffix(path, "/apply-avatar-style"):
		types = []string{"image/svg+xml"}
	}
	content := make(map[string]any, len(types))
	for _, typ := range types {
		responseSchema := schema
		if typ != "application/json" {
			responseSchema = map[string]any{"type": "string", "format": "binary"}
		}
		content[typ] = map[string]any{"schema": responseSchema}
	}
	return content
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
