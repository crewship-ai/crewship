package sidecar

// Slash-action route for pipeline-schedule (routine) creation
// (PRD-SLASH-CAPABILITIES-2026 §6.5).
//
// Calling convention mirrors /spawn — the sidecar just forwards to
// crewshipd's internal mirror with workspace_id appended to the
// query and X-Caller-User-Id (when set on the inbound) flowing
// through proxyToAPIFiltered automatically.
//
//	POST http://localhost:9119/routines/schedules/create
//	Content-Type: application/json
//	X-Caller-User-Id: <user id>     (set by chat-bridge / CLI repl)
//	X-Caller-Source:  chat-ui | cli-repl
//	{
//	  "name": "nightly digest",
//	  "target_pipeline_slug": "daily-digest",
//	  "cron_expr": "0 7 * * *",
//	  "timezone": "Europe/Prague",
//	  "inputs": { ... }
//	}
//
// All authorization happens server-side. The sidecar does not
// inspect role, capability, or caller identity beyond passing
// the headers through — keeping enforcement in one place avoids
// drift between transport (sidecar) and authority (backend).

import (
	"net/http"
	"net/url"
)

// handleRoutineScheduleCreate proxies POST /routines/schedules/create
// to the crewshipd internal mirror. URL-encodes workspace_id so a
// reserved-char workspace identifier can't poison the query string —
// same defensive shape as handleSpawn.
func (s *Server) handleRoutineScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/routines/schedules?"+q.Encode())
}
