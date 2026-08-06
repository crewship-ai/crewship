package api

// Mission creation caps — the tests that define them.
//
// Written as security tests, not feature tests: the interesting cases are the
// ones where the caller is trying to get past the bound. Five properties:
//
//  1. a task list wider than the limit is refused, and one task narrower is not;
//  2. a crew already holding its budget of live agent missions is refused, and
//     freeing one lets the identical call through — so the loop that creates a
//     hundred one-task missions is bounded even though each is tiny;
//  3. the refusal names the instance setting an operator would change, in the
//     structured shape the autonomy gate's 403 uses;
//  4. changing the setting changes the answer on the NEXT call — the caps are
//     read live from app_settings, not snapshotted at boot;
//  5. nothing the agent puts in the request body can raise its own cap.
//
// Every boundary test carries its own mutation: the same request refused at the
// configured limit is accepted once the setting moves by one. A cap that
// refused unconditionally would pass (1) and (2) and fail those.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// missionCapFixture is one crew with a lead. Mission rows are added per test to
// shape the crew's live-mission budget.
type missionCapFixture struct {
	db     *sql.DB
	h      *InternalMissionHandler
	wsID   string
	crewID string
	lead   string
}

func setupMissionCapFixture(t *testing.T) *missionCapFixture {
	t.Helper()
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "mcap-crew", wsID, "Crew", "mcap-crew")
	lead := seedAgentRow(t, db, "mcap-lead", wsID, crewID, "Lead", "mcap-lead", "LEAD")
	return &missionCapFixture{
		db:     db,
		h:      NewInternalMissionHandler(db, nil, nil, newTestLogger()),
		wsID:   wsID,
		crewID: crewID,
		lead:   lead,
	}
}

func (f *missionCapFixture) setLimit(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := f.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, fmt.Sprint(v)); err != nil {
		t.Fatalf("set %s=%d: %v", key, v, err)
	}
}

// createMission drives POST /api/v1/internal/missions exactly as the sidecar
// does. extra is merged into the JSON body so a test can try to smuggle fields.
//
// With no policy resolver wired the autonomy gate uses its conservative guided
// default, so an ACCEPTED creation answers 202 (created-but-held), not 201.
func (f *missionCapFixture) createMission(t *testing.T, title string, taskCount int, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	tasks := make([]map[string]any, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		tasks = append(tasks, map[string]any{"title": fmt.Sprintf("t%d", i), "task_order": i})
	}
	body := map[string]any{
		"title":         title,
		"lead_agent_id": f.lead,
		"crew_id":       f.crewID,
		"workspace_id":  f.wsID,
		"tasks":         tasks,
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/missions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.Create(w, req)
	return w
}

// seedLiveAgentMission writes a mission row shaped exactly like one this door
// creates: agent-authored, orchestration, non-terminal.
func (f *missionCapFixture) seedLiveAgentMission(t *testing.T, id, status string) {
	t.Helper()
	if _, err := f.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status,
		                      mission_type, authored_via, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'seeded', ?, 'orchestration', 'agent_tool_call', datetime('now'), datetime('now'))`,
		id, f.wsID, f.crewID, f.lead, "tr-"+id, status); err != nil {
		t.Fatalf("seed mission %s: %v", id, err)
	}
}

func (f *missionCapFixture) missionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE crew_id = ?`, f.crewID).Scan(&n); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	return n
}

// refusalField reads one field out of the structured 403 body.
func refusalField(t *testing.T, w *httptest.ResponseRecorder, key string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	v, _ := payload[key].(string)
	return v
}

