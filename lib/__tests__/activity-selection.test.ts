import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"

import {
  ACTIVITY_HOME,
  ACTIVITY_MAX_DEPTH,
  activitySurface,
  activityTrail,
  backFrom,
  currentStop,
  jumpTo,
  openStop,
  selectStop,
  stopMatcher,
  workflowAnchor,
  workflowLabel,
  type ActivitySelection,
  type ActivityStop,
} from "@/lib/activity-selection"

// The four things /activity can be pointed at. The routine and the workflow
// are the pair from the reported screenshot: the chip read "routine: Normalize
// dates to ISO 8601" while the card below it still drew the
// on-close-file-followup chain.
const WORKFLOW: ActivitySelection = {
  kind: "workflow",
  id: "run_9f3",
  label: "on-close-file-followup",
}
const ROUTINE: ActivitySelection = {
  kind: "routine",
  id: "normalize-dates",
  label: "Normalize dates to ISO 8601",
}
const ISSUE: ActivitySelection = { kind: "issue", id: "mission_1", label: "ENG-4" }
const CREW: ActivitySelection = { kind: "crew", id: "crew_1", label: "Ops" }

const ALL: ActivitySelection[] = [WORKFLOW, ROUTINE, ISSUE, CREW]

describe("activitySurface", () => {
  it("draws no graph and applies no focus before anything is picked", () => {
    const s = activitySurface(null)
    expect(s.focus).toBeNull()
    expect(s.chainAnchor).toBeNull()
    expect(s.chainLabel).toBeNull()
    expect(s.chip).toBeNull()
  })

  it("takes the graph away when a routine is picked after a workflow", () => {
    // The reported repro, in the order the reader hit it.
    let selection: ActivitySelection | null = WORKFLOW
    expect(activitySurface(selection).chainAnchor).toBe("run_9f3")

    selection = ROUTINE
    const s = activitySurface(selection)
    expect(s.chainAnchor).toBeNull()
    expect(s.chainLabel).toBeNull()
    expect(s.focus).toEqual({
      kind: "routine",
      id: "normalize-dates",
      label: "Normalize dates to ISO 8601",
    })
    expect(s.chip?.label).toBe("routine: Normalize dates to ISO 8601 (loaded window)")
  })

  it("drops the entity focus when a workflow is picked after a routine", () => {
    let selection: ActivitySelection | null = ROUTINE
    expect(activitySurface(selection).focus).not.toBeNull()

    selection = WORKFLOW
    const s = activitySurface(selection)
    expect(s.focus).toBeNull()
    expect(s.chainAnchor).toBe("run_9f3")
    expect(s.chainLabel).toBe("on-close-file-followup")
  })

  it("never puts an entity focus and a chain on screen at the same time", () => {
    // Every pick, from every starting point: the surface answers with one of
    // the two, never both. This is the invariant the two useStates could not
    // hold.
    for (const first of ALL) {
      for (const second of ALL) {
        let selection: ActivitySelection | null = first
        selection = second
        const s = activitySurface(selection)
        const both = s.focus !== null && s.chainAnchor !== null
        expect(both, `${first.kind} then ${second.kind} showed both`).toBe(false)
        // …and it is never neither, or the pick did nothing visible.
        expect(
          s.focus !== null || s.chainAnchor !== null,
          `${first.kind} then ${second.kind} showed neither`,
        ).toBe(true)
      }
    }
  })

  it("shows exactly one clearable chip for whatever is selected", () => {
    for (const selection of ALL) {
      const chip = activitySurface(selection).chip
      expect(chip, `${selection.kind} had no chip`).not.toBeNull()
      expect(chip?.label).toContain(selection.label)
      expect(chip?.label.startsWith(`${selection.kind}: `)).toBe(true)
    }
    expect(activitySurface(null).chip).toBeNull()
  })

  it("counts a workflow as a view, not as a filter over the feed", () => {
    // emptyByFilters reads `narrows`: a workflow re-points the graph but the
    // journal carries no chain_origin, so the feed is not narrowed by it.
    // Counting it as a filter would make "none of them satisfies all N
    // filters" a lie.
    expect(activitySurface(WORKFLOW).chip?.narrows).toBe(false)
    expect(activitySurface(ROUTINE).chip?.narrows).toBe(true)
    expect(activitySurface(ISSUE).chip?.narrows).toBe(true)
    expect(activitySurface(CREW).chip?.narrows).toBe(true)
  })

  it("says which selections narrow the whole table and which only the loaded window", () => {
    // Carried over from the focus chips: a routine slug is not indexed, so its
    // narrowing only covers what was fetched, and the chip has always said so.
    expect(activitySurface(ROUTINE).chip?.label).toContain("(loaded window)")
    expect(activitySurface(ISSUE).chip?.label).toBe("issue: ENG-4")
    expect(activitySurface(CREW).chip?.label).toBe("crew: Ops")
    expect(activitySurface(WORKFLOW).chip?.label).toContain("on-close-file-followup")
  })

  it("keeps the graph's anchor and its heading from the same selection", () => {
    for (const selection of [...ALL, null]) {
      const s = activitySurface(selection)
      expect((s.chainAnchor === null) === (s.chainLabel === null)).toBe(true)
      if (s.chainAnchor !== null) {
        expect(s.chainAnchor).toBe(selection?.id)
        expect(s.chainLabel).toBe(selection?.label)
      }
    }
  })
})

