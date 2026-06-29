package sidecar

// Slash-action route for agent-authored skills.
//
// Calling convention mirror of skill_generate.go, but the agent supplies the
// finished SKILL.md it wrote with its own model rather than a prompt for a
// server-side generation LLM — so this works on OAuth-only workspaces with no
// Anthropic API key, and captures the real workflow the agent just performed.
//
//	POST http://localhost:9119/skills/author
//	Content-Type: application/json
//	{
//	  "content": "---\nname: ...\ndescription: ...\n---\n# ...\n"
//	}
//
// The crew + workspace come from the sidecar's IPC config, not the request, so
// an agent can only stage into its own crew's namespace. The staged skill is
// held for human review (proposed approve) — it is never made live directly.

import (
	"net/http"
	"net/url"
)

// handleSkillAuthor proxies POST /skills/author to the crewshipd internal
// mirror, stamping the workspace and crew from IPC config onto the query.
func (s *Server) handleSkillAuthor(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	q.Set("crew_id", s.ipc.CrewID)
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/skills/author?"+q.Encode())
}
