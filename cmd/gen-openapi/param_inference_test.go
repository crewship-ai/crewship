package main

import (
	"sort"
	"strings"
	"testing"
)

// routerSource is the surrounding file text the resolver reads identifiers
// against — the declarations that give a receiver its concrete type.
const routerSource = `
func (r *Router) register() {
	audit := NewAuditHandler(r.db, r.logger)
	system := NewSystemHandler(r.logger, r.version).WithActiveContainer(r.activeContainer())
	r.steerHandler = NewSteerHandler(r.db, r.steerer, r.logger)
}
`

func TestResolveHandlerRefsResolvesEveryRegistrationShape(t *testing.T) {
	cases := []struct {
		name       string
		call       string
		wantTarget handlerTarget
	}{{
		name:       "method value on a constructed local",
		call:       `r.authedAdmin("GET", "/api/v1/audit", audit.List)`,
		wantTarget: handlerTarget{typeName: "AuditHandler", method: "List"},
	}, {
		name:       "wrapped in middleware and http.HandlerFunc",
		call:       `r.mux.Handle("GET /api/v1/system/runtime", authed(r.authMw.OptionalWorkspaceRole(http.HandlerFunc(system.Runtime))))`,
		wantTarget: handlerTarget{typeName: "SystemHandler", method: "Runtime"},
	}, {
		name:       "constructor called inline",
		call:       `r.authedAdmin("GET", "/api/v1/admin/keeper/health", NewAdminKeeperHealthHandler(r.logger).Get)`,
		wantTarget: handlerTarget{typeName: "AdminKeeperHealthHandler", method: "Get"},
	}, {
		name:       "field on the router",
		call:       `r.authedSelfMut("POST", "/api/v1/chats/{chatId}/steer", r.steerHandler.Steer)`,
		wantTarget: handlerTarget{typeName: "SteerHandler", method: "Steer"},
	}, {
		name:       "closure delegating to a builder chain",
		call:       "r.mux.Handle(\"GET /api/v1/system/version\", authed(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {\n\t\tNewSystemHandler(r.logger, r.version).WithBuild(r.build).Version(w, req)\n\t})))",
		wantTarget: handlerTarget{typeName: "SystemHandler", method: "Version"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, targets, ok := resolveHandlerRefs(tc.call, routerSource)
			if !ok {
				t.Fatalf("resolveHandlerRefs(%s) did not resolve", tc.name)
			}
			found := false
			for _, target := range targets {
				if target == tc.wantTarget {
					found = true
				}
			}
			if !found {
				t.Fatalf("targets = %+v, want %+v", targets, tc.wantTarget)
			}
		})
	}
}

// The inline closures that read nothing must resolve to a handler body — not
// to a name match across the package — and that body must yield no parameters.
func TestResolveHandlerRefsReadsAnInlineClosureAsTheHandler(t *testing.T) {
	call := "r.mux.HandleFunc(\"GET /api/health\", func(w http.ResponseWriter, _ *http.Request) {\n\t\twriteJSON(w, http.StatusOK, map[string]string{\"status\": \"ok\"})\n\t})"
	inline, targets, ok := resolveHandlerRefs(call, routerSource)
	if !ok || len(inline) != 1 {
		t.Fatalf("resolveHandlerRefs = %v, %v, %v; want one inline handler", inline, targets, ok)
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v; a closure taking `_ *http.Request` forwards to nothing", targets)
	}
	if got := queryParamNames(inline[0].signature, inline[0].body); len(got) != 0 {
		t.Errorf("query parameters = %v, want none", got)
	}
	if !strings.Contains(inline[0].body, "http.StatusOK") {
		t.Errorf("inline body did not capture the handler: %q", inline[0].body)
	}
}

