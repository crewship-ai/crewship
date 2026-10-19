import { describe, it, expect } from "vitest"

import {
  arrivalFromParams,
  filtersHref,
  issueNeighbours,
  issueSequence,
  panelHref,
  searchHref,
} from "../issues-page-links"
import { NO_FILTERS } from "@/hooks/use-issue-filters"
import type { Mission } from "@/lib/types/mission"

// The Issues panel on /issues/<id> (v67 I-FULL draws the full page inside
// Issues, with its panel) navigates instead of filtering in place: each pick
// is a link /issues reads once on arrival. ↑/↓ step through the list the
// reader came from.

const issue = (identifier: string, over: Partial<Mission> = {}): Mission =>
  ({ id: identifier.toLowerCase(), identifier, title: identifier, status: "TODO", crew_id: "c1", created_at: "", updated_at: "", ...over }) as Mission

describe("panel links", () => {
  it.each([
    [{ kind: "quick", id: "all" }, "/issues"],
    [{ kind: "quick", id: "mine" }, "/issues?view=mine"],
    [{ kind: "saved", id: "v1" }, "/issues?saved=v1"],
    [{ kind: "project", id: "p 1" }, "/issues?project=p+1"],
  ] as const)("%j → %s", (sel, href) => {
    expect(panelHref(sel)).toBe(href)
  })

  it("a facet picked on the issue page opens /issues with that facet", () => {
    expect(filtersHref({ ...NO_FILTERS, statuses: ["TODO", "REVIEW"], label: "docs" })).toBe("/issues?status=TODO%2CREVIEW&label=docs")
    expect(filtersHref(NO_FILTERS)).toBe("/issues")
  })

  it("a search opens /issues with ?q=", () => {
    expect(searchHref("  login bug ")).toBe("/issues?q=login+bug")
    expect(searchHref("  ")).toBe("/issues")
  })
})

describe("arrivalFromParams (read once by /issues, then stripped)", () => {
  it("reads the view, saved view, search and New project, and keeps every other key", () => {
    const a = arrivalFromParams(new URLSearchParams("view=mine&q=login&saved=v1&create=project&status=TODO"))
    expect(a).toEqual({ quick: "mine", savedId: "v1", search: "login", createProject: true, rest: "status=TODO" })
  })

  it("is empty for a plain link, and ignores an unknown view", () => {
    expect(arrivalFromParams(new URLSearchParams("view=nope&project=p1"))).toEqual({
      quick: null, savedId: null, search: null, createProject: false, rest: "project=p1",
    })
  })
})

describe("issueSequence: the list the reader came from", () => {
  const list = [issue("OPS-3", { status: "DONE" }), issue("OPS-4"), issue("OPS-5", { status: "REVIEW" })]

  it("applies the facets carried in ?from=/issues?…", () => {
    expect(issueSequence(list, "/issues?status=TODO,REVIEW", { groupBy: "none", orderBy: "manual" }).map((i) => i.identifier)).toEqual(["OPS-4", "OPS-5"])
  })

  it("from anywhere else it is every loaded issue, in the list's grouping", () => {
    expect(issueSequence(list, "/inbox", { groupBy: "status", orderBy: "manual" }).map((i) => i.identifier)).toEqual(["OPS-5", "OPS-4", "OPS-3"])
  })
})

describe("issueNeighbours", () => {
  const seq = [issue("OPS-3"), issue("OPS-4"), issue("OPS-5")]
  it("gives the previous and next, wrapping like the peek", () => {
    expect(issueNeighbours(seq, "OPS-4")).toEqual({ prev: "OPS-3", next: "OPS-5" })
    expect(issueNeighbours(seq, "OPS-5")).toEqual({ prev: "OPS-4", next: "OPS-3" })
  })
  it("has none when the issue is not in the list or alone in it", () => {
    expect(issueNeighbours(seq, "OPS-9")).toEqual({ prev: null, next: null })
    expect(issueNeighbours([issue("OPS-4")], "OPS-4")).toEqual({ prev: null, next: null })
  })
})

describe("arrival from the issue page, all six views", () => {
  it.each(["mine", "reply", "working", "review", "blocked", "all"])("?view=%s", (v) => {
    expect(arrivalFromParams(new URLSearchParams(`view=${v}`)).quick).toBe(v)
  })
  it("ignores an unknown view", () => {
    expect(arrivalFromParams(new URLSearchParams("view=everything")).quick).toBeNull()
  })
})
