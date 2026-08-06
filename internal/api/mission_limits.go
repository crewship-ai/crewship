package api

// Mission creation caps — how much work ONE agent-driven mission may set in
// motion, and how many such missions a crew may have live at once (#1768).
//
// WHY THIS FILE EXISTS. The #1768 autonomy matrix put mission_create at
// AutoLogInbox for `guided`, the DEFAULT autonomy level: an agent may plan a
// mission and it proceeds immediately, with a non-blocking notice. Part of the
// argument for that relaxation was that missions are bounded elsewhere. They
// were not. delegation_limits.go says so in as many words — the mission engine
// dispatches its task list through DispatchAssignment, which never passes
// through insertCappedAssignment — and assignments.go:344 says the same thing
// from the other side, including where the missing control belongs:
//
//	"Capping the mission path means capping task-list creation, which is a
//	 different control on a different door."
//
// This is that control. It is NOT a delegation cap moved onto the dispatch
// path: the delegation cap counts delegation HOPS, and a mission's tasks are
// not hops — they are one plan, authored in one call, by one agent.
//
// WHAT IS BOUNDED, AND WHY THESE TWO NUMBERS.
//
//   - mission.max_tasks bounds the WIDTH of one plan: how many mission_tasks
//     rows one POST /api/v1/internal/missions may carry. Each of those rows is
//     a future DispatchAssignment — a container start, a system prompt and a
//     model turn — so the task list is the fan-out, expressed up front.
//
//   - mission.max_active_per_crew bounds how many agent-authored missions a
//     crew may have in a non-terminal state. Without it the first cap buys
//     nothing: a loop that creates a hundred one-task missions costs the same
//     hundred runs as one hundred-task mission, and would sail past a
//     per-mission bound. This is the same reasoning that made the delegation
//     caps a PAIR rather than a depth limit alone.
//
// RECURSION IS REACHABLE, AND IT IS PROMPTED FOR. This was checked in the code
// before either number was chosen, because the answer decides the shape of the
// second cap. A mission that starts with no tasks makes the engine dispatch its
// lead as a LEAD-planning run (orchestrator/mission_tasks_planning.go
// dispatchLeadPlanning), and a LEAD-planning run is the one dispatch shape that
// keeps the sidecar — assignments_run.go:566 sets skipSidecar=false only for
// it. The planning brief that run receives then says, verbatim:
//
//	"Option B — If you prefer structured mission with dependencies:
//	   Create a new sub-mission with dependency DAG:
//	   curl -s -X POST http://localhost:9119/mission/create ..."
//
// So an agent executing a mission can create a mission, we ask it to, and the
// sub-mission it creates can itself be task-less and plan again. No bug and no
// compromise is required to recurse — only a lead that keeps taking Option B.
//
// That is why the second cap counts per CREW and not per lead agent. A
// per-agent budget is escaped by the next level of the recursion assigning its
// tasks to a different member; a crew-wide count is not, because the crew on
// this door is the token's crew binding (assertBoundCrewWorkspaceDB runs before
// the cap) and an agent cannot create a mission anywhere else. The recursion
// can still cross into another crew when a task is assigned to a linked crew's
// agent, and that crew then spends its own budget — bounded per crew, not
// globally. Stated rather than implied, for the reason delegation_limits.go
// states its own gaps: a cap believed to cover more than it does is worse than
// a missing one.
//
// WHICH DOORS THIS COVERS. One: InternalMissionHandler.Create, the backend
// adapter behind the sidecar's POST /mission/create. The human JWT door
// (MissionHandler.Create, POST /api/v1/crews/{crewId}/missions) is deliberately
// NOT capped — an operator with `create` role planning work is making a
// decision, not fanning out unattended, and that handler cannot carry a task
// list at all (it takes title + lead only). The cap therefore has nothing to
// say there. What a human creates DOES sit in the same table, and is
// deliberately not counted either: the budget bounds what agents set in motion,
// so a busy human board must not silently switch agent planning off.
//
// NOTHING HERE IS READ FROM THE REQUEST. The one quantity taken from the
// caller's payload is the length of the task list it actually sent — which is
// the thing being bounded, not a claim about it. There is no "max_tasks",
// "budget" or "depth" field on this route, so the /query failure
// delegation_limits.go was written against ("a cap on a number the agent writes
// is not a cap") has no analogue: raising the bound requires app_settings,
// which the agent cannot write.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
)

