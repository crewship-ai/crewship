package sidecar

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pages"
)

// Identity is never taken from model arguments. Keep this request separate
// from the API envelope so future identity fields cannot be forwarded by accident.
type pageProjectToolRequest struct {
	Operation        string              `json:"operation" yaml:"operation"`
	Slug             string              `json:"slug" yaml:"slug"`
	Crew             string              `json:"crew,omitempty" yaml:"crew,omitempty"`
	ExpectedRevision *int64              `json:"expected_revision,omitempty" yaml:"expected_revision,omitempty"`
	Files            []pages.ProjectFile `json:"files,omitempty" yaml:"files,omitempty"`
	DeletePaths      []string            `json:"delete_paths,omitempty" yaml:"delete_paths,omitempty"`
	Path             string              `json:"path,omitempty" yaml:"path,omitempty"`
	BuildID          string              `json:"build_id,omitempty" yaml:"build_id,omitempty"`
	Definition       *pages.Document     `json:"definition,omitempty" yaml:"definition,omitempty"`
}

func (s *Server) pageProject(ctx context.Context, body pageProjectToolRequest, agentID string) (int, []byte) {
	if s.ipc == nil {
		return 503, mustJSON(map[string]string{"error": "IPC not configured"})
	}
	data, err := json.Marshal(map[string]any{"workspace_id": s.ipc.WorkspaceID, "crew_id": s.ipc.CrewID, "agent_id": agentID, "target_crew_slug": body.Crew, "operation": body.Operation, "slug": body.Slug, "expected_revision": body.ExpectedRevision, "files": body.Files, "delete_paths": body.DeletePaths, "path": body.Path, "build_id": body.BuildID, "definition": body.Definition})
	if err != nil {
		return 400, mustJSON(map[string]string{"error": "invalid project arguments"})
	}
	result, err := s.ipcRequestJSON(ctx, http.MethodPost, "/api/v1/internal/pages/project", data)
	if err != nil {
		return 502, mustJSON(map[string]string{"error": "Page project response unavailable; outcome may be unknown. Read the current revision and build status before retrying."})
	}
	return result.status, result.body
}

var pageProjectMCPSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation","slug"],"properties":{"operation":{"type":"string","enum":["init","read","save","build","status","check"]},"slug":{"type":"string"},"crew":{"type":"string","description":"Onboarding only: target owner crew, same delegation as save_page."},"expected_revision":{"type":"integer","minimum":0},"path":{"type":"string"},"build_id":{"type":"string"},"files":{"type":"array","maxItems":256,"items":{"type":"object","additionalProperties":false,"required":["path","encoding","content"],"properties":{"path":{"type":"string"},"encoding":{"type":"string","enum":["utf8","base64"]},"content":{"type":"string"}}}},"delete_paths":{"type":"array","maxItems":256,"items":{"type":"string"}},"definition":{"type":"object"}}}`)
