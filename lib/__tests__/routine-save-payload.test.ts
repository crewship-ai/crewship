import { describe, it, expect } from "vitest"

import { renamePayload, duplicatePayload, slugify } from "@/lib/routine-save-payload"

// The kebab's Rename shipped posting `{definition, skip_test_gate}`.
// The endpoint requires slug + name, so every save 400'd with "Save
// failed" — and the one that landed came back with an empty `name`
// column, so the page titled itself with the slug.
//
// These assert the body field by field, because "it posts to the right
// URL" was exactly the level of confidence that produced the bug.

const routine = {
  slug: "classify-ticket",
  name: "Classify support ticket",
  description: "Classify a ticket into fixed label sets.",
  author_crew_id: "crew-1",
  definition: {
    name: "classify-ticket",
    display_name: "Classify support ticket",
    description: "Classify a ticket into fixed label sets.",
    steps: [{ id: "a", type: "agent_run" }],
  },
}

describe("renamePayload", () => {
  const body = renamePayload(routine, { name: "Ticket triage", description: "Sorts tickets." })

  it("carries every field the endpoint requires", () => {
    expect(body.slug).toBe("classify-ticket")
    expect(body.name).toBe("Ticket triage")
    expect(body.description).toBe("Sorts tickets.")
    expect(body.definition).toBeTruthy()
    expect(body.author_crew_id).toBe("crew-1")
    expect(body.skip_test_gate).toBe(true)
  })

  it("writes the title to BOTH the column and the definition", () => {
    // The page renders from the `name` column; the DSL carries
    // display_name. Updating one and not the other is how the heading
    // came to show a slug.
    expect(body.name).toBe("Ticket triage")
    expect(body.definition.display_name).toBe("Ticket triage")
  })

  it("never moves the routine's identity", () => {
    // definition.name is what the slug derives from.
    expect(body.definition.name).toBe("classify-ticket")
    expect(body.slug).toBe("classify-ticket")
  })

  it("leaves the steps untouched", () => {
    expect(body.definition.steps).toEqual(routine.definition.steps)
  })

  it("does not send an approved routine back for review over a title", () => {
    expect(body.skip_governance_gate).toBe(true)
  })

  it("trims what the user typed", () => {
    const t = renamePayload(routine, { name: "  Spaced  ", description: "  desc  " })
    expect(t.name).toBe("Spaced")
    expect(t.description).toBe("desc")
  })
})

describe("duplicatePayload", () => {
  const body = duplicatePayload(routine, { name: "Classify support ticket (copy)" })

  it("gives the copy its own identity", () => {
    expect(body.slug).toBe("classify-support-ticket-copy")
    expect(body.definition.name).toBe("classify-support-ticket-copy")
    expect(body.name).toBe("Classify support ticket (copy)")
  })

  it("does not overwrite the original", () => {
    expect(body.slug).not.toBe(routine.slug)
    expect(body.definition.name).not.toBe(routine.definition.name)
  })

  it("lets the governance gate judge the copy on its own merits", () => {
    // A duplicate is a NEW routine. Waving it through because the
    // original was approved would make Duplicate a way around review.
    expect(body.skip_governance_gate).toBeUndefined()
  })

  it("keeps the steps", () => {
    expect(body.definition.steps).toEqual(routine.definition.steps)
  })
})

describe("slugify", () => {
  it("lowercases and hyphenates", () => {
    expect(slugify("Monthly Accounting Pack")).toBe("monthly-accounting-pack")
  })

  it("collapses punctuation rather than emitting it", () => {
    expect(slugify("Classify (copy) — v2!")).toBe("classify-copy-v2")
  })

  it("does not leave leading or trailing hyphens", () => {
    expect(slugify("  —hello—  ")).toBe("hello")
  })
})