const (
	// SettingMissionMaxTasks bounds the task list of ONE agent-created
	// mission. 0 means "agent-created missions may carry no task list" —
	// planning still works, but every task has to arrive through /assign,
	// where the delegation caps apply.
	SettingMissionMaxTasks = "mission.max_tasks"

	// SettingMissionMaxActivePerCrew bounds how many agent-authored missions
	// one crew may have in a non-terminal state. 0 switches agent-driven
	// mission creation off for the instance — the single number an operator
	// needs for "no agent plans a mission", mirroring delegation.max_depth=0.
	SettingMissionMaxActivePerCrew = "mission.max_active_per_crew"
)

const (
	// 16 = twice defaultDelegationMaxFanout. A mission's task list is
	// legitimately wider than one turn's fan-out, because its tasks are
	// sequenced by depends_on rather than run at once — but not unboundedly
	// wider. For scale: the largest builtin workflow template is 4 steps
	// (orchestrator.BuiltinTemplates), the lead-planning brief's own SCALING
	// RULES top out at "COMPLEX: 2-4 agents", and the largest seeded crew has
	// 3 members. 16 is four times the biggest shape we ship and five times the
	// crew that would execute it, so no plan a legitimate crew authors is near
	// it, while a runaway list of hundreds is refused before a single row is
	// written.
	defaultMissionMaxTasks = 16

	// 6 live agent-authored missions per crew: two per member of the largest
	// seeded crew (3), and under the orchestrator's default MaxConcurrentRuns
	// of 8 — a crew cannot have more plans in flight than the instance will
	// give it concurrent runs, so the extra missions would only ever queue.
	//
	// Together the two bound one crew's agent-authored mission work at
	// 6 x 16 = 96 tasks in flight, the same order as the delegation caps'
	// 8 + 64 worst case. That parity is the point: the two controls guard the
	// same resource through different doors and should not disagree by an
	// order of magnitude about how much one crew may spend.
	defaultMissionMaxActivePerCrew = 6

	// Sanity ceilings. Past these the stored value is a typo rather than a
	// policy, and settingInt answers the compiled default instead of clamping
	// — same convention as the delegation caps and agentMinMemoryMB: a clamped
	// nonsense value is a gate running on a number nobody chose.
	maxMissionTasksCeiling  = 512
	maxMissionActiveCeiling = 128
)

// missionLimits is the live cap policy for agent-driven mission creation.
type missionLimits struct {
	MaxTasks         int
	MaxActivePerCrew int
}

// MissionLimits resolves the live mission caps. A nil or unreadable DB yields
// the compiled defaults, the same fail-to-default contract DelegationLimits and
// the admission limits use.
func MissionLimits(ctx context.Context, db *sql.DB) missionLimits {
	return missionLimits{
		MaxTasks:         settingInt(ctx, db, SettingMissionMaxTasks, defaultMissionMaxTasks, 0, maxMissionTasksCeiling),
		MaxActivePerCrew: settingInt(ctx, db, SettingMissionMaxActivePerCrew, defaultMissionMaxActivePerCrew, 0, maxMissionActiveCeiling),
	}
}

// missionAuthoredViaAgent is the provenance value the internal door stamps on
// every mission it writes, and therefore the rows the active-mission budget
// counts. The column and its CHECK are v108; the value is the one the internal
// ISSUE door already uses for the same "an agent asked for this" meaning
// (issue_create_core.go). Counting on it is what keeps a human's board from
// spending the agents' budget.
//
// Rows written before this stamp existed carry NULL and are not counted. That
// under-counts once, on an instance upgrading with agent-authored missions
// already live, and decays as those reach a terminal status — the alternative
// (counting every mission ever created in the crew) would have the cap refuse
// on a backlog it cannot attribute.
const missionAuthoredViaAgent = "agent_tool_call"

// missionLiveStatusFilter is the SQL fragment selecting the rows the budget
// counts: agent-authored orchestration missions in this crew that are not
// finished.
//
// Two details that are easy to get wrong and both matter:
//
//   - mission_type = 'orchestration'. The `missions` table also stores ISSUES
//     (mission_type='issue', migration v33-v41 + v129) — a board with 200 open
//     issues would otherwise read as 200 live missions and switch agent
//     planning off on any real workspace.
//   - the status test is a NOT IN over terminal statuses, not an IN over live
//     ones. A status this file has never heard of counts as live, so a new
//     mission state added elsewhere fails the cap CLOSED rather than silently
//     leaving the budget.
const missionLiveStatusFilter = `crew_id = ?
	   AND mission_type = 'orchestration'
	   AND authored_via = '` + missionAuthoredViaAgent + `'
	   AND status NOT IN ('COMPLETED','FAILED','CANCELLED','DONE')`

// missionRefusal is a mission cap saying no, in words the agent can act on and
// with the setting an operator would change named in the body.
type missionRefusal struct {
	msg     string
	Setting string
	Limit   int
}

