package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// blockedTask holds a BLOCKED task's ID and its parsed dependency list.
type blockedTask struct {
	id   string
	deps []string
}

// findUnblockableTasks queries all BLOCKED tasks in a mission whose dependencies
// are all COMPLETED. When filterByTaskID is non-empty, only tasks that depend on
// that specific task are considered.
func (h *MissionHandler) findUnblockableTasks(ctx context.Context, missionID, filterByTaskID string) []blockedTask {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, depends_on FROM mission_tasks WHERE mission_id = ? AND status = 'BLOCKED'`,
		missionID)
	if err != nil {
		h.logger.Error("query blocked tasks", "error", err)
		return nil
	}
	defer rows.Close()

	var candidates []blockedTask
	for rows.Next() {
		var id, depsJSON string
		if err := rows.Scan(&id, &depsJSON); err != nil {
			continue
		}

		deps := parseDependencyJSON(depsJSON)
		if len(deps) == 0 {
			continue
		}

		// When filtering by a specific completed task, skip tasks that don't depend on it
		if filterByTaskID != "" {
			found := false
			for _, dep := range deps {
				if dep == filterByTaskID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		candidates = append(candidates, blockedTask{id: id, deps: deps})
	}

	if len(candidates) == 0 {
		return nil
	}

	// Batch-fetch all task statuses for this mission in one query
	statusRows, err := h.db.QueryContext(ctx,
		`SELECT id, status FROM mission_tasks WHERE mission_id = ?`, missionID)
	if err != nil {
		h.logger.Error("query task statuses", "error", err)
		return nil
	}
	defer statusRows.Close()

	statusMap := make(map[string]string)
	for statusRows.Next() {
		var id, st string
		if err := statusRows.Scan(&id, &st); err != nil {
			continue
		}
		statusMap[id] = st
	}

	// Check if ALL dependencies are now completed using the pre-fetched map
	var result []blockedTask
	for _, bt := range candidates {
		allDone := true
		for _, dep := range bt.deps {
			if statusMap[dep] != "COMPLETED" {
				allDone = false
				break
			}
		}
		if allDone {
			result = append(result, bt)
		}
	}
	return result
}

// unblockDependentTasks finds BLOCKED tasks whose all dependencies are now completed
// and transitions them to PENDING with WebSocket broadcast. When completedTaskID is
// non-empty, only tasks that depend on that specific task are checked.
func (h *MissionHandler) unblockDependentTasks(r *http.Request, missionID, completedTaskID string) {
	wsID := WorkspaceIDFromContext(r.Context())
	tasks := h.findUnblockableTasks(r.Context(), missionID, completedTaskID)

	now := time.Now().UTC().Format(time.RFC3339)
	for _, bt := range tasks {
		if _, err := h.db.ExecContext(r.Context(),
			`UPDATE mission_tasks SET status = 'PENDING', updated_at = ? WHERE id = ?`,
			now, bt.id); err != nil {
			h.logger.Error("unblock task failed", "task_id", bt.id, "error", err)
			continue
		}
		if h.hub != nil {
			h.hub.Broadcast("mission:"+missionID, ws.ServerMessage{
				Type:    "task.status",
				Channel: "mission:" + missionID,
				Payload: map[string]string{"id": bt.id, "status": "PENDING"},
			})
			wsChannel := "workspace:" + wsID
			h.hub.Broadcast(wsChannel, ws.ServerMessage{
				Type:    "task.updated",
				Channel: wsChannel,
				Payload: map[string]string{"id": bt.id, "mission_id": missionID, "status": "PENDING"},
			})
		}
	}
}

// unblockCompletedDeps transitions all BLOCKED tasks in a mission whose dependencies
// are all COMPLETED to PENDING. Used after restart to fix tasks that were blindly set
// to BLOCKED despite having met deps. Does not broadcast (restart has its own broadcast).
func (h *MissionHandler) unblockCompletedDeps(r *http.Request, missionID string) {
	tasks := h.findUnblockableTasks(r.Context(), missionID, "")

	now := time.Now().UTC().Format(time.RFC3339)
	for _, bt := range tasks {
		if _, err := h.db.ExecContext(r.Context(),
			`UPDATE mission_tasks SET status = 'PENDING', updated_at = ? WHERE id = ?`,
			now, bt.id); err != nil {
			h.logger.Error("unblockCompletedDeps: update failed", "task_id", bt.id, "error", err)
		}
	}
}

// remapDependencies replaces old task IDs in a JSON dependency array with new IDs
// using the provided mapping. Used during mission cloning.
func remapDependencies(depsJSON string, idMap map[string]string) string {
	deps := parseDependencyJSON(depsJSON)
	if len(deps) == 0 {
		return "[]"
	}
	newDeps := make([]string, 0, len(deps))
	for _, d := range deps {
		if newID, ok := idMap[d]; ok {
			newDeps = append(newDeps, newID)
		}
	}
	out, _ := json.Marshal(newDeps)
	return string(out)
}