// The fail-closed property. A registration the generator cannot resolve must
// contribute no parameters at all — the old fallback matched every function
// with the same name anywhere in internal/api and merged their query reads,
// which is how GET /api/health came to advertise `assignee_id`.
func TestResolveHandlerRefsFailsClosedOnAnUnresolvedRegistration(t *testing.T) {
	for _, call := range []string{
		`r.authedAdmin("GET", "/api/v1/mystery", mystery.Get)`,
		`r.mux.Handle("GET /api/v1/mystery", authed(handlerFromSomewhere(r.db).Serve))`,
		`r.mux.Handle("GET /api/v1/mystery"`,
	} {
		inline, targets, ok := resolveHandlerRefs(call, routerSource)
		if ok || len(inline) != 0 || len(targets) != 0 {
			t.Errorf("resolveHandlerRefs(%q) = %v, %+v, %v; want no handler at all", call, inline, targets, ok)
		}
	}
}

// A test helper is never a handler. Merging one's query reads into a published
// operation documents parameters no caller can send.
func TestSourceFilesExcludeTests(t *testing.T) {
	reset := func(dir string) {
		routerDir = dir
		cachedSourceFiles, cachedSources, cachedPackageSrc = nil, map[string]string{}, ""
	}
	t.Cleanup(func() { reset("internal/api") })
	reset("../../internal/api")

	files := sourceFiles()
	if len(files) == 0 {
		t.Fatal("no source files found — the assertion below would be vacuous")
	}
	tests := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			tests++
		}
	}
	if tests > 0 {
		t.Errorf("%d of %d scanned files are _test.go files", tests, len(files))
	}
}

