package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Operation prose is generated alongside schemas, not patched into the artifact.
// Curated descriptions cover workflows where a path alone misses the intent.
// The fallback deliberately describes HTTP/resource shape without inventing
// lifecycle, permissions, or completion guarantees.
func operationMetadata(rt route) (string, string, bool) {
	key := rt.method + " " + rt.path
	curated := map[string][2]string{
		"GET /api/v1/agents":                                         {"List agents", "List agents visible to the authenticated caller in the selected workspace."},
		"POST /api/v1/agents":                                        {"Create agent", "Create an agent from the supplied configuration. Inspect the request schema for required fields."},
		"POST /api/v1/agents/hire":                                   {"Hire agent", "Hire an agent using the supplied hiring configuration."},
		"GET /api/v1/crews":                                          {"List crews", "List crews visible to the caller in the selected workspace."},
		"GET /api/v1/workspaces/{workspaceId}/pipelines":             {"List routines", "List saved pipeline definitions (routines) in the selected workspace."},
		"POST /api/v1/crews/{crewId}/missions":                       {"Create mission", "Create a mission for the selected crew. Inspect the response for its identifier before scheduling work."},
		"GET /api/v1/runs":                                           {"List runs", "List recorded agent runs visible to the caller; inspect individual runs for status and diagnostics."},
		"POST /api/v1/crews":                                         {"Create crew", "Create a crew in the selected workspace."},
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run": {"Start routine run", "Start a routine execution. The response is a run receipt; inspect run status separately to determine completion."},
		"POST /api/v1/conversations/search":                          {"Search conversations", "Search conversation transcripts within the caller's authorized scope. This POST is a read operation."},
		"POST /api/v1/memory/search/hybrid":                          {"Search memory", "Search authorized memory using hybrid retrieval. This POST reads memory and may call the configured embedding provider."},
		"POST /api/v1/automations/preview":                           {"Preview automation matches", "Evaluate an automation matcher against recent journal events without saving a rule or triggering an execution."},
	}
	read := rt.method == "GET" || rt.method == "HEAD" || rt.method == "OPTIONS"
	// Explicit audit: ConversationHandler.Search, MemoryHybridSearchHandler.Search,
	// AutomationHandler.PreviewMatch, OAuthHandler.Discover and the instance backup
	// PreviewContents/RestoreChecks handlers. CheckBundle is NOT read-only: it
	// updates the catalog proof. Never infer safety from names such as preview.
	switch key {
	case "POST /api/v1/oauth/discover", "POST /api/v1/admin/instance/backups/restore/checks", "POST /api/v1/admin/instance/backups/plans/preview-contents", "POST /api/v1/conversations/search", "POST /api/v1/memory/search/hybrid", "POST /api/v1/automations/preview":
		read = true
	}
	if v, ok := curated[key]; ok {
		return v[0], v[1], read
	}
	parts := strings.Split(strings.TrimPrefix(rt.path, "/api/v1/"), "/")
	words := []string{}
	for _, part := range parts {
		if !strings.HasPrefix(part, "{") {
			words = append(words, strings.ReplaceAll(part, "-", " "))
		}
	}
	noun := strings.Join(words, " ")
	action := "Invoke"
	switch rt.method {
	case "GET":
		action = "List"
		if strings.HasSuffix(rt.path, "}") {
			action = "Get"
		}
	case "HEAD":
		action = "Inspect"
	case "OPTIONS":
		action = "Inspect options for"
	case "DELETE":
		action = "Delete"
	case "PATCH", "PUT":
		action = "Update"
	case "POST":
		action = "Create"
		last := parts[len(parts)-1]
		if len(parts) > 1 && strings.HasPrefix(parts[len(parts)-2], "{") || last == "search" || last == "preview" {
			action = "Invoke"
		}
	}
	summary := action + " " + noun
	return summary, summary + " using " + rt.method + " " + openAPIPath(rt.path) + ". Inspect the parameters and request/response schemas for this operation's contract.", read
}

// Handler comments provide domain meaning beyond URL-derived fallback prose.
// Index once per generation; route resolution already knows receiver/method.
func handlerDescriptions() map[handlerTarget]string {
	docs := map[handlerTarget]string{}
	for _, file := range sourceFiles() {
		tree, err := parser.ParseFile(token.NewFileSet(), file, readSource(file), parser.ParseComments)
		if err != nil {
			continue
		}
		for _, declaration := range tree.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}
			target := handlerTarget{method: fn.Name.Name}
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				recv := fn.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				ident, ok := recv.(*ast.Ident)
				if !ok {
					continue
				}
				target.typeName = ident.Name
			}
			prose := strings.TrimSpace(fn.Doc.Text())
			// Keep the summary paragraphs, not long implementation listings.
			paragraphs := strings.Split(prose, "\n\n")
			if len(paragraphs) > 2 {
				paragraphs = paragraphs[:2]
			}
			prose = strings.Join(strings.Fields(strings.Join(paragraphs, " ")), " ")
			if len(prose) > 1000 {
				cut := strings.LastIndex(prose[:1000], " ")
				if cut > 0 {
					prose = prose[:cut] + "…"
				}
			}
			docs[target] = prose
		}
	}
	return docs
}
