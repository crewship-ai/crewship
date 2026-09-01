// Command gen-openapi extracts the registered HTTP routes from internal/api
// and writes a minimal OpenAPI 3.0 document to internal/api/openapi.gen.json,
// embedded and served at GET /openapi.json (internal/api/openapi.go,
// internal/server/routes.go).
//
// Which FILES it scans is decided by content, not by filename (#1953): every
// non-test .go file in internal/api that contains a route-registration call is
// read. It used to glob router_*.go, which made the naming convention
// load-bearing — a registrar under any other name contributed nothing to the
// spec and nothing said so.
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
	"go/ast"
	"go/parser"
	"go/token"
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

// queryParam is one documented query parameter. required is only ever true
// when the handler was seen to reject the request outright without it — see
// requiredQueryParams. Everything else stays optional, because over-claiming
// required tells a client a request will fail when it will succeed.
type queryParam struct {
	typ      string
	required bool
}

type handlerInfo struct {
	query    map[string]queryParam
	statuses map[string]bool
	// Which error envelope this operation's handler writes. Both helpers go
	// through writeJSON, so the MEDIA TYPE is application/json either way —
	// these decide the body schema only. Neither set means the statuses came
	// from middleware (auth, role and workspace gates), which replies
	// {"error": …}; that is the default and is verified live in
	// error_contract_test.go.
	repliesError   bool
	repliesProblem bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-openapi:", err)
		os.Exit(1)
	}
}

func run() error {
	files, err := routeSourceFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		// go:generate runs with the package directory as its working directory,
		// while direct invocation runs from the repository root.
		if wd, _ := os.Getwd(); filepath.Base(wd) == "api" {
			routerDir = "../../internal/api"
			outputPath = "../../internal/api/openapi.gen.json"
			cachedSourceFiles = nil
			files, err = routeSourceFiles()
			if err != nil {
				return err
			}
		}
	}

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

