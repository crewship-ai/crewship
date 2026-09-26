package api

import (
	"context"
	"strings"
)

type chatWorkSource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug,omitempty"`
	RunID  string `json:"run_id,omitempty"`
	StepID string `json:"step_id,omitempty"`
}

// One scoped batch for the returned page. Never infer links from display titles.
func (h *AgentHandler) attachChatSources(ctx context.Context, workspaceID string, chats []chatResponse) error {
	if len(chats) == 0 {
		return nil
	}
	args := []any{workspaceID}
	byID := make(map[string]*chatResponse, len(chats))
	for i := range chats {
		args = append(args, chats[i].ID)
		byID[chats[i].ID] = &chats[i]
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chats)), ",")
	rows, err := h.db.QueryContext(ctx, `SELECT c.id, COALESCE(p.id,''),COALESCE(p.name,''),COALESCE(p.slug,''),COALESCE(pr.id,''),COALESCE(c.pipeline_step_id,''),COALESCE(m.id,''),COALESCE(m.title,''),COALESCE(m.identifier,'')
 FROM chats c
 LEFT JOIN pipeline_runs pr ON pr.id=c.pipeline_run_id AND pr.workspace_id=c.workspace_id
 LEFT JOIN pipelines p ON p.id=pr.pipeline_id AND p.workspace_id=c.workspace_id AND p.deleted_at IS NULL
 LEFT JOIN missions m ON m.id=c.id AND m.workspace_id=c.workspace_id AND c.mode='MISSION'
 WHERE c.workspace_id=? AND c.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, pid, name, slug, run, step, mid, title, identifier string
		if err := rows.Scan(&id, &pid, &name, &slug, &run, &step, &mid, &title, &identifier); err != nil {
			return err
		}
		if mid != "" {
			byID[id].Source = &chatWorkSource{Kind: "issue", ID: mid, Name: title, Slug: identifier}
		} else if pid != "" {
			byID[id].Source = &chatWorkSource{Kind: "routine", ID: pid, Name: name, Slug: slug, RunID: run, StepID: step}
		}
	}
	return rows.Err()
}
