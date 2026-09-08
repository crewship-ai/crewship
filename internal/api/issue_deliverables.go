package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// Publish only explicitly declared shared deliverables. OpenRoot confines all
// reads, including symlinks, to this issue's crew; arbitrary host paths are never
// read. Existing attachment validation, size limits and digest dedupe still apply.
func (h *AssignmentHandler) publishIssueDeliverables(ctx context.Context, assignmentID, result string) {
	if h.attachments == nil || h.attachments.storagePath == "" {
		return
	}
	handoff := orchestrator.ParseHandoff(result)
	if !handoff.Parsed || handoff.Artifacts == "" {
		return
	}
	var mission, workspace, crew, agent string
	err := h.db.QueryRowContext(ctx, `SELECT m.id,m.workspace_id,m.crew_id,a.assigned_to_id FROM assignments a JOIN missions m ON m.id=a.mission_id WHERE a.id=? AND a.workspace_id=m.workspace_id`, assignmentID).Scan(&mission, &workspace, &crew, &agent)
	if err != nil || !safeIDPattern.MatchString(crew) {
		return
	}
	root, err := os.OpenRoot(filepath.Join(h.attachments.storagePath, "crews", crew, "shared"))
	if err != nil {
		return
	}
	defer root.Close()
	for i, raw := range strings.FieldsFunc(handoff.Artifacts, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if i >= 10 {
			break
		}
		path := strings.Trim(strings.TrimSpace(raw), "`\" ")
		if !strings.HasPrefix(path, "/crew/shared/") {
			continue
		}
		relative := strings.TrimPrefix(path, "/crew/shared/")
		if strings.HasPrefix(relative, ".memory/") || !filepath.IsLocal(relative) {
			continue
		}
		file, err := root.Open(relative)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > agentAttachmentUploadBytes {
			file.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, agentAttachmentUploadBytes+1))
		file.Close()
		if err != nil || len(data) > agentAttachmentUploadBytes {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
		att, created, err := h.attachments.attachBytes(req, workspace, mission, filepath.Base(relative), data, "", agent)
		if err != nil {
			h.logger.Warn("could not publish issue deliverable", "assignment_id", assignmentID, "error", err)
			continue
		}
		if created {
			h.attachments.recordAttachmentEvent(req, mission, workspace, "agent", agent, actionAttachmentAdded, att)
		}
	}
}