describe("workflowLabel", () => {
  it("names the chain by its routine", () => {
    expect(workflowLabel({ routine_slug: "on-close-file-followup", started_by: "Rule: on close" })).toBe(
      "on-close-file-followup",
    )
  })

  it("falls back to whoever started it when the chain has no routine", () => {
    expect(workflowLabel({ started_by: "Pavel" })).toBe("Pavel")
  })

  it("never renders an empty heading for a chain the index no longer holds", () => {
    expect(workflowLabel(undefined)).toBe("this workflow")
    expect(workflowLabel({})).toBe("this workflow")
  })
})

/* ------------------------------------------------------------------ *
 *  The walk: one selection, one main column, and a way back
 * ------------------------------------------------------------------ */

// The three stops of the reported walk, in the kinds the chain graph actually
// emits (lib/trace/build-chain-graph: issue | routine | run | assignment |
// agent | inbox | automation, clicked as "kind:ref" by chain-canvas).
const STEP: ActivityStop = { kind: "run", id: "run_7c1", label: "step 2 · fetch" }
const AGENT: ActivityStop = { kind: "agent", id: "agent_ada", label: "ada" }

describe("the main column follows the current stop", () => {
  it("shows the overview when nothing is selected", () => {
    const s = activitySurface(currentStop(ACTIVITY_HOME))
    expect(s.main).toBe("overview")
    expect(s.chip).toBeNull()
  })

  it("gives a picked workflow the whole column, so the overview cannot sit under it", () => {
    // The report: picking a workflow re-pointed the graph and left the global
    // overview — "56 events · past 24 hours · every crew, agent, routine and
    // issue in one place" — underneath it, identical for every workflow.
    const path = selectStop(WORKFLOW)
    const s = activitySurface(currentStop(path))
    expect(s.main).toBe("workflow")
    expect(s.chainAnchor).toBe("run_9f3")
  })

  it("keeps an entity pick on the overview, narrowed — it is a lens, not a page", () => {
    for (const sel of [ROUTINE, ISSUE, CREW]) {
      const s = activitySurface(currentStop(selectStop(sel)))
      expect(s.main, `${sel.kind} changed the column`).toBe("overview")
      expect(s.focus?.id).toBe(sel.id)
    }
  })

  it("opens a graph node as its own column, named and counted as a filter", () => {
    const s = activitySurface(currentStop(openStop(selectStop(WORKFLOW), AGENT)))
    expect(s.main).toBe("node")
    expect(s.node).toEqual(AGENT)
    expect(s.chip).toEqual({ label: "agent: ada", narrows: true })
    // No graph under a node view, and no entity focus either — the node
    // narrows the window itself.
    expect(s.chainAnchor).toBeNull()
    expect(s.focus).toBeNull()
  })

  it("routes every stop kind to exactly one column", () => {
    const stops: ActivityStop[] = [WORKFLOW, ROUTINE, ISSUE, CREW, STEP, AGENT]
    for (const stop of stops) {
      const s = activitySurface(stop)
      expect(["overview", "workflow", "node"]).toContain(s.main)
      // The invariant the stacked page broke: the workflow column and the
      // overview column are alternatives, never a stack.
      expect(s.main === "workflow" && s.focus !== null).toBe(false)
      expect(s.main === "overview" && s.chainAnchor !== null).toBe(false)
    }
  })
})

describe("back returns where you came from", () => {
  it("walks workflow → node → node and unwinds one stop at a time", () => {
    let path = selectStop(WORKFLOW)
    path = openStop(path, STEP)
    path = openStop(path, ISSUE)
    expect(currentStop(path)).toEqual(ISSUE)

    path = backFrom(path)
    expect(currentStop(path)).toEqual(STEP)

    // Two backs from the issue = the workflow, not a fixed home.
    path = backFrom(path)
    expect(currentStop(path)).toEqual({ kind: "workflow", id: "run_9f3", label: "on-close-file-followup" })
    expect(activitySurface(currentStop(path)).main).toBe("workflow")

    path = backFrom(path)
    expect(currentStop(path)).toBeNull()
    expect(activitySurface(currentStop(path)).main).toBe("overview")
  })

  it("is a no-op at the overview rather than a dead click into nothing", () => {
    const back = backFrom(ACTIVITY_HOME)
    expect(back.stops).toEqual([])
    expect(currentStop(back)).toBeNull()
  })

  it("still returns to the previous stop once the path has been trimmed", () => {
    // At the depth cap the OLDEST stop is what falls off, never the one you
    // just came from — replacing the top would make back at the boundary
    // land somewhere you were never at.
    let path = selectStop(WORKFLOW)
    for (let i = 0; i < ACTIVITY_MAX_DEPTH + 4; i++) {
      path = openStop(path, { kind: "run", id: `run_${i}`, label: `run ${i}` })
    }
    const cameFrom = path.stops[path.stops.length - 2]
    expect(cameFrom).toBeDefined()
    expect(currentStop(backFrom(path))).toEqual(cameFrom)
  })
})