// routeAnnotation returns the `// openapi:` annotation attached to the
// registration starting at `start`, if any.
//
// "Attached" means it sits in the contiguous run of comment lines immediately
// above the registration — the doc-comment position. A byte window instead of
// a line walk is what made the annotation on /api/v1/audit (router_admin.go)
// also apply to /api/v1/admin/stats, /admin/users and /admin/workspaces three
// lines below it: those handlers read no query string at all, yet each
// published the audit log's ten filters. An annotation is local to one route
// by design (see applyAnnotation); this makes the lookup say so.
func routeAnnotation(src string, start int) string {
	lines := strings.Split(src[:start], "\n")
	// The last element is the partial line the registration starts on; the
	// comment block, if any, is above it.
	for i := len(lines) - 2; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			return ""
		}
		if m := annotationPattern.FindStringSubmatch(lines[i]); m != nil {
			return m[1]
		}
	}
	return ""
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
	_, remainingCrewAgentComponentsV1 := remainingCrewAgentSchemaCatalogV1()
	_, credentialComponents := credentialsConnectorsAuthProfileSchemaCatalog()
	_, remainingComponents := remainingAuthIntegrationsSchemaCatalog()
	_, finalAdminPlatformComponents := finalAdminPlatformSchemaCatalog()
	_, finalComponents := finalIntegrationsConnectorsSchemaCatalog()
	_, coreResourceRequestComponentsV2 := coreResourceRequestSchemaCatalogV2()
	_, integrationsAuthRequestComponents := integrationsAuthRequestBodySchemaCatalog()
	_, adminSpecialComponents := adminSpecialRequestSchemaCatalog()
	_, finalCoreRequestComponents := finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog()
	_, finalAuthComponents := finalAuthIntegrationsCredentialsNotificationsUsersWebhooksSchemaCatalog()
	_, onboardingProposalComponents := onboardingProposalSchemaCatalog()
	for _, catalog := range []map[string]any{
		coreResourceSchemas(), issueSkillCredentialSchemaComponents(), executionSchemaComponents(), crewWorkspaceComponentsV1,
		credentialComponents, remainingCrewAgentComponentsV1, remainingComponents, finalAdminPlatformComponents, finalComponents,
		coreResourceRequestComponentsV2, integrationsAuthRequestComponents, adminSpecialComponents, finalCoreRequestComponents, finalAuthComponents, onboardingProposalComponents,
	} {
		for name, schema := range catalog {
			// Domain catalogs are the audited source of truth.  They intentionally
			// replace the older fallback entries with the same component name.
			schemas[name] = schema
		}
	}
	_, workflowRequestComponents := workflowRequestSchemaCatalog()
	for name, schema := range workflowRequestComponents {
		schemas[name] = schema
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
		if schema, ok := routeSchemaCatalog()[rt.method+" "+rt.path]; ok && schema.SuccessStatuses != nil {
			success := map[string]bool{}
			for _, status := range schema.SuccessStatuses {
				success[status] = true
			}
			for status := range info.statuses {
				if status[0] != '2' {
					success[status] = true
				}
			}
			info.statuses = success
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
		for name, param := range info.query {
			schema := map[string]any{"type": param.typ}
			// requiredQueryParams proves something stronger than OpenAPI's
			// `required`, which only means "present" and is satisfied by the
			// empty string: it finds a handler that READS the parameter and
			// then returns 4xx when it is EMPTY. Say so, or a client sends ""
			// because the document permits it and gets a 400 the document said
			// could not happen — 10 of the findings behind #1815 are exactly
			// that. Only string parameters: minLength is meaningless on the
			// others and the inference only ever fires on a `== ""` guard.
			if param.required && param.typ == "string" {
				schema["minLength"] = 1
			}
			params = append(params, map[string]any{
				"name": name, "in": "query", "required": param.required,
				"schema": schema,
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
		// Error responses describe what the server sends, which is not what
		// this generator used to claim (#1919). Both error helpers route
		// through writeJSON, so the media type is application/json for every
		// one of them; only the body differs, and only by which helper the
		// handler reached for. Success responses retain the generic schema
		// until endpoint-specific schemas are added.
		for status, response := range responses {
			if status[0] != '2' {
				response.(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": errorBodySchema(info)}}
			}
			if status[0] == '2' {
				if status != "204" {
					response.(map[string]any)["content"] = responseContentForRoute(rt)
				}
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
			op["requestBody"] = requestBodyForRoute(rt)
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
			result[key] = mergeDomainSchema(result[key], schema)
		}
	}
	for key, schema := range schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range remainingExecutionDomainSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range remainingAdminSystemSchemaCatalogV2() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for _, domain := range publicActivitySchemaCatalog() {
		for key, schema := range domain {
			result[key] = mergeDomainSchema(result[key], schema)
		}
	}
	for key, schema := range observabilityPaymentsSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	credentialCatalog, _ := credentialsConnectorsAuthProfileSchemaCatalog()
	for _, domain := range credentialCatalog {
		for key, schema := range domain {
			result[key] = mergeDomainSchema(result[key], schema)
		}
	}
	for key, schema := range remainingAuthIntegrationsSchemaCatalogRoutes() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	crewWorkspaceCatalogV1, _ := crewWorkspaceGETSchemaCatalogV1()
	for _, domain := range crewWorkspaceCatalogV1 {
		for key, schema := range domain {
			result[key] = mergeDomainSchema(result[key], schema)
		}
	}
	remainingCrewAgentCatalogV1, _ := remainingCrewAgentSchemaCatalogV1()
	for key, schema := range remainingCrewAgentCatalogV1 {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	finalAdminPlatformRoutes, _ := finalAdminPlatformSchemaCatalog()
	for key, schema := range finalAdminPlatformRoutes {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	finalCatalog, _ := finalIntegrationsConnectorsSchemaCatalog()
	for key, schema := range finalCatalog {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	integrationAuthRequests, _ := integrationsAuthRequestBodySchemaCatalog()
	for key, schema := range integrationAuthRequests {
		// This catalog owns request bodies only. Preserve the response contract
		// supplied by the response-domain catalog for the same operation.
		merged := result[key]
		if schema.Request != nil {
			merged.Request = schema.Request
		}
		if schema.RequestMedia != nil {
			merged.RequestMedia = schema.RequestMedia
		}
		if schema.Response != nil {
			merged.Response = schema.Response
		}
		if schema.ResponseMedia != nil {
			merged.ResponseMedia = schema.ResponseMedia
		}
		if schema.SuccessStatuses != nil {
			merged.SuccessStatuses = schema.SuccessStatuses
		}
		result[key] = merged
	}
	for key, schema := range finalWorkflowIssueSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range automationSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range hookSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range pagesSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, name := range executionResponseSchemas() {
		result[key] = mergeDomainSchema(result[key], DomainSchema{Response: ref(name)})
	}
	for key, name := range executionRequestSchemas() {
		schema := result[key]
		schema.Request = ref(name)
		result[key] = schema
	}
	for key, schema := range finalSpecialDomainSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range final21GenericResponseSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	for key, schema := range routineTrustSchemaCatalog() {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	coreResourceRequestsV2, _ := coreResourceRequestSchemaCatalogV2()
	for key, schema := range coreResourceRequestsV2 {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	workflowRoutes, _ := workflowRequestSchemaCatalog()
	for key, schema := range workflowRoutes {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	adminSpecialRoutes, _ := adminSpecialRequestSchemaCatalog()
	for key, schema := range adminSpecialRoutes {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	finalCoreRequests, _ := finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog()
	for key, schema := range finalCoreRequests {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	finalAuthRoutes, _ := finalAuthIntegrationsCredentialsNotificationsUsersWebhooksSchemaCatalog()
	for key, schema := range finalAuthRoutes {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	onboardingProposalRoutes, _ := onboardingProposalSchemaCatalog()
	for key, schema := range onboardingProposalRoutes {
		result[key] = mergeDomainSchema(result[key], schema)
	}
	return result
}

func mergeDomainSchema(existing, incoming DomainSchema) DomainSchema {
	if incoming.Request != nil {
		existing.Request = incoming.Request
	}
	if incoming.RequestMedia != nil {
		existing.RequestMedia = incoming.RequestMedia
	}
	if incoming.Response != nil {
		existing.Response = incoming.Response
	}
	if incoming.ResponseMedia != nil {
		existing.ResponseMedia = incoming.ResponseMedia
	}
	if incoming.SuccessStatuses != nil {
		existing.SuccessStatuses = incoming.SuccessStatuses
	}
	return existing
}

func requestBodyForRoute(rt route) map[string]any {
	request := requestSchema(rt)
	media := []string{"application/json"}
	if schema, ok := routeSchemaCatalog()[rt.method+" "+rt.path]; ok && schema.RequestMedia != nil {
		media = schema.RequestMedia
	}
	content := make(map[string]any, len(media))
	for _, mediaType := range media {
		content[mediaType] = map[string]any{"schema": request}
	}
	return map[string]any{"content": content}
}

func remainingAuthIntegrationsSchemaCatalogRoutes() map[string]DomainSchema {
	routes, _ := remainingAuthIntegrationsSchemaCatalog()
	return routes
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

// object builds an object schema. The variadic `required` is not decoration:
// a response schema that names its properties but not its required ones
// accepts a body that shares no field name with what the server sends, so the
// contract gate certifies the drift instead of catching it (see
// docs/prd/response-shape-contract.md).
//
// The list is not written from memory. TestOpenAPIRequired_MatchesTheStructsOwnJSONTags
// in internal/api derives it from the response struct's json tags — a field
// without `,omitempty` is emitted on every response and belongs here — and
// fails naming the exact fields when the two disagree.
func object(properties map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
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
		// Session provenance: omitted entirely for runs that recorded none
		// (older runs, adapters with no session-init), which is why every one
		// of these is nullable rather than a guaranteed scalar.
		"cli_version": nullable("string"), "api_key_source": nullable("string"),
		"permission_mode": nullable("string"), "session_id": nullable("string"),
		"mcp_server_errors": array(ref("MCPServerError")),
		// The list above is what the record could identify and is capped; these
		// two say what it leaves out, and a client that renders the list without
		// them shows a partial answer as a complete one.
		"mcp_server_error_count":      scalar("integer"),
		"mcp_server_errors_truncated": scalar("boolean"),
		// Tool names and refusal counts only — the denied input never reaches
		// the run record. The count separates an agent that tried once from one
		// hammering a wall it cannot see.
		"permission_denials":           array(ref("DeniedTool")),
		"permission_denials_truncated": scalar("boolean"),
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
		"MCPServerError": object(stringProps("name", "type", "message")),
		// tool_name carries the failure CATEGORY when the CLI named no tool, so
		// a refusal nobody could name still renders. count is absent on records
		// written before the tally existed — it is never a "denied zero times".
		"DeniedTool": object(map[string]any{"tool_name": scalar("string"), "count": scalar("integer")}),
		"RunStats":   object(map[string]any{"running": scalar("integer"), "today": scalar("integer"), "failed": scalar("integer")}),
		"Pagination": object(map[string]any{"page": scalar("integer"), "limit": scalar("integer"), "total": scalar("integer"), "total_pages": scalar("integer")}),
	}
	return map[string]any{"schemas": schemas}
}

// errorBodySchema picks the envelope an operation's error responses actually
// carry. A handler that reaches for both gets a oneOf rather than a guess:
// over-narrowing the contract is worse than admitting the ambiguity, because a
// client generated from it would reject a response the server is entitled to
// send.
func errorBodySchema(info handlerInfo) map[string]any {
	switch {
	case info.repliesProblem && info.repliesError:
		return map[string]any{"oneOf": []any{errorSchema(), problemSchema()}}
	case info.repliesProblem:
		return problemSchema()
	default:
		return errorSchema()
	}
}

// errorSchema is the {"error": "…"} envelope replyError writes, and what the
// auth, role and workspace middleware write for the 401/403/400 they add to
// every wrapped operation.
func errorSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"error"}, "properties": map[string]any{
		"error": map[string]any{"type": "string"},
	}}
}

func problemSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"type", "title", "status", "detail"}, "properties": map[string]any{
		"type": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
		"status": map[string]any{"type": "integer"}, "detail": map[string]any{"type": "string"},
	}}
}

// An annotation is intentionally small and local to a route. Example:
//
//	// openapi: query page:integer limit:integer metric:string!; responses 200,400
//
// It is an escape hatch for handlers whose query parsing is delegated to a
// helper, or whose status is produced outside the handler body.
//
// Grammar: `query <field>...` where a field is `name`, `name:type`, or either
// with a trailing `!` marking the parameter required. Type defaults to string
// and must be one of integer, number, boolean, string — an unrecognised type
// drops the field rather than guessing. `responses <codes>` is a comma list.
// Both clauses are optional and separated by `;`.
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
			// A trailing `!` declares the parameter required: `metric:string!`,
			// or `metric!` when the type is left implicit. It is the escape
			// hatch #1824 asks for, for the handlers whose validation lives in
			// a helper the body scan cannot see — GET /api/v1/metrics/timeseries
			// rejects an absent ?metric inside parseTimeseriesParams, and
			// GET /api/v1/auth/pair/poll inside normalizePairingCode.
			//
			// It is a claim a human made after reading the handler, so it is
			// held to the same standard as an inferred one: every parameter
			// carrying it is pinned by
			// TestGeneratedSpecMarksExactlyTheVerifiedParametersRequired, and
			// each must have a test showing the 4xx on omission.
			required := strings.HasSuffix(field, "!")
			field = strings.TrimSuffix(field, "!")
			bits := strings.Split(field, ":")
			typ := "string"
			if len(bits) > 1 {
				typ = bits[1]
			}
			switch typ {
			case "integer", "number", "boolean", "string":
				// Keep any requiredness already inferred from the handler: the
				// annotation declares the type, and may add the obligation, but
				// never removes one the handler was seen to enforce.
				param := info.query[bits[0]]
				param.typ = typ
				param.required = param.required || required
				info.query[bits[0]] = param
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

// inboundRequestParamPattern matches a `*http.Request` parameter declaration —
// the handler's own request, or one taken by a closure declared inside it.
// This is the only way a request enters a handler from the outside; anything
// the handler builds itself (http.NewRequest, http.NewRequestWithContext) is
// outbound and carries no API query surface.
var inboundRequestParamPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s+\*http\.Request\b`)

// derivedRequestPattern matches a request derived from another request, as a
// whole statement: `stripped := r.Clone(r.Context())`, `scoped := r.WithContext(ctx)`,
// or a bare alias `req := r`. Provenance travels across these — journal Count
// reads its real `limit`/`cursor` off an r.Clone, so a rule that accepted only
// the literal `r` would drop them from the spec.
var derivedRequestPattern = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*([A-Za-z_][A-Za-z0-9_]*)(?:\.(?:Clone|WithContext)\([^\n]*\))?\s*$`)

// requestQueryPattern matches a query read taken straight off a request:
// r.URL.Query().Get("cursor") and its Has/Values siblings. The receiver is
// captured because `.URL.Query()` alone does not mean the API's query string —
// an outbound *http.Request has the same method.
var requestQueryPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.URL\.Query\(\)\.(?:Get|Has|Values)\(\s*"([^"]+)"`)

// queryValuesBindingPattern matches a local bound to a request's decoded query
// values — `q := r.URL.Query()` — so the far more common `q.Get("cursor")` two
// lines later is still recognised. The request receiver is captured for the
// same reason as above.
var queryValuesBindingPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*([A-Za-z_][A-Za-z0-9_]*)\.URL\.Query\(\)`)

// accessorPattern matches any `<ident>.Get|Has|Values("name")`. url.Values is
// not the only type with that shape: r.Header, r.Trailer, r.Form, r.PostForm
// and an http.Client read identically, so a match here is a query parameter
// only when queryValuesBindingPattern bound the receiver above. Unrecognised
// receivers fail safe — no parameter — because guessing wrong publishes, for
// example, `?Authorization=` to every agent that drives the API from the spec.
var accessorPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.(?:Get|Has|Values)\(\s*"([^"]+)"`)

// inboundRequests returns the identifiers that hold the request the server was
// handed, plus every request derived from one. Provenance is a whitelist on
// purpose: an identifier of unknown origin is not an inbound request, so it
// contributes nothing rather than being guessed into the published contract.
func inboundRequests(signature, body string) map[string]bool {
	inbound := map[string]bool{}
	for _, decl := range []string{signature, body} {
		for _, m := range inboundRequestParamPattern.FindAllStringSubmatch(decl, -1) {
			inbound[m[1]] = true
		}
	}
	// Derivations chain (`a := r.Clone(...)`, then `b := a`), so propagate to a
	// fixed point rather than one hop.
	for changed := true; changed; {
		changed = false
		for _, m := range derivedRequestPattern.FindAllStringSubmatch(body, -1) {
			if inbound[m[2]] && !inbound[m[1]] {
				inbound[m[1]] = true
				changed = true
			}
		}
	}
	return inbound
}

// boundQueryValues returns the identifiers holding a request's decoded query
// values — the receivers for which `x.Get("n")` means a query parameter.
func boundQueryValues(inbound map[string]bool, body string) map[string]bool {
	bound := map[string]bool{}
	for _, m := range queryValuesBindingPattern.FindAllStringSubmatch(body, -1) {
		if inbound[m[2]] {
			bound[m[1]] = true
		}
	}
	return bound
}

// queryParamNames returns the query parameter names a handler reads off the
// inbound request. signature is the handler's parameter list, body its source.
func queryParamNames(signature, body string) []string {
	inbound := inboundRequests(signature, body)
	bound := boundQueryValues(inbound, body)
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, m := range requestQueryPattern.FindAllStringSubmatch(body, -1) {
		if inbound[m[1]] {
			add(m[2])
		}
	}
	for _, m := range accessorPattern.FindAllStringSubmatch(body, -1) {
		if bound[m[1]] {
			add(m[2])
		}
	}
	return names
}

// queryReadAssignmentPattern matches a query read bound to a local at the top
// level of a handler body: `filePath := r.URL.Query().Get("path")` or, when the
// decoded values were bound first, `since := qs.Get("since")`. The receiver is
// captured so the caller can check it is really the inbound request's query.
// One leading tab, and the read as the whole right-hand side, is deliberate —
// see requiredQueryParams.
var queryReadAssignmentPattern = regexp.MustCompile(
	`^\t([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*(.+?)\s*$`)

// bareQueryReadPattern matches the read itself, once any emptiness-preserving
// wrapper has been peeled off it.
var bareQueryReadPattern = regexp.MustCompile(
	`^([A-Za-z_][A-Za-z0-9_]*)(\.URL\.Query\(\))?\.Get\(\s*"([^"]+)"\s*\)$`)

// emptinessPreservingWrappers are the string functions for which f("") == ""
// holds by definition, so a value wrapped in them is empty exactly when the
// query parameter was absent. That equivalence is the whole reason the
// requiredness rule may look through them.
//
// The list is a whitelist and stays one. `code := normalizePairingCode(
// r.URL.Query().Get("code"))` in cli_pair.go is followed by a textbook
// reject-on-empty guard and ?code genuinely is required — but only because
// that helper happens to return "" for a code of the wrong length. A wrapper
// that returned a fallback instead would make the guard unreachable and the
// parameter optional, and marking it required would tell a client a request
// will fail when it will succeed. Establishing which of the two a given helper
// is means reading its body, i.e. the interprocedural analysis this generator
// deliberately does not do; the `!` annotation is the escape hatch for it.
var emptinessPreservingWrappers = []string{
	"strings.TrimSpace(",
	"strings.ToLower(",
	"strings.ToUpper(",
}

// unwrapEmptinessPreserving peels those wrappers off an expression, innermost
// last: `strings.ToLower(strings.TrimSpace(r.URL.Query().Get("x")))` becomes
// `r.URL.Query().Get("x")`. An expression wrapped in anything else is returned
// as-is, and then fails to match the bare read — no parameter, which is the
// safe direction.
func unwrapEmptinessPreserving(expr string) string {
	for peeled := true; peeled; {
		peeled = false
		for _, wrapper := range emptinessPreservingWrappers {
			if strings.HasPrefix(expr, wrapper) && strings.HasSuffix(expr, ")") {
				expr = strings.TrimSpace(expr[len(wrapper) : len(expr)-1])
				peeled = true
				break
			}
		}
	}
	return expr
}

// emptyCheckPattern matches the emptiness test itself, in the two spellings
// this codebase uses.
func emptyCheckPattern(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(`(?:\b` + q + `\s*==\s*""|TrimSpace\(\s*` + q + `\s*\)\s*==\s*"")`)
}

// requiredQueryParams returns the query parameters the handler rejects the
// request for omitting — the ones a client must send.
//
// The rule is deliberately one narrow shape. Both halves must sit at the top
// level of the handler body (a single leading tab, which gofmt guarantees), so
// that both are reached unconditionally:
//
//	v := r.URL.Query().Get("name")
//	…
//	if v == "" {            // or strings.TrimSpace(v) == "", or `v == "" || w == ""`
//	    <4xx write>
//	    return
//	}
//
// The read may be wrapped in an emptiness-preserving string function —
// `toolkit := strings.TrimSpace(r.URL.Query().Get("toolkit"))` — because f("")
// is "" for every entry in emptinessPreservingWrappers, so the guard still
// fires exactly when the parameter was absent. Any other wrapper is refused:
// see the comment on that list.
//
// A `||` chain marks every emptiness-tested parameter in it, because any one
// of them being absent produces the 4xx on its own. It never fires when the
// guard defaults the value (`if v == "" { v = "7d" }`), when the emptiness
// test is only one of several conditions that must ALL hold (`&&`), when the
// guard has an else branch, or when either half sits inside a nested block —
// where "missing" may only be fatal on some other condition.
//
// Everything it does not recognise stays optional. That asymmetry is the whole
// design: under-claiming leaves a client where it already was, while
// over-claiming tells it a request will fail when it will succeed, and a
// generated client turns that into a mandatory argument its caller cannot omit.
// AuthMiddleware.RequireWorkspace is the case that shows why the shape has to
// be this tight — it reads ?workspace_id and 400s on empty, but only after
// falling back to the path segment and the X-Workspace-ID header, so the
// parameter is not required at all. Its guard assigns rather than replies, and
// this rule does not fire on it.
func requiredQueryParams(signature, body string) map[string]bool {
	inbound := inboundRequests(signature, body)
	bound := boundQueryValues(inbound, body)
	lines := strings.Split(body, "\n")

	// Locals holding a query read taken unconditionally at the top level.
	reads := map[string]string{}
	for _, line := range lines {
		m := queryReadAssignmentPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		read := bareQueryReadPattern.FindStringSubmatch(unwrapEmptinessPreserving(m[2]))
		if read == nil {
			continue
		}
		local, receiver, viaURL, name := m[1], read[1], read[2] != "", read[3]
		// `r.URL.Query().Get(…)` must come off an inbound request; a bare
		// `x.Get(…)` only counts when x was bound to one's decoded values.
		// Anything else (r.Header, an outbound request, a plain url.Values)
		// is not a query parameter — the same provenance rule queryParamNames
		// applies, for the same reason.
		if viaURL != inbound[receiver] || (!viaURL && !bound[receiver]) {
			continue
		}
		reads[local] = name
	}

	required := map[string]bool{}
	for i, line := range lines {
		if !strings.HasPrefix(line, "\tif ") || strings.Contains(line, "&&") {
			continue
		}
		block, closed := guardBlock(lines, i)
		if !closed || !strings.Contains(block, "return") || !rejects4xx(block) {
			continue
		}
		for local, name := range reads {
			if !emptyCheckPattern(local).MatchString(line) {
				continue
			}
			if reassigned(lines, local) {
				continue
			}
			required[name] = true
		}
	}
	return required
}

// reassigned reports whether a local that was read from the query string is
// written again anywhere in the handler. Any second source — a default
// (`if v == "" { v = "7d" }`) or a fallback chain (query, then path segment,
// then header, as AuthMiddleware.RequireWorkspace does for workspace_id) —
// means a later 4xx guard does not prove the query parameter was required.
func reassigned(lines []string, local string) bool {
	pattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(local) + `\s*=[^=]`)
	for _, line := range lines {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// guardBlock returns the body of the top-level `if` opening at lines[start],
// and whether it closed with a plain `}` — an `} else {` means the empty case
// is handled rather than rejected.
func guardBlock(lines []string, start int) (string, bool) {
	var block []string
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "\t}" {
			return strings.Join(block, "\n"), true
		}
		if strings.HasPrefix(lines[i], "\t}") {
			return "", false
		}
		block = append(block, lines[i])
	}
	return "", false
}

// rejects4xx reports whether a guard body answers with a 4xx status.
func rejects4xx(block string) bool {
	for name, code := range statusNames {
		if code[0] == '4' && strings.Contains(block, "http."+name) {
			return true
		}
	}
	for _, m := range inlineStatusPattern.FindAllStringSubmatch(block, -1) {
		if m[1][0] == '4' {
			return true
		}
	}
	return false
}

// handlerTarget names a handler the generator resolved from a registration: a
// method on a concrete type, or (typeName empty) a package-level function.
type handlerTarget struct {
	typeName string
	method   string
}

// inlineHandler is a function literal registered directly as the handler. Its
// body is the handler body, so it needs no lookup at all.
type inlineHandler struct {
	signature string
	body      string
}

// unwrapHandlerArg peels the middleware wrappers a registration puts around
// its handler — authed(...), wsCtx(...), http.HandlerFunc(...),
// r.authMw.OptionalWorkspaceRole(...) — down to the handler expression. Only
// single-argument calls are peeled; anything else is already the handler (or
// is not one).
func unwrapHandlerArg(e ast.Expr) ast.Expr {
	for i := 0; i < 8; i++ {
		call, ok := e.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return e
		}
		if _, literal := call.Args[0].(*ast.BasicLit); literal {
			return e
		}
		e = call.Args[0]
	}
	return e
}

// typeNameOf resolves the expression a handler method is called on to the
// concrete type declaring it, using the registering file as the scope:
//
//	audit.List                          -> `audit := NewAuditHandler(...)`   -> AuditHandler
//	NewAdminKeeperHealthHandler(l).Get  -> constructor call                  -> AdminKeeperHealthHandler
//	NewSystemHandler(...).WithBuild(b)  -> builder chain, resolve the head   -> SystemHandler
//	r.steerHandler.Steer                -> `r.steerHandler = NewSteerHandler(...)` -> SteerHandler
//
// An empty result means unresolved, and callers must then infer nothing.
func typeNameOf(e ast.Expr, src string) string {
	switch b := e.(type) {
	case *ast.CallExpr:
		switch fun := b.Fun.(type) {
		case *ast.Ident:
			// A constructor, and only a constructor: any other function's
			// name is not a type name, and guessing one resolves to nothing
			// at best and to the wrong handler at worst.
			if strings.HasPrefix(fun.Name, "New") && len(fun.Name) > 3 {
				return fun.Name[3:]
			}
		case *ast.SelectorExpr:
			return typeNameOf(fun.X, src)
		}
	case *ast.Ident:
		return declaredTypeName(b.Name, src)
	case *ast.SelectorExpr:
		if recv, ok := b.X.(*ast.Ident); ok {
			return fieldTypeName(recv.Name, b.Sel.Name, src)
		}
	case *ast.UnaryExpr:
		return typeNameOf(b.X, src)
	case *ast.CompositeLit:
		if id, ok := b.Type.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

func declaredTypeName(name, src string) string {
	m := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*:=\s*(?:&?)([A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(src)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimPrefix(m[1], "New")
}

// fieldTypeName resolves `r.steerHandler` through the assignment that builds
// it, then through the struct field declaration if the assignment lives in
// another file.
func fieldTypeName(recv, field, src string) string {
	assign := regexp.MustCompile(`\b` + regexp.QuoteMeta(recv) + `\.` + regexp.QuoteMeta(field) + `\s*=\s*(?:&?)([A-Za-z_][A-Za-z0-9_]*)`)
	decl := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(field) + `\s+\*([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	for _, scope := range []string{src, packageSource()} {
		if m := assign.FindStringSubmatch(scope); len(m) == 2 {
			if name := strings.TrimPrefix(m[1], "New"); name != "" {
				return name
			}
		}
		if m := decl.FindStringSubmatch(scope); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// requestParamNames returns the names a function literal gives its inbound
// *http.Request, skipping the blank identifier — a handler written
// `func(w http.ResponseWriter, _ *http.Request)` cannot read a query string.
func requestParamNames(fn *ast.FuncType) []string {
	var names []string
	for _, field := range fn.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Request" {
			continue
		}
		for _, name := range field.Names {
			if name.Name != "_" {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// delegatedTargets finds the handler a registered closure forwards to —
// `NewSystemHandler(...).WithBuild(r.build).Version(w, req)` — recognised by
// the closure passing its own request along. Without this the closure-wrapped
// routes would resolve to nothing and document only their middleware statuses.
func delegatedTargets(lit *ast.FuncLit, src string) []handlerTarget {
	requests := map[string]bool{}
	for _, name := range requestParamNames(lit.Type) {
		requests[name] = true
	}
	if len(requests) == 0 {
		return nil
	}
	var targets []handlerTarget
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		forwarded := false
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && requests[id.Name] {
				forwarded = true
			}
		}
		if !forwarded {
			return true
		}
		if typeName := typeNameOf(sel.X, src); typeName != "" {
			targets = append(targets, handlerTarget{typeName: typeName, method: sel.Sel.Name})
		}
		return true
	})
	return targets
}

// resolveHandlerRefs parses a route registration and returns the handler it
// registers. ok is false when the registration resolves to nothing — and that
// is the point: the previous fallback matched every same-named function in
// internal/api (test helpers included) and merged their query parameters, so
// five operations published the union of all 95 query parameters in the
// package, including GET /api/health, which reads none. An unresolved
// registration must document nothing rather than everything; the `// openapi:`
// annotation is the escape hatch when a route genuinely needs more.
func resolveHandlerRefs(call, src string) (inline []inlineHandler, targets []handlerTarget, ok bool) {
	fset := token.NewFileSet()
	expr, err := parser.ParseExprFrom(fset, "registration.go", []byte(call), 0)
	if err != nil {
		return nil, nil, false
	}
	callExpr, isCall := expr.(*ast.CallExpr)
	if !isCall {
		return nil, nil, false
	}
	offset := func(p token.Pos) int { return fset.Position(p).Offset }
	for i := len(callExpr.Args) - 1; i >= 0; i-- {
		switch handler := unwrapHandlerArg(callExpr.Args[i]).(type) {
		case *ast.FuncLit:
			start, end := offset(handler.Body.Lbrace), offset(handler.Body.Rbrace)
			if start < 0 || end+1 > len(call) || start >= end {
				return nil, nil, false
			}
			inline = append(inline, inlineHandler{
				signature: call[offset(handler.Type.Pos()):start],
				body:      call[start : end+1],
			})
			return inline, delegatedTargets(handler, src), true
		case *ast.SelectorExpr:
			typeName := typeNameOf(handler.X, src)
			if typeName == "" {
				return nil, nil, false
			}
			return nil, []handlerTarget{{typeName: typeName, method: handler.Sel.Name}}, true
		case *ast.Ident:
			return nil, []handlerTarget{{method: handler.Name}}, true
		}
	}
	return nil, nil, false
}

// routeRegistrationCall matches a route registration on the router, in any of
// the five call shapes this codebase uses. It is deliberately looser than
// combinedPattern / splitPattern — no literal method or path — because its
// only job is to decide whether a FILE registers routes at all.
//
// #1953: route discovery used to be `router_*.go`, so a registrar with any
// other filename contributed nothing to the spec, silently. Two invariants
// were built on that glob; the other one (internal/api's
// route_authz_invariant_test.go) is the security-relevant half, and
// internal/api/pages_internal.go was the file both of them could not see.
var routeRegistrationCall = regexp.MustCompile(`\.(?:mux\.Handle|mux\.HandleFunc|authedMut|authedSelfMut|authedAdmin)\(`)

// routeSourceFiles lists the non-test Go files in routerDir that register at
// least one route. A file that registers none is skipped only because it has
// nothing to contribute, never because of what it is called.
func routeSourceFiles() ([]string, error) {
	var files []string
	for _, file := range sourceFiles() {
		data, err := os.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		if routeRegistrationCall.Match(data) {
			files = append(files, file)
		}
	}
	return files, nil
}

// sourceFiles lists the package's non-test Go files. _test.go is excluded
// unconditionally: a test helper is never a handler, and merging one's query
// reads into a published operation documents parameters no caller can send.
func sourceFiles() []string {
	if cachedSourceFiles != nil {
		return cachedSourceFiles
	}
	all, _ := filepath.Glob(filepath.Join(routerDir, "*.go"))
	for _, file := range all {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		cachedSourceFiles = append(cachedSourceFiles, file)
	}
	return cachedSourceFiles
}

var (
	cachedSourceFiles []string
	cachedSources     = map[string]string{}
	cachedPackageSrc  string
)

func readSource(path string) string {
	if src, ok := cachedSources[path]; ok {
		return src
	}
	data, err := os.ReadFile(path)
	if err != nil {
		cachedSources[path] = ""
		return ""
	}
	cachedSources[path] = string(data)
	return cachedSources[path]
}

// packageSource is every non-test file concatenated — the scope for a lookup
// that can legitimately cross files, such as a Router field declared in
// router.go and assigned in router_orchestration.go.
func packageSource() string {
	if cachedPackageSrc != "" {
		return cachedPackageSrc
	}
	var b strings.Builder
	for _, file := range sourceFiles() {
		b.WriteString(readSource(file))
		b.WriteString("\n")
	}
	cachedPackageSrc = b.String()
	return cachedPackageSrc
}

var inlineStatusPattern = regexp.MustCompile(`(?:writeJSON|WriteHeader)\([^\n]*?\b(\d{3})\b`)

// absorbHandlerBody folds one handler body's query reads and status branches
// into the operation being built.
func absorbHandlerBody(info *handlerInfo, signature, body string) {
	required := requiredQueryParams(signature, body)
	for _, name := range queryParamNames(signature, body) {
		param := info.query[name]
		if param.typ == "" {
			param.typ = "string"
		}
		if required[name] {
			param.required = true
		}
		info.query[name] = param
	}
	if strings.Contains(body, "replyError(") {
		info.repliesError = true
	}
	if strings.Contains(body, "writeProblem(") || strings.Contains(body, "internalError(") {
		info.repliesProblem = true
	}
	for name, code := range statusNames {
		if strings.Contains(body, "http."+name) {
			info.statuses[code] = true
		}
	}
	for _, n := range inlineStatusPattern.FindAllStringSubmatch(body, -1) {
		info.statuses[n[1]] = true
	}
}

func inferHandlerInfo(rt route) handlerInfo {
	info := handlerInfo{query: map[string]queryParam{}, statuses: map[string]bool{"200": true}}
	src := readSource(rt.source)
	if src == "" {
		return info
	}
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

	inline, targets, ok := resolveHandlerRefs(rt.call, src)
	if !ok {
		return info
	}
	for _, handler := range inline {
		absorbHandlerBody(&info, handler.signature, handler.body)
	}
	for _, target := range targets {
		pattern := regexp.MustCompile(`(?m)^func\s+` + regexp.QuoteMeta(target.method) + `\s*\(`)
		if target.typeName != "" {
			pattern = regexp.MustCompile(`func\s+\([^)]*\*` + regexp.QuoteMeta(target.typeName) + `\)\s*` + regexp.QuoteMeta(target.method) + `\s*\(`)
		}
		for _, file := range sourceFiles() {
			candidate := readSource(file)
			for _, m := range pattern.FindAllStringIndex(candidate, -1) {
				body := functionBody(candidate, m[1])
				if body == "" {
					continue
				}
				absorbHandlerBody(&info, functionSignature(candidate, m[1]), body)
			}
		}
	}
	return info
}

// functionSignature returns the parameter list between `from` — the index just
// past a function's opening parenthesis — and the brace that opens its body.
// That text is where the inbound `*http.Request` is declared.
func functionSignature(src string, from int) string {
	open := strings.IndexByte(src[from:], '{')
	if open < 0 {
		return ""
	}
	return src[from : from+open]
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