// A plan wider than the limit is refused, and nothing is written. One task
// narrower goes through, so the bound is a boundary and not a blanket no.
func TestMissionCaps_TaskListBeyondTheLimitIsRefused(t *testing.T) {
	f := setupMissionCapFixture(t)
	f.setLimit(t, SettingMissionMaxTasks, 3)

	w := f.createMission(t, "too wide", 4, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("4 tasks past a limit of 3: expected 403, got %d (%s)", w.Code, w.Body.String())
	}
	if n := f.missionCount(t); n != 0 {
		t.Errorf("refused creation still wrote %d mission row(s)", n)
	}
	var tasks int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM mission_tasks`).Scan(&tasks); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if tasks != 0 {
		t.Errorf("refused creation still wrote %d mission_task row(s)", tasks)
	}

	// Just under the limit is accepted — otherwise the test above is satisfied
	// by a handler that refuses everything.
	if w := f.createMission(t, "just under", 3, nil); w.Code != http.StatusAccepted {
		t.Fatalf("3 tasks at a limit of 3 must be accepted, got %d (%s)", w.Code, w.Body.String())
	}

	// Mutation: one more task of headroom and the identical call goes through,
	// on the next call, with nothing restarted.
	f.setLimit(t, SettingMissionMaxTasks, 4)
	if w := f.createMission(t, "too wide", 4, nil); w.Code != http.StatusAccepted {
		t.Fatalf("max_tasks=4 must accept the 4-task plan, got %d (%s)", w.Code, w.Body.String())
	}
}

// The refusal has to name the setting an operator would change, in the machine
// fields the CLI already renders for the autonomy gate's 403.
func TestMissionCaps_RefusalNamesTheSetting(t *testing.T) {
	t.Run("task list", func(t *testing.T) {
		f := setupMissionCapFixture(t)
		f.setLimit(t, SettingMissionMaxTasks, 2)

		w := f.createMission(t, "wide", 5, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
		}
		if got := refusalField(t, w, "setting"); got != SettingMissionMaxTasks {
			t.Errorf("setting field = %q, want %q", got, SettingMissionMaxTasks)
		}
		if got := refusalField(t, w, "limit"); got != "2" {
			t.Errorf("limit field = %q, want \"2\"", got)
		}
		if got := refusalField(t, w, "crew_id"); got != f.crewID {
			t.Errorf("crew_id field = %q, want %q", got, f.crewID)
		}
		reason := refusalField(t, w, "reason")
		for _, want := range []string{"5", "2", SettingMissionMaxTasks} {
			if !strings.Contains(reason, want) {
				t.Errorf("reason must name %q so the agent can report it; got %q", want, reason)
			}
		}
	})

	t.Run("crew budget", func(t *testing.T) {
		f := setupMissionCapFixture(t)
		f.setLimit(t, SettingMissionMaxActivePerCrew, 1)
		f.seedLiveAgentMission(t, "live-1", "IN_PROGRESS")

		w := f.createMission(t, "one too many", 1, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
		}
		if got := refusalField(t, w, "setting"); got != SettingMissionMaxActivePerCrew {
			t.Errorf("setting field = %q, want %q", got, SettingMissionMaxActivePerCrew)
		}
		if reason := refusalField(t, w, "reason"); !strings.Contains(reason, SettingMissionMaxActivePerCrew) {
			t.Errorf("reason must name the setting; got %q", reason)
		}
	})
}

// The per-mission task cap does nothing against a loop that creates a hundred
// one-task missions. The crew budget is what bounds that, and it counts only
// what is still live — finishing one frees the slot.
func TestMissionCaps_CrewBudgetBoundsManySmallMissions(t *testing.T) {
	f := setupMissionCapFixture(t)
	f.setLimit(t, SettingMissionMaxActivePerCrew, 2)

	if w := f.createMission(t, "m1", 1, nil); w.Code != http.StatusAccepted {
		t.Fatalf("first mission: got %d (%s)", w.Code, w.Body.String())
	}
	if w := f.createMission(t, "m2", 1, nil); w.Code != http.StatusAccepted {
		t.Fatalf("second mission: got %d (%s)", w.Code, w.Body.String())
	}
	before := f.missionCount(t)
	if w := f.createMission(t, "m3", 1, nil); w.Code != http.StatusForbidden {
		t.Fatalf("third mission past a budget of 2: expected 403, got %d (%s)", w.Code, w.Body.String())
	}
	if got := f.missionCount(t); got != before {
		t.Errorf("refused creation still wrote %d mission row(s)", got-before)
	}

	// A finished mission frees the slot: the budget is on concurrency, not on
	// lifetime output — same shape as a lead's fan-out in a long chat.
	if _, err := f.db.Exec(`UPDATE missions SET status='COMPLETED' WHERE title='m1'`); err != nil {
		t.Fatalf("complete m1: %v", err)
	}
	if w := f.createMission(t, "m3", 1, nil); w.Code != http.StatusAccepted {
		t.Fatalf("expected acceptance once a slot freed, got %d (%s)", w.Code, w.Body.String())
	}

	// Mutation: raising the budget admits the next one immediately.
	if w := f.createMission(t, "m4", 1, nil); w.Code != http.StatusForbidden {
		t.Fatalf("budget should be full again, got %d (%s)", w.Code, w.Body.String())
	}
	f.setLimit(t, SettingMissionMaxActivePerCrew, 3)
	if w := f.createMission(t, "m4", 1, nil); w.Code != http.StatusAccepted {
		t.Fatalf("max_active_per_crew=3 must accept it on the next call, got %d (%s)", w.Code, w.Body.String())
	}
}

// The caps come from app_settings and are read per decision. Pinned separately
// from the boundary tests because "live" is the property that makes
// `crewship instance settings set` an operator control rather than a restart.
func TestMissionCaps_SettingChangeAppliesToTheNextCall(t *testing.T) {
	f := setupMissionCapFixture(t)

	// Default policy admits a 5-task plan.
	if w := f.createMission(t, "before", 5, nil); w.Code != http.StatusAccepted {
		t.Fatalf("default limits must admit a 5-task plan, got %d (%s)", w.Code, w.Body.String())
	}
	// Lowering the setting bites on the very next call, with the same handler
	// and the same DB handle.
	f.setLimit(t, SettingMissionMaxTasks, 4)
	if w := f.createMission(t, "after", 5, nil); w.Code != http.StatusForbidden {
		t.Fatalf("lowered limit must apply to the next call, got %d (%s)", w.Code, w.Body.String())
	}
}

// A value the agent supplies cannot raise its own cap. There is no cap field on
// this route, so the test's job is to prove that stays true: every plausible
// name for one is ignored, and the refusal still reports the SERVER's number.
func TestMissionCaps_AgentCannotRaiseItsOwnCap(t *testing.T) {
	smuggled := []struct {
		name  string
		extra map[string]any
	}{
		{"setting key verbatim", map[string]any{SettingMissionMaxTasks: 99}},
		{"max_tasks", map[string]any{"max_tasks": 99}},
		{"limit", map[string]any{"limit": 99, "limits": map[string]any{"max_tasks": 99}}},
		{"budget", map[string]any{"budget": 99, "max_active_per_crew": 99}},
		{"nested policy object", map[string]any{"policy": map[string]any{"mission": map[string]any{"max_tasks": 99}}}},
	}
	for _, tc := range smuggled {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMissionCapFixture(t)
			f.setLimit(t, SettingMissionMaxTasks, 2)

			w := f.createMission(t, "smuggle", 5, tc.extra)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s raised the cap: %d (%s)", tc.name, w.Code, w.Body.String())
			}
			if got := refusalField(t, w, "limit"); got != "2" {
				t.Errorf("refusal reports limit %q — the agent's number reached the cap", got)
			}
			if n := f.missionCount(t); n != 0 {
				t.Errorf("%s wrote %d mission row(s)", tc.name, n)
			}
		})
	}

	// Same for the crew budget: a body-supplied budget must not buy a slot.
	t.Run("crew budget", func(t *testing.T) {
		f := setupMissionCapFixture(t)
		f.setLimit(t, SettingMissionMaxActivePerCrew, 1)
		f.seedLiveAgentMission(t, "live-1", "PLANNING")

		w := f.createMission(t, "smuggle", 1, map[string]any{
			SettingMissionMaxActivePerCrew: 99, "max_active_per_crew": 99, "active": 0,
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("body-supplied budget got past the cap: %d (%s)", w.Code, w.Body.String())
		}
	})
}

// The budget counts agent-authored ORCHESTRATION missions. Two neighbours share
// the `missions` table and must not spend it: issues (mission_type='issue' — a
// board with 200 open issues would otherwise switch agent planning off), and
// missions a human created (authored_via NULL).
func TestMissionCaps_IssuesAndHumanMissionsDoNotSpendTheBudget(t *testing.T) {
	f := setupMissionCapFixture(t)
	f.setLimit(t, SettingMissionMaxActivePerCrew, 1)

	for i := 0; i < 5; i++ {
		if _, err := f.db.Exec(`
			INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status,
			                      mission_type, authored_via, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'issue', 'TODO', 'issue', 'agent_tool_call', datetime('now'), datetime('now'))`,
			fmt.Sprintf("iss-%d", i), f.wsID, f.crewID, f.lead, fmt.Sprintf("tr-iss-%d", i)); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
	}
	if _, err := f.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status,
		                      mission_type, created_at, updated_at)
		VALUES ('human-1', ?, ?, ?, 'tr-human-1', 'human plan', 'IN_PROGRESS', 'orchestration', datetime('now'), datetime('now'))`,
		f.wsID, f.crewID, f.lead); err != nil {
		t.Fatalf("seed human mission: %v", err)
	}

	if w := f.createMission(t, "agent plan", 1, nil); w.Code != http.StatusAccepted {
		t.Fatalf("issues and human missions must not spend the agent budget, got %d (%s)", w.Code, w.Body.String())
	}
	// ...and the one the agent just created does, so the filter is not simply
	// counting nothing.
	if w := f.createMission(t, "agent plan 2", 1, nil); w.Code != http.StatusForbidden {
		t.Fatalf("the agent's own live mission must spend the budget, got %d (%s)", w.Code, w.Body.String())
	}
}

