import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"

import { activitySurface, workflowLabel, type ActivitySelection } from "@/lib/activity-selection"

// The four things /activity-new can be pointed at. The routine and the workflow
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

describe("activity-stream-view wiring", () => {
  const src = readFileSync(
    resolve(process.cwd(), "components/features/activity-new/activity-stream-view.tsx"),
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
  })

  it("anchors the topology card and the rail on the same derived surface", () => {
    expect(src).toMatch(/const surface = /)
    expect(src).toMatch(/const focus = surface\.focus/)
    expect(src).toMatch(/anchor=\{surface\.chainAnchor\}/)
    expect(src).toMatch(/selectedChain=\{surface\.chainAnchor\}/)
  })
})