func (e *missionRefusal) Error() string { return e.msg }

// countActiveAgentMissions returns how many agent-authored missions the crew
// already has live — the number the budget is compared against.
func countActiveAgentMissions(ctx context.Context, db *sql.DB, crewID string) (int, error) {
	if db == nil {
		return 0, nil
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM missions WHERE `+missionLiveStatusFilter, crewID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active agent missions: %w", err)
	}
	return n, nil
}

// activeMissionGuard returns the SQL predicate (and its arguments) that
// re-proves the crew's mission budget AT INSERT TIME.
//
// The pre-check in enforceMissionCaps exists to produce a good error; on its
// own it is a TOCTOU window, and "many creations at once" is not an exotic race
// here — a loop firing /mission/create is the exact shape the budget is for.
// SQLite evaluates this subquery and the INSERT under one write transaction, so
// two racing creations serialise and the second sees the first's row. Same
// argument insertCappedAssignment rests on.
//
// The predicate must stay identical to countActiveAgentMissions; they answer
// the same question at two moments.
func activeMissionGuard(crewID string, maxActive int) (string, []any) {
	return `(SELECT COUNT(*) FROM missions WHERE ` + missionLiveStatusFilter + `) < ?`,
		[]any{crewID, maxActive}
}

// enforceMissionCaps judges one agent-driven mission creation against the live
// caps and returns them, so the caller can re-prove the budget inside its
// INSERT (activeMissionGuard) instead of trusting this read to still hold.
//
// taskCount is the length of the list the caller actually sent — the quantity
// itself, not a number it asserted about itself.
//
// Fails CLOSED on a DB error: a cap that cannot read its own state has not
// established that this creation is inside it, and creating anyway is the
// unbounded behaviour this exists to end.
func enforceMissionCaps(ctx context.Context, db *sql.DB, crewID string, taskCount int) (missionLimits, error) {
	lim := MissionLimits(ctx, db)

	if taskCount > lim.MaxTasks {
		if lim.MaxTasks == 0 {
			return lim, &missionRefusal{
				Setting: SettingMissionMaxTasks,
				Limit:   0,
				msg: fmt.Sprintf(
					"mission task lists are switched off on this instance (%s = 0). "+
						"Create the mission without tasks and hand out the work with /assign, "+
						"or ask an operator to raise %s.",
					SettingMissionMaxTasks, SettingMissionMaxTasks),
			}
		}
		return lim, &missionRefusal{
			Setting: SettingMissionMaxTasks,
			Limit:   lim.MaxTasks,
			msg: fmt.Sprintf(
				"mission refused: the plan carries %d task(s) and the limit is %d per mission "+
					"(instance setting %s). Split the work into a smaller first mission and plan the "+
					"rest once it reports back — raising the limit is an operator action.",
				taskCount, lim.MaxTasks, SettingMissionMaxTasks),
		}
	}

	used, err := countActiveAgentMissions(ctx, db, crewID)
	if err != nil {
		return lim, err
	}
	if used >= lim.MaxActivePerCrew {
		if lim.MaxActivePerCrew == 0 {
			return lim, &missionRefusal{
				Setting: SettingMissionMaxActivePerCrew,
				Limit:   0,
				msg: fmt.Sprintf(
					"agent-created missions are switched off on this instance (%s = 0). "+
						"Do the work yourself or hand it out with /assign, and report that it needed "+
						"a mission — an operator raises %s to allow it.",
					SettingMissionMaxActivePerCrew, SettingMissionMaxActivePerCrew),
			}
		}
		return lim, &missionRefusal{
			Setting: SettingMissionMaxActivePerCrew,
			Limit:   lim.MaxActivePerCrew,
			msg: fmt.Sprintf(
				"mission refused: this crew already has %d of a maximum %d agent-created mission(s) "+
					"still running (instance setting %s). Finish or close one — read them with "+
					"/mission/<id> — before planning another, or do the work yourself.",
				used, lim.MaxActivePerCrew, SettingMissionMaxActivePerCrew),
		}
	}

	return lim, nil
}

// writeMissionCapRefusal answers a refused creation with the same structured
// shape gateInternalAction's 403 uses (itself modelled on agents_hire.go:418),
// so the CLI renders it the same way: a human-readable reason plus the machine
// fields naming what to change. `setting` is the instance setting an operator
// would raise — the mission-cap analogue of the gate's autonomy_level.
func writeMissionCapRefusal(w http.ResponseWriter, crewID string, e *missionRefusal) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":   "Mission creation refused by instance limit",
		"reason":  e.Error(),
		"crew_id": crewID,
		"setting": e.Setting,
		"limit":   strconv.Itoa(e.Limit),
	})
}
