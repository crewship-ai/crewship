package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
)

var routineMCPGetDraftSchema = json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string"},"crew":{"type":"string","description":"Target crew slug, permitted only for the onboarding Guide."}},"required":["slug"],"additionalProperties":false}`)
var routineMCPSaveDraftSchema = json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string"},"crew":{"type":"string"},"draft":{"type":"object","description":"The exact revision envelope from get_routine_draft. Change only document; preserve id, slug, revision, base_pipeline_id and base_revision.","properties":{"id":{"type":"string"},"slug":{"type":"string"},"revision":{"type":"integer","minimum":0},"base_pipeline_id":{"type":"string"},"base_revision":{"type":"integer"},"document":{"type":"object"}},"required":["slug","revision","document"]}},"required":["slug","draft"],"additionalProperties":false}`)

type routineDraftArguments struct {
	Slug  string          `json:"slug"`
	Crew  string          `json:"crew"`
	Draft json.RawMessage `json:"draft"`
}

func (s *Server) routineDraft(ctx context.Context, args routineDraftArguments, agentID string, save bool) (int, []byte) {
	if s.ipc == nil {
		return http.StatusServiceUnavailable, mustJSON(map[string]string{"error": "IPC not configured"})
	}
	if args.Slug == "" {
		return http.StatusBadRequest, mustJSON(map[string]string{"error": "slug required"})
	}
	// Caller-provided author/workspace fields are never forwarded.
	body := map[string]any{"workspace_id": s.ipc.WorkspaceID, "slug": args.Slug, "author_crew_id": s.ipc.CrewID, "author_agent_id": agentID, "target_crew_slug": args.Crew}
	if save {
		if len(args.Draft) == 0 {
			return http.StatusBadRequest, mustJSON(map[string]string{"error": "draft revision envelope required"})
		}
		body["draft"] = args.Draft
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return http.StatusBadRequest, mustJSON(map[string]string{"error": "invalid draft"})
	}
	path := "/api/v1/internal/pipelines/drafts/get"
	if save {
		path = "/api/v1/internal/pipelines/drafts/save"
	}
	res, err := s.ipcRequestJSON(ctx, http.MethodPost, path, encoded)
	if err != nil {
		return http.StatusBadGateway, mustJSON(map[string]string{"error": "draft request failed: " + err.Error()})
	}
	return res.status, res.body
}
