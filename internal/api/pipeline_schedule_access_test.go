package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type countingRowQuerier struct {
	q pipeline.RowQuerier
	n *int
}

func (c countingRowQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	*c.n++
	return c.q.QueryRowContext(ctx, query, args...)
}

// One list reads the flag once and each creator's standing once, not once
// per row.
func TestScheduleCreator_ListReadsTheFlagAndEachStandingOnce(t *testing.T) {
	h, db, ownerID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_n", "n-routine")
	mustExec(t, db, `INSERT INTO users(id,email) VALUES('u_mgr','mgr@ex.com')`)
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('wm_mgr',?,'u_mgr','MANAGER')`, wsID)
	mustExec(t, db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('n-flag','ff_artifact_agent_access_modes',?,1)`, wsID)
	for i, who := range [][2]string{{ownerID, "OWNER"}, {ownerID, "OWNER"}, {ownerID, "OWNER"}, {"u_mgr", "MANAGER"}, {"u_mgr", "MANAGER"}} {
		rr := httptest.NewRecorder()
		h.CreateSchedule(rr, asPerson(httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"cron_expr":"0 * * * *","target_pipeline_slug":"n-routine","name":"s%d"}`, i))), who[0], wsID, who[1]))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rr.Code, rr.Body)
		}
	}
	flags, standings := 0, 0
	origFlag, origAccess := scheduleArtifactFlag, newScheduleAccess
	t.Cleanup(func() { scheduleArtifactFlag, newScheduleAccess = origFlag, origAccess })
	scheduleArtifactFlag = func(ctx context.Context, q featureflags.RowQuerier, ws, key string) (bool, error) {
		flags++
		return origFlag(ctx, q, ws, key)
	}
	newScheduleAccess = func(q pipeline.RowQuerier, ws string) *pipeline.ScheduleAccess {
		return pipeline.NewScheduleAccess(countingRowQuerier{q, &standings}, ws)
	}
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, withWorkspaceUser(httptest.NewRequest("GET", "/", nil), ownerID, wsID, "OWNER"))
	var rows []scheduleResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &rows)
	if len(rows) != 5 {
		t.Fatalf("list: %s", rr.Body)
	}
	for _, r := range rows {
		if r.ArtifactAccess != "active" {
			t.Fatalf("row %s: %q", r.ID, r.ArtifactAccess)
		}
	}
	if flags != 1 || standings != 2 {
		t.Fatalf("one list of 5 schedules by 2 creators read the flag %d times and standing %d times, want 1 and 2", flags, standings)
	}
}
