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
//
// That was true as a description of this file and false as a
// description of the system until #1768: the backend mirror
// (internal_routines.go) skipped its gate for the agent path and
// justified it by asserting the autonomy check had already run "in the
// /spawn-style entry" — meaning here. Each file named the other as the
// enforcement point and neither enforced, so an agent could create a
// cron entry that outlived the session at any autonomy level.
//
// The authority named above is now real: RoutineInternalAdapter.
// CreateSchedule resolves the calling crew's autonomy_level and refuses
// at strict, or writes the schedule with enabled=0 pending an operator
// approval below full. Do not re-add a check here — a second gate on the
// transport is how the two drift apart again. If this comment ever needs
// to claim enforcement elsewhere, name the function, not the layer.

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
