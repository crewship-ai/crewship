package sidecar

// LEAD-driven ephemeral hire (PR-D F5 §6).
//
// A LEAD agent in active-orchestration mode can spawn a contractor on
// demand by POSTing to the sidecar's /spawn endpoint. The sidecar
// forwards the request to crewshipd's internal /api/v1/internal/
// agents/hire endpoint, which proxies into the same AgentHandler.Hire
// logic the public API uses (with role MANAGER injected via the
// internal-auth path so the LEAD's caller-role doesn't block the
// hire).
//
// Calling convention mirrors /agent/create + /crew/create:
//
//	POST http://localhost:9119/spawn
//	Content-Type: application/json
//	{
//	  "crew_slug": "docs",
//	  "template_slug": "docs-writer",
//	  "model": "claude-haiku-4-5",
//	  "ttl_minutes": 60,
//	  "reason": "section 7 needs a polish pass"
//	}
//
// The LEAD's own crew_id is NOT auto-injected — the request body
// passes through verbatim. That lets a LEAD with explicit cross-crew
// authority hire into a connected crew (subject to that crew's
// autonomy_level policy on the server side; strict crews still
// reject). 99% of LEADs will hire into their own crew.

import (
	"net/http"
	"net/url"
)

// handleSpawn proxies POST /spawn to the crewshipd internal hire
// endpoint. The IPC layer attaches X-Internal-Token + workspace_id
// downstream; the LEAD's request body flows through verbatim.
//
// Same shape as handleCreateAgent / handleCreateCrew — single line
// inside the handler because proxyToAPI does the heavy lifting. The
// rest is service-availability check + path composition.
func (s *Server) handleSpawn(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	// URL-encode workspace_id so reserved characters (&, ?, =, #) in
	// an operator-set workspace identifier can't poison query parsing
	// downstream. url.Values is more defensive than naked string
	// concat against operator-controlled input.
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/agents/hire?"+q.Encode())
}
