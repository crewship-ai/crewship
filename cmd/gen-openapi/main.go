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
// It deliberately does NOT infer request/response body schemas from the
// handler's readJSON/writeJSON calls — that would need real static analysis
// (go/types, not regexp) across ~530 handlers and is out of scope for this
// pass. Every operation gets a generic `object` request/response schema.
// That's enough for path/method-level API discovery and tools like
// schemathesis to start fuzzing against; it is NOT a substitute for
// hand-written per-endpoint schemas if this ever needs to be a contract
// consumers code-generate clients from.
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
						"application/json": map[string]any{"schema": genericSchema},
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
				"Paths, methods, and path parameters are exact; request/response bodies are a generic " +
				"object placeholder, not hand-authored per-endpoint schemas — see cmd/gen-openapi/main.go.",
			"version": "generated",
		},
		"paths": paths,
	}
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