describe("the path is bounded", () => {
  it("never grows past the cap, however long the walk", () => {
    let path = selectStop(WORKFLOW)
    for (let i = 0; i < 200; i++) {
      path = openStop(path, { kind: "assignment", id: `asg_${i}`, label: `assignment ${i}` })
    }
    expect(path.stops.length).toBe(ACTIVITY_MAX_DEPTH)
    expect(path.dropped).toBe(200 + 1 - ACTIVITY_MAX_DEPTH)
    expect(activityTrail(path).truncated).toBe(true)
    // Bounded by forgetting the START of the walk, never by refusing to move:
    // the click just made is where the reader now is.
    expect(currentStop(path)).toEqual({ kind: "assignment", id: "asg_199", label: "assignment 199" })
  })

  it("does not count a re-click on the current stop as a step", () => {
    let path = openStop(selectStop(WORKFLOW), AGENT)
    path = openStop(path, { ...AGENT })
    expect(path.stops.length).toBe(2)
  })

  it("returns to a stop already on the path instead of stacking a second copy", () => {
    // workflow → issue → agent → issue is a loop; without this the depth (and
    // the breadcrumb) grows for as long as someone keeps clicking between two
    // nodes, and "back" starts replaying the loop.
    let path = selectStop(WORKFLOW)
    path = openStop(path, ISSUE)
    path = openStop(path, AGENT)
    path = openStop(path, { ...ISSUE })
    expect(path.stops.map((s) => s.id)).toEqual(["run_9f3", "mission_1"])
    expect(currentStop(backFrom(path))?.kind).toBe("workflow")
  })

  it("starts a fresh path when the rail picks something", () => {
    let deep = selectStop(WORKFLOW)
    deep = openStop(deep, STEP)
    deep = openStop(deep, AGENT)

    // The rail chooses which walk you are on, so it replaces the path: back
    // from a rail pick reaches the overview, never the walk you had left.
    const fresh = selectStop(ROUTINE)
    expect(fresh.stops).toEqual([ROUTINE])
    expect(fresh.dropped).toBe(0)
    expect(currentStop(backFrom(fresh))).toBeNull()
    // …and the walk it replaced was not mutated on the way out.
    expect(deep.stops.map((s) => s.kind)).toEqual(["workflow", "run", "agent"])
    expect(selectStop(null)).toEqual(ACTIVITY_HOME)
  })
})

describe("the trail says where you are and how you got there", () => {
  it("names every stop, marks the last as current, and roots at the overview", () => {
    const path = openStop(openStop(selectStop(WORKFLOW), STEP), AGENT)
    const trail = activityTrail(path)
    expect(trail.crumbs.map((c) => c.label)).toEqual([
      "Overview",
      "workflow: on-close-file-followup",
      "run: step 2 · fetch",
      "agent: ada",
    ])
    expect(trail.crumbs.map((c) => c.depth)).toEqual([0, 1, 2, 3])
    expect(trail.crumbs.filter((c) => c.current).map((c) => c.label)).toEqual(["agent: ada"])
    expect(trail.truncated).toBe(false)
  })

  it("marks the overview as current when nothing is selected", () => {
    const trail = activityTrail(ACTIVITY_HOME)
    expect(trail.crumbs).toEqual([{ label: "Overview", depth: 0, current: true }])
  })

  it("jumps back to any crumb by truncating to its depth", () => {
    const path = openStop(openStop(selectStop(WORKFLOW), STEP), AGENT)
    expect(currentStop(jumpTo(path, 1))?.kind).toBe("workflow")
    expect(currentStop(jumpTo(path, 2))).toEqual(STEP)
    expect(jumpTo(path, 0)).toEqual(ACTIVITY_HOME)
    // Out of range in either direction is clamped, not thrown.
    expect(jumpTo(path, -3)).toEqual(ACTIVITY_HOME)
    expect(currentStop(jumpTo(path, 99))).toEqual(AGENT)
  })
})

