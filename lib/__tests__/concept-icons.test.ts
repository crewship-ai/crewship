import { describe, it, expect } from "vitest"

import { CONCEPT_ICON } from "../concept-icons"

// =============================================================================
// The nav rail is the definition, not a second opinion.
//
// Issues, Routines, Skills, Credentials and Integrations all had an icon in
// the sidebar already. Every other surface then chose again from memory, so
// the agent overview showed Routines as a Workflow, Skills as Sparkles and
// Tools as a Wrench. Same concept, different face per screen — at which point
// the icon has stopped carrying meaning and is just decoration.
//
// This asserts the two agree. If the rail changes an icon, this fails until
// the map follows, which is the direction the dependency should run.
// =============================================================================

describe("concept icons", () => {
  it("matches the nav rail for every concept the rail names", async () => {
    const sidebar = await import("@/components/layout/app-sidebar")
    const nav = sidebar.NAV_ICONS as Record<string, unknown>

    for (const key of ["dashboard", "inbox", "issues", "routines", "activity",
      "journal", "crews", "skills", "credentials", "integrations",
      "marketplace", "settings", "admin"] as const) {
      expect(nav[key], `rail is missing "${key}"`).toBe(CONCEPT_ICON[key])
    }
  })

  it("gives connector-reached tools the same icon as Integrations", () => {
    // They are one concept wearing two labels; a different icon would imply a
    // second, separate thing to go and configure.
    expect(CONCEPT_ICON.tools).toBe(CONCEPT_ICON.integrations)
  })

  it("points a card at the icon of the screen it opens", () => {
    // Runs' footer link goes to the journal, so it wears the journal's icon —
    // the icon is a promise about where the link lands.
    expect(CONCEPT_ICON.runs).toBe(CONCEPT_ICON.journal)
  })

  it("never leaves a concept without an icon", () => {
    for (const [key, icon] of Object.entries(CONCEPT_ICON)) {
      expect(icon, `"${key}" has no icon`).toBeTruthy()
    }
  })
})
