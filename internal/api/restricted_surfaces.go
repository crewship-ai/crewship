package api

// restrictedSurface is one screen a restricted session may open, named by the
// UI path it lives at and the exact restrictedRoutes patterns it needs.
//
// It is derived FROM the allowlist, never alongside it: a surface is offered
// only when every pattern it needs is in restrictedRoutes, so the shell cannot
// show a screen whose requests the server would refuse (#2861).
// restricted_surfaces_test.go holds the two in step both ways — every surface
// pattern is allowlisted, and every allowlisted pattern belongs to a surface
// or is named as shared infrastructure.
type restrictedSurface struct {
	Name     string
	Patterns []string
}

var restrictedSurfaces = []restrictedSurface{
	{Name: "chat", Patterns: []string{
		"GET /api/v1/agents",
		"GET /api/v1/agents/{agentId}/chats",
		"POST /api/v1/agents/{agentId}/chats",
		"GET /api/v1/chats/{chatId}/messages",
		"GET /api/v1/chats/{chatId}/execution-profile",
	}},
	{Name: "routines", Patterns: []string{
		"GET /api/v1/workspaces/{workspaceId}/restricted-routines",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run",
		"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}",
	}},
	{Name: "pages", Patterns: []string{
		"GET /api/v1/workspaces/{workspaceId}/restricted-pages",
		"POST /api/v1/pages/{slug}/application/actions/{panelId}/{actionId}",
		"POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}",
		"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}",
	}},
	{Name: "account_security", Patterns: []string{
		"GET /api/v1/auth/sessions",
		"POST /api/v1/auth/sessions/{id}/revoke",
		"GET /api/v1/auth/cli-tokens",
		"DELETE /api/v1/auth/cli-tokens/{tokenId}",
		"POST /api/v1/users/me/password",
	}},
}

// allowedRestrictedSurfaces lists, in a stable order, the surfaces whose every
// pattern restrictedRoutes allows.
func allowedRestrictedSurfaces() []string {
	out := []string{}
	for _, s := range restrictedSurfaces {
		ok := true
		for _, p := range s.Patterns {
			if _, allowed := restrictedRoutes[p]; !allowed {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s.Name)
		}
	}
	return out
}