// requiredQueryParams is the #1824 rule. These fixtures are the shapes
// internal/api actually contains; the negative cases are the ones that make
// over-claiming possible, and each must stay optional.
func TestRequiredQueryParamsRecognisesOnlyRejectOnEmpty(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{{
		name: "read then reject",
		body: "{\n" +
			"\tfilePath := r.URL.Query().Get(\"path\")\n" +
			"\tif filePath == \"\" {\n" +
			"\t\treplyError(w, http.StatusBadRequest, \"path parameter required\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: []string{"path"},
	}, {
		name: "rejected further down, after an unrelated gate",
		body: "{\n" +
			"\tfilePath := r.URL.Query().Get(\"path\")\n" +
			"\tif !canRole(role, \"read\") {\n" +
			"\t\treplyError(w, http.StatusForbidden, \"Forbidden\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"\tif filePath == \"\" {\n" +
			"\t\treplyError(w, http.StatusBadRequest, \"path parameter required\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: []string{"path"},
	}, {
		name: "either one missing is fatal, so both are required",
		body: "{\n" +
			"\tfromStr := r.URL.Query().Get(\"from\")\n" +
			"\ttoStr := r.URL.Query().Get(\"to\")\n" +
			"\tif fromStr == \"\" || toStr == \"\" {\n" +
			"\t\treplyError(w, http.StatusBadRequest, \"from and to are required\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: []string{"from", "to"},
	}, {
		name: "trimmed emptiness check via bound query values",
		body: "{\n" +
			"\tqs := r.URL.Query()\n" +
			"\tname := qs.Get(\"name\")\n" +
			"\tif strings.TrimSpace(name) == \"\" {\n" +
			"\t\twriteJSON(w, 422, map[string]string{\"error\": \"name required\"})\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: []string{"name"},
	}, {
		name: "the guard supplies a default",
		body: "{\n" +
			"\twindow := r.URL.Query().Get(\"range\")\n" +
			"\tif window == \"\" {\n" +
			"\t\twindow = \"7d\"\n" +
			"\t}\n" +
			"}",
		want: nil,
	}, {
		// AuthMiddleware.RequireWorkspace: reads ?workspace_id and 400s on
		// empty, but only after the path segment and the X-Workspace-ID
		// header. The parameter is not required at all.
		name: "fallback chain, then reject",
		body: "{\n" +
			"\tworkspaceID := r.URL.Query().Get(\"workspace_id\")\n" +
			"\tif workspaceID == \"\" {\n" +
			"\t\tworkspaceID = r.PathValue(\"workspaceId\")\n" +
			"\t}\n" +
			"\tif workspaceID == \"\" {\n" +
			"\t\tworkspaceID = r.Header.Get(\"X-Workspace-ID\")\n" +
			"\t}\n" +
			"\tif workspaceID == \"\" {\n" +
			"\t\treplyError(w, http.StatusBadRequest, \"workspace_id is required\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: nil,
	}, {
		name: "only fatal in combination",
		body: "{\n" +
			"\tid := r.URL.Query().Get(\"id\")\n" +
			"\tif id == \"\" && slug == \"\" {\n" +
			"\t\treplyError(w, http.StatusBadRequest, \"id or slug required\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: nil,
	}, {
		name: "empty is answered, not rejected",
		body: "{\n" +
			"\tcursor := r.URL.Query().Get(\"cursor\")\n" +
			"\tif cursor == \"\" {\n" +
			"\t\twriteJSON(w, http.StatusOK, map[string]any{\"data\": nil})\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: nil,
	}, {
		name: "read inside a nested block, where missing may only matter there",
		body: "{\n" +
			"\tif mode == \"scoped\" {\n" +
			"\t\tscope := r.URL.Query().Get(\"scope\")\n" +
			"\t\tif scope == \"\" {\n" +
			"\t\t\treplyError(w, http.StatusBadRequest, \"scope required\")\n" +
			"\t\t\treturn\n" +
			"\t\t}\n" +
			"\t}\n" +
			"}",
		want: nil,
	}, {
		name: "a header is not a query parameter",
		body: "{\n" +
			"\ttoken := r.Header.Get(\"Authorization\")\n" +
			"\tif token == \"\" {\n" +
			"\t\treplyError(w, http.StatusUnauthorized, \"unauthorized\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: nil,
	}, {
		name: "an outbound request is not the API's query surface",
		body: "{\n" +
			"\tupstream, _ := http.NewRequest(\"GET\", target, nil)\n" +
			"\tnonce := upstream.URL.Query().Get(\"probe_nonce\")\n" +
			"\tif nonce == \"\" {\n" +
			"\t\treplyError(w, http.StatusBadRequest, \"probe_nonce required\")\n" +
			"\t\treturn\n" +
			"\t}\n" +
			"}",
		want: nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make([]string, 0, 2)
			for name := range requiredQueryParams(handlerSignature, tc.body) {
				got = append(got, name)
			}
			sort.Strings(got)
			want := append([]string{}, tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("requiredQueryParams = %v, want %v", got, tc.want)
			}
		})
	}
}

// routeAnnotation attaches an annotation to the route below it, not to the
// next several. The byte-window lookup it replaced gave /api/v1/admin/stats,
// /admin/users and /admin/workspaces the audit log's ten query filters.
func TestRouteAnnotationAppliesOnlyToTheRouteItPrecedes(t *testing.T) {
	src := "func (r *Router) register() {\n" +
		"\taudit := NewAuditHandler(r.db, r.logger)\n" +
		"\t// openapi: query page:integer limit:integer; responses 200,400\n" +
		"\tr.authedAdmin(\"GET\", \"/api/v1/audit\", audit.List)\n" +
		"\n" +
		"\t// Admin\n" +
		"\tadmin := NewAdminHandler(r.db, r.logger)\n" +
		"\tr.authedAdmin(\"GET\", \"/api/v1/admin/stats\", admin.Stats)\n" +
		"}\n"

	annotated := strings.Index(src, `r.authedAdmin("GET", "/api/v1/audit"`)
	if got := routeAnnotation(src, annotated); !strings.Contains(got, "page:integer") {
		t.Errorf("annotated route lost its annotation: %q", got)
	}
	neighbour := strings.Index(src, `r.authedAdmin("GET", "/api/v1/admin/stats"`)
	if got := routeAnnotation(src, neighbour); got != "" {
		t.Errorf("annotation bled onto the next route: %q", got)
	}
}