describe("the rail keeps naming the workflow you are inside", () => {
  it("holds the highlight while the walk goes deeper", () => {
    // Otherwise the rail reads "no workflow selected" three levels into one —
    // one screen, two answers to "what am I looking at", which is the whole
    // reason there is a single path here.
    let path = selectStop(WORKFLOW)
    expect(workflowAnchor(path)).toBe("run_9f3")
    path = openStop(path, STEP)
    path = openStop(path, AGENT)
    expect(workflowAnchor(path)).toBe("run_9f3")
    expect(activitySurface(currentStop(path)).chainAnchor).toBeNull()
  })

  it("drops it when the walk was never in a workflow, and when it ends", () => {
    expect(workflowAnchor(ACTIVITY_HOME)).toBeNull()
    expect(workflowAnchor(selectStop(ISSUE))).toBeNull()
    expect(workflowAnchor(openStop(selectStop(ISSUE), AGENT))).toBeNull()
    expect(workflowAnchor(backFrom(selectStop(WORKFLOW)))).toBeNull()
  })
})

describe("stopMatcher narrows the window to one node", () => {
  // Shaped like rows the journal actually holds: agent_id / crew_id /
  // mission_id are columns, everything else lives in payload or refs, and
  // producers disagree about which of the two they write.
  const rows = [
    { id: "1", agent_id: "agent_ada", payload: { run_id: "run_7c1" } },
    { id: "2", agent_id: "agent_bo", payload: { run_id: "run_7c1" } },
    { id: "3", agent_id: "agent_bo", refs: { assignment_id: "asg_1" } },
    { id: "4", crew_id: "crew_1", payload: { step_id: "fetch" } },
    { id: "5", mission_id: "mission_1", payload: { step: "fetch" } },
    { id: "6", payload: { inbox_id: "inbox_9" } },
  ]
  const matched = (stop: ActivityStop) => rows.filter(stopMatcher(stop)).map((r) => r.id)

  it("matches an agent on the column, not on a payload key it does not carry", () => {
    expect(matched(AGENT)).toEqual(["1"])
  })

  it("matches a run on the payload, across the rows that share it", () => {
    expect(matched(STEP)).toEqual(["1", "2"])
  })

  it("reads refs as well as payload, because producers write either", () => {
    expect(matched({ kind: "assignment", id: "asg_1", label: "asg" })).toEqual(["3"])
  })

  it("matches a step under both spellings the spine builds from", () => {
    expect(matched({ kind: "step", id: "fetch", label: "fetch" })).toEqual(["4", "5"])
  })

  it("falls back to <kind>_id for a kind nobody has taught it yet", () => {
    // The graph gained `inbox` and `automation` after this module was
    // written; a new kind must narrow to something, not to everything.
    expect(matched({ kind: "inbox", id: "inbox_9", label: "Inbox" })).toEqual(["6"])
    expect(matched({ kind: "automation", id: "auto_x", label: "Rule" })).toEqual([])
  })

  it("does not match a row that merely mentions the id under another key", () => {
    expect(stopMatcher(AGENT)({ payload: { parent_agent_id: "agent_ada" } })).toBe(false)
  })
})

describe("activity-stream-view wiring", () => {
  const src = readFileSync(
    resolve(process.cwd(), "components/features/activity-stream/activity-stream-view.tsx"),
    "utf8",
  )

  it("finds the view at all (guards against a moved file)", () => {
    expect(src).toContain("export function ActivityStreamView")
  })

  it("keeps no chain state of its own beside the selection", () => {
    // The defect was a second useState the selection could not reach. One
    // source of truth or the screen gets two answers again.
    expect(src).not.toMatch(/setSelectedChain/)
    expect(src).not.toMatch(/setFocus\b/)
    // …and the walk did not reintroduce one: the path IS the selection.
    expect(src).not.toMatch(/useState<ActivitySelection/)
  })

  it("derives the rail, the query and the column from the same path", () => {
    expect(src).toMatch(/const surface = /)
    expect(src).toMatch(/const focus = surface\.focus/)
    expect(src).toMatch(/selectedChain=\{railChain\}/)
    expect(src).toMatch(/workflowAnchor\(path\)/)
    expect(src).toMatch(/currentStop\(path\)/)
  })

  it("renders the overview only when the column is the overview", () => {
    // The report: a workflow and the global overview on one screen. The
    // guard is a single derived boolean so the two cannot both be true.
    expect(src).toMatch(/const overviewShown =[\s\S]{0,200}?surface\.main === "overview"/)
    expect(src).toMatch(/\{overviewShown && \(\s*<ActivityOverview/)
  })

  it("hands the workflow column to WorkflowPage with a way back and a way down", () => {
    expect(src).toMatch(/<WorkflowPage/)
    expect(src).toMatch(/onBack=\{/)
    expect(src).toMatch(/onOpenNode=\{/)
  })
})
