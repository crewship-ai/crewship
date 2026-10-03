package main

import "strings"

// Operation prose is generated alongside schemas, not patched into the artifact.
// Curated descriptions cover workflows where a path alone misses the intent.
// The fallback deliberately describes HTTP/resource shape without inventing
// lifecycle, permissions, or completion guarantees.
func operationMetadata(rt route) (string, string, bool) {
	key := rt.method + " " + rt.path
	curated := map[string][2]string{
		"POST /api/v1/admin/instance/backups/restore/checks":         {"Check backup restore", "Check a backup restore request without performing the restore."},
		"POST /api/v1/admin/instance/backups/plans/preview-contents": {"Preview backup contents", "Preview the contents selected by a backup plan."},
		"POST /api/v1/oauth/discover":                                {"Discover OAuth endpoints", "Discover OAuth server metadata for the supplied provider."},
		"GET /api/health":                                            {"Get server health", "Check whether the server is responding."},
		"GET /api/v1/users/{id}/avatar":                              {"Get user avatar", "Retrieve the avatar for the selected user."},
		"GET /api/v1/agents/{agentId}/avatar":                        {"Get agent avatar", "Retrieve the avatar for the selected agent."},
		"POST /api/v1/agents/{agentId}/chats/{chatId}/shares":        {"Create chat share", "Create a share for the selected agent conversation."},
		"GET /api/v1/agents/{agentId}/chats/{chatId}/shares":         {"List chat shares", "List shares for the selected agent conversation."},
		"GET /api/v1/agents":                                         {"List agents", "List agents visible to the authenticated caller in the selected workspace."},
		"POST /api/v1/agents":                                        {"Create agent", "Create an agent from the supplied configuration."},
		"POST /api/v1/agents/hire":                                   {"Hire agent", "Hire an agent using the supplied hiring configuration."},
		"GET /api/v1/crews":                                          {"List crews", "List crews visible to the caller in the selected workspace."},
		"GET /api/v1/workspaces/{workspaceId}/pipelines":             {"List routines", "List saved pipeline definitions (routines) in the selected workspace."},
		"POST /api/v1/crews/{crewId}/missions":                       {"Create mission", "Create a mission for the selected crew."},
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
	parts := strings.Split(strings.Trim(rt.path, "/"), "/")
	if len(parts) > 0 && parts[0] == "api" {
		parts = parts[1:]
	}
	if len(parts) > 0 && parts[0] == "v1" {
		parts = parts[1:]
	}
	words := []string{}
	for i, part := range parts {
		if part != "" && !strings.HasPrefix(part, "{") {
			if i+1 < len(parts) && strings.HasPrefix(parts[i+1], "{") {
				part = metadataSingular(part)
			}
			words = append(words, strings.NewReplacer("-", " ", "_", " ", ":", " ").Replace(part))
		}
	}
	resource := strings.Join(words, " ")
	if resource == "" {
		resource = "API resource"
	}
	last := ""
	if len(parts) > 0 {
		last = parts[len(parts)-1]
	}
	// A final identifier selects one resource. Singleton subresources such
	// as avatar, health and status also use Get, even after an identifier.
	action := "Get"
	switch rt.method {
	case "GET":
		if metadataCollection(last) {
			action = "List"
		}
	case "HEAD":
		action = "Inspect"
	case "OPTIONS":
		action = "Get supported methods for"
	case "DELETE":
		action = "Delete"
	case "PATCH", "PUT":
		action = "Update"
	case "POST":
		if verb, ok := metadataAction(last); ok {
			action = verb
			if len(words) > 1 {
				resource = strings.Join(words[:len(words)-1], " ")
			} else {
				resource = "API resource"
			}
		} else if metadataCollection(last) {
			action = "Create"
		} else {
			// A POST is not necessarily a create. Unknown singleton/action
			// routes get a neutral label, not invented business semantics.
			action = "Submit"
		}
	}
	summary := strings.Join(strings.Fields(action+" "+resource), " ")
	// Fallback prose deliberately contains only the public operation and
	// route. Never publish arbitrary Go comments or implementation names.
	return summary, summary + " (" + rt.method + " " + openAPIPath(rt.path) + ").", read
}

func metadataCollection(segment string) bool {
	if segment == "people" {
		return true
	}
	if strings.ContainsAny(segment, "{}") {
		return false
	}
	switch segment {
	case "status", "aux-status", "container-status", "crews-status", "image-status", "setup-status", "access", "metrics", "mission-metrics", "stats", "settings", "preferences", "notification-prefs", "capabilities", "progress":
		return false
	}
	return strings.HasSuffix(segment, "s")
}

func metadataAction(segment string) (string, bool) {
	verbs := map[string]string{
		"test_run": "Test", "step_run": "Run step for", "run_batch": "Run batch for", "run": "Run", "start": "Start", "stop": "Stop", "restart": "Restart", "resume": "Resume", "cancel": "Cancel",
		"search": "Search", "preview": "Preview", "preview-contents": "Preview contents of", "discover": "Discover",
		"approve": "Approve", "reject": "Reject", "resolve": "Resolve", "complete": "Complete", "review": "Review",
		"enable": "Enable", "disable": "Disable", "activate": "Activate", "reactivate": "Reactivate", "suspend": "Suspend",
		"publish": "Publish", "unpublish": "Unpublish", "upload": "Upload", "import": "Import", "export": "Export",
		"clone": "Clone", "fork": "Fork", "restore": "Restore", "rollback": "Roll back", "rebuild": "Rebuild",
		"refresh": "Refresh", "sync": "Synchronize", "rotate": "Rotate", "revoke": "Revoke", "revoke-all": "Revoke all",
		"connect": "Connect", "auto-connect": "Automatically connect", "bind": "Bind", "install": "Install", "deploy": "Deploy",
		"check": "Check", "verify": "Verify", "test": "Test", "self-test": "Test", "probe": "Probe", "dry_run": "Preview execution of",
		"apply": "Apply", "save": "Save", "reset": "Reset", "generate": "Generate", "fetch": "Fetch", "read": "Read",
		"ask": "Ask", "hire": "Hire", "rehire": "Rehire", "pin": "Pin", "unpin": "Unpin", "mute": "Mute", "unlock": "Unlock",
		"prune-crew-runtimes": "Prune crew runtimes for", "prune-legacy-resources": "Prune legacy resources for", "reap-orphan-containers": "Reap orphan containers for",
		"container-start": "Start container for", "container-stop": "Stop container for", "restart-agents": "Restart agents for",
	}
	verb, ok := verbs[segment]
	return verb, ok
}

// Singularize resource names only when the next path segment selects one item.
func metadataSingular(resource string) string {
	switch resource {
	case "people":
		return "person"
	case "status", "access", "metrics", "settings", "news":
		return resource
	case "statuses":
		return "status"
	case "addresses":
		return "address"
	}
	if strings.HasSuffix(resource, "ies") {
		return strings.TrimSuffix(resource, "ies") + "y"
	}
	if strings.HasSuffix(resource, "s") && !strings.HasSuffix(resource, "ss") {
		return strings.TrimSuffix(resource, "s")
	}
	return resource
}
