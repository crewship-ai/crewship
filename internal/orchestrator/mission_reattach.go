package orchestrator

// Boot-time mission recovery. runMissionLoop is an in-memory goroutine that
// only ever starts from StartMission, and StartMission is only called by API
// handlers — so a server restart leaves every IN_PROGRESS mission with no
// driver, permanently stuck. ReattachInProgressMissions is the boot-time
// scan (called from the server lifecycle, after orphaned-run recovery) that
// finds those missions and re-attaches an orchestration loop to each.
//
// Recovery of stranded *task* state is intentionally left to the loop
// itself: ResolveReadyTasks already self-heals BLOCKED tasks whose
// dependencies completed, and scheduleTask's PENDING→IN_PROGRESS
// compare-and-swap makes re-dispatch race-safe.

import (
	"context"
)

// ReattachInProgressMissions scans the missions table for rows stuck in
// IN_PROGRESS without a live orchestration loop and re-attaches one to each
// via StartMission. It returns the number of missions re-attached.
//
// Best-effort per mission: a mission that fails to start (e.g. its crew row
// was deleted underneath it) is logged and skipped so one bad row cannot
// strand the rest of the fleet. Missions already in the active map are
// skipped — StartMission is idempotent anyway, but skipping keeps the
// returned count honest.
func (e *MissionEngine) ReattachInProgressMissions(ctx context.Context) int {
	rows, err := e.db.QueryContext(ctx,
		`SELECT id FROM missions WHERE status = 'IN_PROGRESS' ORDER BY created_at ASC`)
	if err != nil {
		e.logger.Error("mission re-attach: scan IN_PROGRESS missions", "error", err)
		return 0
	}
	var ids []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			e.logger.Error("mission re-attach: scan row", "error", scanErr)
			continue
		}
		ids = append(ids, id)
	}
	iterErr := rows.Err()
	_ = rows.Close()
	if iterErr != nil {
		e.logger.Error("mission re-attach: iterate missions", "error", iterErr)
	}

	reattached := 0
	for _, id := range ids {
		e.mu.Lock()
		_, alreadyActive := e.active[id]
		stopping := e.stopping
		e.mu.Unlock()
		if stopping {
			return reattached
		}
		if alreadyActive {
			continue
		}
		if err := e.StartMission(ctx, id); err != nil {
			e.logger.Error("mission re-attach: start failed", "mission_id", id, "error", err)
			continue
		}
		e.logger.Info("re-attached orchestration loop to IN_PROGRESS mission", "mission_id", id)
		reattached++
	}
	return reattached
}