// The budget has to hold when the creations arrive together — which is the only
// way a loop ever arrives. A read-then-write check admits all of them; the
// predicate rides inside the INSERT precisely so it cannot.
func TestMissionCaps_CrewBudgetHoldsUnderConcurrentCreation(t *testing.T) {
	f := setupMissionCapFixture(t)
	const budget = 3
	f.setLimit(t, SettingMissionMaxActivePerCrew, budget)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f.createMission(t, fmt.Sprintf("race-%d", i), 1, nil)
		}(i)
	}
	wg.Wait()

	var live int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE crew_id = ? AND mission_type = 'orchestration'
		   AND authored_via = 'agent_tool_call'
		   AND status NOT IN ('COMPLETED','FAILED','CANCELLED','DONE')`, f.crewID).Scan(&live); err != nil {
		t.Fatalf("count live missions: %v", err)
	}
	if live > budget {
		t.Fatalf("%d missions admitted past a budget of %d — the cap is a read-then-write race", live, budget)
	}
	if live == 0 {
		t.Fatal("no creation got through at all; the test proves nothing about the boundary")
	}
}

// Zero is an operator's off switch on both axes, and a cap whose floor is 1
// cannot express one.
func TestMissionCaps_ZeroSwitchesTheDoorOff(t *testing.T) {
	t.Run("max_active_per_crew=0 refuses every agent mission", func(t *testing.T) {
		f := setupMissionCapFixture(t)
		f.setLimit(t, SettingMissionMaxActivePerCrew, 0)
		w := f.createMission(t, "m", 0, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
		}
		if reason := refusalField(t, w, "reason"); !strings.Contains(reason, SettingMissionMaxActivePerCrew) {
			t.Errorf("the off-switch refusal must name its setting; got %q", reason)
		}
	})

	t.Run("max_tasks=0 still allows a task-less mission", func(t *testing.T) {
		f := setupMissionCapFixture(t)
		f.setLimit(t, SettingMissionMaxTasks, 0)
		if w := f.createMission(t, "m", 1, nil); w.Code != http.StatusForbidden {
			t.Fatalf("one task past max_tasks=0: expected 403, got %d (%s)", w.Code, w.Body.String())
		}
		if w := f.createMission(t, "m", 0, nil); w.Code != http.StatusAccepted {
			t.Fatalf("a task-less mission must still be creatable, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// Defaults are a shipped policy, not an implementation detail: they are what
// every instance that never touches a setting runs on.
func TestMissionLimits_Defaults(t *testing.T) {
	db := setupTestDB(t)
	lim := MissionLimits(context.Background(), db)
	if lim.MaxTasks != defaultMissionMaxTasks || lim.MaxActivePerCrew != defaultMissionMaxActivePerCrew {
		t.Fatalf("defaults = tasks %d / active %d, want %d / %d",
			lim.MaxTasks, lim.MaxActivePerCrew, defaultMissionMaxTasks, defaultMissionMaxActivePerCrew)
	}
	// The defaults must not regress a legitimate crew: the widest workflow
	// template we ship is 4 steps, and the seeded crews are 1-3 agents.
	if lim.MaxTasks < 4 {
		t.Errorf("max_tasks default %d refuses the widest builtin workflow template (4 steps)", lim.MaxTasks)
	}
	if lim.MaxActivePerCrew < 1 {
		t.Error("a default of 0 would ship agent mission creation switched off")
	}
	// Out-of-range and unparseable values answer the compiled default rather
	// than being clamped — same convention as the delegation caps.
	for _, bad := range []string{"-1", "0.5", "not-a-number", fmt.Sprint(maxMissionTasksCeiling + 1)} {
		if _, err := db.Exec(
			`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, SettingMissionMaxTasks, bad); err != nil {
			t.Fatalf("set bad value %q: %v", bad, err)
		}
		if got := MissionLimits(context.Background(), db).MaxTasks; got != defaultMissionMaxTasks {
			t.Errorf("value %q yielded max_tasks %d, want the compiled default %d", bad, got, defaultMissionMaxTasks)
		}
	}
}
