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
		"GET /api/v1/workspaces/{workspaceId}/logo":                  {"Get workspace logo", "Retrieve the uploaded logo of the selected workspace."},
		"POST /api/v1/workspaces/{workspaceId}/logo":                 {"Set workspace logo", "Upload or replace the workspace logo (PNG, JPEG or WebP, at most 2MB)."},
		"DELETE /api/v1/workspaces/{workspaceId}/logo":               {"Remove workspace logo", "Remove the workspace logo; the workspace is drawn with its initials again."},
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

		// Artifacts. The routes live under /pages for compatibility; {slug} is the
		// artifact slug or "@" followed by its page ID. Every operation is off by
		// default; availability reports the gate without requiring it to be on.
		"GET /api/v1/pages/artifacts/availability":                      {"Check Artifacts availability", "Report whether Artifacts are enabled for the selected workspace. Artifacts are off by default: enable the `artifact_projects` workspace feature flag, or set CREWSHIP_ARTIFACT_PROJECTS_ENABLED=true for every workspace on the instance."},
		"GET /api/v1/pages/artifacts/storage":                           {"Get artifact storage usage", "Report this workspace's aggregate artifact storage (used, in-flight reserved and limit bytes, plus warning levels). Workspace owners and admins only; members get 403 because workspace-wide usage reveals activity on Pages they cannot read. History is never deleted automatically; when full, writes are refused with 413 artifact_storage_quota and existing data is kept."},
		"POST /api/v1/pages/artifacts":                                  {"Create artifact", "Create an artifact and publish its first immutable version from `entrypoint`, `files` and an optional `message`. Requires a workspace OWNER, ADMIN or MANAGER (or the page-create capability) with a `pages:write` credential. A slug that is already used returns 409 `artifact_slug_taken`. Requires the `artifact_projects` feature flag or its instance bypass."},
		"GET /api/v1/pages/{slug}/artifact":                             {"Get artifact", "Read one consistent artifact snapshot: the selected version's files, latest and shared version numbers, history quota, comments and the caller's capabilities. The owner reads any version (optional `version` query) and the full history; an editor reads the latest version; other readers see only the shared version. An artifact the caller may not see returns 404."},
		"GET /api/v1/pages/{slug}/artifact/versions":                    {"List artifact versions", "Return the same authorized snapshot as Get artifact. Only the owner receives the complete version history; other callers receive the single version they may read."},
		"POST /api/v1/pages/{slug}/artifact/versions":                   {"Publish artifact version", "Publish a new immutable version. `base_version` must equal the current latest version (compare-and-swap); otherwise the request fails with 409 `artifact_version_conflict` and nothing is written. Allowed for the owner and for users with an Edit grant. A version above 32 MiB or a history above 256 MiB returns 413 `artifact_history_quota`; existing versions are kept."},
		"POST /api/v1/pages/{slug}/artifact/restore":                    {"Restore artifact version", "Publish a copy of an earlier `version` as the new latest version. History is never rewritten. `base_version` must equal the current latest version, otherwise 409 `artifact_version_conflict`. Owner only, or the agent run acting for the owner."},
		"POST /api/v1/pages/{slug}/artifact/share":                      {"Share artifact version", "Pin `version` as the shared version that workspace readers see and record an audit entry. `base_version` must equal the current latest version, otherwise 409 `artifact_version_conflict`. Owner only; an Edit grant or an agent run cannot change the shared version."},
		"GET /api/v1/pages/{slug}/artifact/comments":                    {"List artifact comments", "Return the same authorized snapshot as Get artifact. The owner receives comments on every version; other callers receive only the comments on the version they may read."},
		"POST /api/v1/pages/{slug}/artifact/comments":                   {"Comment on artifact", "Anchor a comment to an element (`path`, `selector`, `quote`) of the version the caller currently sees. A stale version returns 409 `artifact_version_conflict`; an anchor that no longer matches returns 409 `artifact_anchor_mismatch`. Set `send_to_agent` to queue the comment for the artifact's agent."},
		"POST /api/v1/pages/{slug}/artifact/comments/{commentId}/reply": {"Reply to artifact comment", "Record a reply to a comment against a later `version`, or against the same version with `needs_clarification`. Owner only, or the agent run acting for the owner."},
		"GET /api/v1/pages/{slug}/artifact/data":                        {"List Dashboard collections", "List the data collections declared by `dashboard.json` in code `version`, with their document counts and sizes. The owner may pass any version, an editor the latest version and other readers only the shared version. Requires the `artifact_projects` and `artifact_dashboard_data` feature flags."},
		"GET /api/v1/pages/{slug}/artifact/data/{collection}":           {"Read Dashboard collection", "Read every document of one collection declared by code `version`. Data is shared by all versions of the artifact; the version only selects which collections are visible. Requires the `artifact_projects` and `artifact_dashboard_data` feature flags."},
		"POST /api/v1/pages/{slug}/artifact/data/{collection}":          {"Write Dashboard documents", "Atomically upsert up to 50 documents. `base_version` must equal the latest code version and each document's `ifVersion` must equal its stored version (0 for a new document); any mismatch returns 409 and nothing is written. Owner only, or the agent run acting for the owner. Exceeding a dataset limit returns 413 `artifact_data_quota`."},
		"GET /api/v1/pages/{slug}/artifact/slides":                      {"Read artifact Slides", "Return the Slides projection (deck and comment anchors) of an artifact whose code version contains `deck.json` and one file per slide. The `version` query is required: the owner may choose any version, an editor the latest and other readers only the shared version. Unsupported content returns 422. Requires the `artifact_projects` and `artifact_slides` feature flags."},
		"GET /api/v1/pages/{slug}/artifact/export.pdf":                  {"Export artifact Slides as PDF", "Render the Slides projection of code `version` to PDF. Access rules match Read artifact Slides. Text that does not fit its frame returns 422 `artifact_slides_overflow`; an export over the size limit returns 413. Requires the `artifact_projects` and `artifact_slides` feature flags."},
		"GET /api/v1/workspaces/{workspaceId}/artifact-creation-policy": {"Get artifact creation policy", "Read the workspace ceiling for agent-created artifacts: whether it is enabled, which crews may create artifacts and the policy `version`. Workspace OWNER or ADMIN only."},
		"PUT /api/v1/workspaces/{workspaceId}/artifact-creation-policy": {"Update artifact creation policy", "Replace the agent artifact-creation ceiling. `base_version` must equal the current policy version, otherwise 409 `stale_policy`. Every crew must be an active crew of this workspace. Workspace OWNER or ADMIN only; the change is audited."},

		// Design canvases share the Artifacts version history but have their own
		// workspace flag (`artifacts_design`, or CREWSHIP_ARTIFACTS_DESIGN_ENABLED).
		"POST /api/v1/pages/design":                   {"Create Design", "Create a Design canvas and its first version. Requires a workspace OWNER, ADMIN or MANAGER (or the page-create capability) with a `pages:write` credential; agent runs cannot create one. Requires the `artifacts_design` feature flag."},
		"GET /api/v1/pages/{slug}/design":             {"Get Design", "Read the live collaborative canvas, or the historical checkpoint chosen with `version`. Without `version`, authorized readers receive the live canvas too; a non-editor selecting a retained version is limited to the shared version. Requires the `artifacts_design` feature flag."},
		"GET /api/v1/pages/{slug}/design/versions":    {"List Design versions", "List Design versions. Editors receive the full history; read-only callers receive only the shared version."},
		"GET /api/v1/pages/{slug}/design/ws":          {"Open Design collaboration socket", "Upgrade to a WebSocket that carries standard Yjs sync and awareness frames for the live canvas. Read-only callers receive updates but cannot write. Live edits change the canvas but do not publish a version; use Create Design checkpoint."},
		"POST /api/v1/pages/{slug}/design/operations": {"Apply Design operations", "Apply up to 500 typed operations (upsert, delete, reorder) to the live canvas and publish the result as a new version in one transaction. A concurrent change returns 409. Requires edit rights."},
		"POST /api/v1/pages/{slug}/design/snapshots":  {"Create Design checkpoint", "Publish the current live canvas as a new immutable version. Requires edit rights."},
		"POST /api/v1/pages/{slug}/design/restore":    {"Restore Design version", "Replace the live canvas with an earlier `version` and publish it as a new version. Requires edit rights."},
		"GET /api/v1/pages/{slug}/design/comments":    {"List Design comments", "List Design comments. Callers without edit rights receive only the comments on the shared version."},
		"POST /api/v1/pages/{slug}/design/comments":   {"Comment on Design", "Anchor a comment to a node (`selector`) of an immutable Design `version`. Requires edit rights or a Comment grant."},
		"POST /api/v1/pages/{slug}/design/share":      {"Share Design version", "Pin `version` as the shared version that read-only callers see. Owner only; an Edit grant does not allow sharing."},
		"POST /api/v1/pages/{slug}/design/agent":      {"Connect Design agent", "Link the Design to one of the owner's private agent chats (`chat_id`) so the owner can send it work. Owner only; this grants the agent no access by itself."},

		// Craft agent tasks need the instance switches CREWSHIP_CRAFT_NATIVE_ENABLED
		// and CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED; otherwise they answer 503.
		"POST /api/v1/pages/{slug}/craft/tasks":         {"Start Craft agent task", "Ask an agent to edit the whole Craft document on behalf of the calling human. The body is exactly `chat_id`, `message_id` and `whole_document: true`. Returns 202 with a `work_id` in state `queued`; poll Get Craft agent task for progress. Answers 503 unless native Craft and Craft agents are enabled on the instance."},
		"GET /api/v1/pages/{slug}/craft/tasks/{workID}": {"Get Craft agent task", "Read the state, recorded tool events and last committed save receipt of a Craft agent task. Only the human who started the task can read it; document content is never included."},
		"POST /api/v1/pages/{slug}/craft/tasks/cancel":  {"Stop Craft agent task", "Stop the agent that currently holds the Craft lease for this document, revoke its grant and request cancellation of its run. The body must be an empty object. Only the human who started the task may stop it. Returns 200 when the run stopped, or 202 while the stop is still pending."},
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
