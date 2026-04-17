package orchestrator

import (
	"fmt"
	"strings"
	"testing"
)

// These benchmarks simulate the dispatchLeadPlanning prompt-building loop
// both before and after the fix. The real function pulls title / desc from
// sqlite and dispatches an assignment, which we skip — the target is the
// string-building allocation shape.

const (
	benchLeadTitle    = "Bootstrap the new onboarding flow"
	benchLeadDesc     = "Implement the missing pieces of the user onboarding wizard so a fresh user can land on /onboarding, authenticate, pick a plan, and complete in under three minutes."
	benchLeadMissionID = "m_abcdef1234567890abcdef"
)

func BenchmarkLeadPlanningPrompt_Sprintf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sb strings.Builder
		sb.WriteString("[MISSION PLANNING REQUEST]\n")
		sb.WriteString("You are the Lead agent for this crew. A new mission has been assigned to you WITHOUT pre-defined tasks.\n")
		sb.WriteString("Your job is to analyze the objective, break it down into concrete tasks, and assign them to your crew members.\n\n")
		sb.WriteString(fmt.Sprintf("Mission: %s\n", benchLeadTitle))
		sb.WriteString(fmt.Sprintf("Description: %s\n", benchLeadDesc))
		sb.WriteString(fmt.Sprintf("Mission ID: %s\n\n", benchLeadMissionID))
		sb.WriteString(leadPlanningStaticTailForBench)
		_ = sb.String()
	}
}

func BenchmarkLeadPlanningPrompt_Fprintf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sb strings.Builder
		sb.Grow(3072)
		sb.WriteString("[MISSION PLANNING REQUEST]\n")
		sb.WriteString("You are the Lead agent for this crew. A new mission has been assigned to you WITHOUT pre-defined tasks.\n")
		sb.WriteString("Your job is to analyze the objective, break it down into concrete tasks, and assign them to your crew members.\n\n")
		fmt.Fprintf(&sb, "Mission: %s\n", benchLeadTitle)
		fmt.Fprintf(&sb, "Description: %s\n", benchLeadDesc)
		fmt.Fprintf(&sb, "Mission ID: %s\n\n", benchLeadMissionID)
		sb.WriteString(leadPlanningStaticTailForBench)
		_ = sb.String()
	}
}

// leadPlanningStaticTailForBench mirrors the static cheat-sheet the real
// function appends after the dynamic Mission/Description/ID lines.
const leadPlanningStaticTailForBench = `SCALING RULES — classify before planning:
  SIMPLE  (fact-finding, single op):    1 agent, 3-10 tool calls, ~5 min
  MEDIUM  (multi-step, 1-2 files):      1-2 agents, 10-15 tool calls, ~15 min
  COMPLEX (research, multi-file):        2-4 agents, 15+ tool calls, ~30 min
Match effort to complexity. Do NOT create missions for SIMPLE tasks — use /assign directly.

INSTRUCTIONS:
1. Assess mission complexity (SIMPLE/MEDIUM/COMPLEX) first
2. Review the mission objective and your crew members' capabilities
3. Break the work into specific, actionable tasks
4. Assign each task to the most suitable crew member (or yourself if solo)
5. Define task dependencies (which tasks must complete before others start)
6. Create the tasks using the mission API:

Option A — Add tasks to this existing mission:
  For each task, run:
  curl -s -X POST http://localhost:9119/assign \
    -H 'Content-Type: application/json' \
    -d '{"target":"<agent_slug>","task":"<detailed task description>"}'

Option B — If you prefer structured mission with dependencies:
  Create a new sub-mission with dependency DAG:
  curl -s -X POST http://localhost:9119/mission/create \
    -H 'Content-Type: application/json' \
    -d '{"title":"...","tasks":[...]}'
  Then start it: curl -s -X POST http://localhost:9119/mission/<id>/start

Option C — If you can handle this yourself (solo crew / simple task):
  Just do the work directly and produce the result.

After creating tasks or completing the work, the system will handle the rest.
[END PLANNING REQUEST]`
