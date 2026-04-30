package cartographer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Fork branches a mission from a checkpoint. A new missions row is
// created with:
//
//   - a fresh id + trace_id
//   - title prefixed with "Fork: "
//   - status reset to PLANNING
//   - the same workspace_id, crew_id, lead_agent_id, description, plan as the parent
//
// All mission_tasks that existed on the parent at fork time are copied
// onto the new mission — we snapshot the whole task list because "fork"
// semantically means "continue from this state", not "start fresh". A
// task that was COMPLETED on the parent stays COMPLETED on the fork so
// the fork doesn't redo finished work; PENDING/BLOCKED tasks carry
// across so the fork has something to work on.
//
// A new Checkpoint row is stamped for the fork with fork_of pointing
// at the source checkpoint. The newly-stamped checkpoint has the SAME
// journal_cursor as the source — the fork literally starts from that
// point in history.
//
// Emits EntryForkCreated. Returns the new mission id and the new
// checkpoint id so the handler can redirect the UI.
//
// Everything happens inside a single transaction — either the new
// mission + tasks + checkpoint all exist, or none of them do.
func Fork(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, fromCheckpointID, newLabel, userID string) (string, string, error) {
	if workspaceID == "" || fromCheckpointID == "" {
		return "", "", errors.New("cartographer: workspace_id and from_checkpoint_id required")
	}

	src, err := Get(ctx, db, workspaceID, fromCheckpointID)
	if err != nil {
		return "", "", err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("cartographer: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Read parent mission row.
	var (
		parentCrew, parentLead, parentTitle string
		parentDesc, parentPlan              sql.NullString
	)
	err = tx.QueryRowContext(ctx, `SELECT crew_id, lead_agent_id, title, description, plan
		FROM missions WHERE id = ? AND workspace_id = ?`,
		src.MissionID, workspaceID).Scan(&parentCrew, &parentLead, &parentTitle, &parentDesc, &parentPlan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("cartographer: parent mission %q not found", src.MissionID)
		}
		return "", "", fmt.Errorf("cartographer: load parent mission: %w", err)
	}

	newMissionID := newRandID("mis_")
	newTraceID := newRandID("tr_")
	newTitle := "Fork: " + parentTitle

	_, err = tx.ExecContext(ctx, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, plan)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?)`,
		newMissionID, workspaceID, parentCrew, parentLead, newTraceID, newTitle, parentDesc, parentPlan)
	if err != nil {
		return "", "", fmt.Errorf("cartographer: insert fork mission: %w", err)
	}

	// Copy tasks verbatim but with new IDs and mission_id pointed at fork.
	// We deliberately null out assignment_id on the fork — an in-flight
	// assignment belongs to the parent mission's conversation; the fork
	// owns a fresh slate.
	taskRows, err := tx.QueryContext(ctx, `SELECT id, assigned_agent_id, title, description, status, task_order, depends_on
		FROM mission_tasks WHERE mission_id = ? ORDER BY task_order, id`, src.MissionID)
	if err != nil {
		return "", "", fmt.Errorf("cartographer: read parent tasks: %w", err)
	}
	type taskRow struct {
		id, assignedAgent, title, description, status, dependsOn sql.NullString
		taskOrder                                                sql.NullInt64
	}
	var parentTasks []taskRow
	for taskRows.Next() {
		var tr taskRow
		if err = taskRows.Scan(&tr.id, &tr.assignedAgent, &tr.title, &tr.description, &tr.status, &tr.taskOrder, &tr.dependsOn); err != nil {
			_ = taskRows.Close()
			return "", "", err
		}
		parentTasks = append(parentTasks, tr)
	}
	if err = taskRows.Err(); err != nil {
		_ = taskRows.Close()
		return "", "", err
	}
	_ = taskRows.Close()

	// Two-pass copy. First pass mints a fresh id for every parent task and
	// records the parent→fork id mapping. Second pass inserts the rows with
	// depends_on remapped onto fork-side ids — without this remap, blocked
	// tasks on the fork wait forever for parent ids that don't exist here.
	idMap := make(map[string]string, len(parentTasks))
	for _, tr := range parentTasks {
		idMap[tr.id.String] = newRandID("mt_")
	}
	for _, tr := range parentTasks {
		newTaskID := idMap[tr.id.String]
		remappedDeps, remapErr := remapDepends(tr.dependsOn.String, idMap)
		if remapErr != nil {
			return "", "", fmt.Errorf("cartographer: remap deps for task %s: %w", tr.id.String, remapErr)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mission_tasks
			(id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newTaskID, newMissionID,
			nullableSQL(tr.assignedAgent),
			tr.title.String, nullableSQL(tr.description),
			defaultStatus(tr.status.String), tr.taskOrder.Int64,
			remappedDeps)
		if err != nil {
			return "", "", fmt.Errorf("cartographer: copy task %s: %w", tr.id.String, err)
		}
	}

	// New checkpoint pinned to the same cursor as the source, fork_of
	// wired back. We insert directly instead of calling Create so it
	// participates in the transaction.
	newCPID := newCheckpointID()
	stateCopy := src.State
	stateCopy.Meta = cloneMeta(src.State.Meta)
	stateJSON, err := marshalState(stateCopy)
	if err != nil {
		return "", "", err
	}
	label := newLabel
	if label == "" {
		label = "fork of " + src.ID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO checkpoints
		(id, workspace_id, crew_id, mission_id, label, journal_cursor, state_snapshot, fork_of, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newCPID, workspaceID, nullable(src.CrewID), newMissionID, label,
		src.JournalCursor, stateJSON, src.ID, nullable(userID))
	if err != nil {
		return "", "", fmt.Errorf("cartographer: insert fork checkpoint: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", "", fmt.Errorf("cartographer: commit fork: %w", err)
	}

	if j != nil {
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			CrewID:      src.CrewID,
			MissionID:   newMissionID,
			Type:        journal.EntryForkCreated,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     userID,
			Summary:     fmt.Sprintf("fork %s from checkpoint %s", newMissionID, src.ID),
			Refs: map[string]any{
				"parent_mission":    src.MissionID,
				"new_mission":       newMissionID,
				"fork_of":           src.ID,
				"new_checkpoint_id": newCPID,
				"journal_cursor":    src.JournalCursor,
			},
		})
	}

	return newMissionID, newCPID, nil
}

// newRandID generates a short prefixed random id for mission/task/trace
// rows. Keep this local instead of importing another package to avoid a
// cycle — cartographer is leaf-level.
func newRandID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

// nullableSQL adapts sql.NullString back to the driver's nullable wire
// form. A bit repetitive with `nullable(string)` but sql.NullString
// round-trips require a distinct helper.
func nullableSQL(n sql.NullString) any {
	if !n.Valid || n.String == "" {
		return nil
	}
	return n.String
}

// defaultStatus falls back to 'PENDING' if the parent row somehow had an
// empty status. We keep COMPLETED/whatever verbatim — see Fork doc for
// the rationale on not resetting finished tasks.
func defaultStatus(s string) string {
	if s == "" {
		return "PENDING"
	}
	return s
}

// remapDepends parses a depends_on JSON array of task ids, replaces each
// id with its fork-side counterpart from idMap, and re-marshals the
// result. Ids missing from the map (e.g. references to tasks deleted
// before the fork or external ids) are dropped — keeping a stale id
// would re-introduce the original "wait forever" bug.
func remapDepends(raw string, idMap map[string]string) (string, error) {
	if raw == "" {
		return "[]", nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "[]", nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if mapped, ok := idMap[id]; ok {
			out = append(out, mapped)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cloneMeta duplicates the map so the fork's snapshot can be mutated
// independently. Nested values are shared by reference — callers that
// need a deep clone should round-trip through JSON.
func cloneMeta(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// marshalState serializes a StateSnapshot for storage. Centralized here
// so Fork, Create, and any future writer agree on the on-disk form.
func marshalState(s StateSnapshot) (string, error) {
	if s.AgentMemory == nil {
		s.AgentMemory = map[string]string{}
	}
	if s.PendingTasks == nil {
		s.PendingTasks = []string{}
	}
	if s.OpenAssignments == nil {
		s.OpenAssignments = []string{}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("cartographer: marshal state: %w", err)
	}
	return string(b), nil
}
