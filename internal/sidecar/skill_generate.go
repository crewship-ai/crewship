package sidecar

// Slash-action route for LLM-driven skill generation
// (PRD-SLASH-CAPABILITIES-2026 §6.5).
//
// Calling convention mirror of routine_schedule.go and spawn.go.
//
//	POST http://localhost:9119/skills/generate
//	Content-Type: application/json
//	X-Caller-User-Id: <user id>
//	X-Caller-Source:  chat-ui | cli-repl
//	{
//	  "slug": "extract-pdf-tables",
//	  "prompt": "Use when the user asks to extract tables from PDFs...",
//	  "model": "claude-haiku-4-5"     // optional
//	}
//
// LLM bill goes against the workspace's Anthropic credential — the
// public Generate path (skills_generate.go:94) resolves it server-
// side. The 30 s sidecar timeout is loose enough for a single LLM
// call but doesn't allow runaway-loop bills (the underlying
// SkillGenerateHandler enforces token caps separately).

import (
	"net/http"
	"net/url"
)

// handleSkillGenerate proxies POST /skills/generate to the crewshipd
// internal mirror.
func (s *Server) handleSkillGenerate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/skills/generate?"+q.Encode())
}
