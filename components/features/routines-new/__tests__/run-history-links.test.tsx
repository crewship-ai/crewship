import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { RunHistoryCard } from "../shared"
import { RUN_HISTORY } from "@/lib/routines-preview/fixtures"

// A run row that only carries an id is a dead end: the reader has to
// open Activity, find the routine, find the run, and re-establish the
// context they already had. The row is the shortest path to the trace
// it describes, so it links to the trace — filtered to this routine AND
// selecting this run, because arriving at an unfiltered rail of every
// run in the workspace is barely better than arriving nowhere.

describe("<RunHistoryCard> links", () => {
  it("filters Activity to this routine and selects the run", () => {
    render(<RunHistoryCard pipelineSlug="mesicni-ucetni-podklady" />)
    const first = RUN_HISTORY[0]
    const row = screen.getByRole("link", { name: new RegExp(first.summary, "i") })
    const href = row.getAttribute("href") ?? ""
    expect(href).toContain("/activity?")
    expect(href).toContain("pipeline=mesicni-ucetni-podklady")
    expect(href).toContain(`run=${first.id}`)
  })

  it("still links to the run when no routine slug is supplied", () => {
    render(<RunHistoryCard />)
    const first = RUN_HISTORY[0]
    const row = screen.getByRole("link", { name: new RegExp(first.summary, "i") })
    expect(row.getAttribute("href")).toContain(`run=${first.id}`)
  })

  it("points its footer at this routine's runs, not the whole workspace", () => {
    render(<RunHistoryCard pipelineSlug="mesicni-ucetni-podklady" />)
    const all = screen.getByRole("link", { name: /full trace|all runs|activity/i })
    expect(all.getAttribute("href")).toContain("pipeline=mesicni-ucetni-podklady")
  })
})
