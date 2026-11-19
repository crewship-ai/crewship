import { describe, it, expect } from "vitest"

import {
  AUDIT_CATEGORIES,
  AUDIT_SOURCES,
  DEFAULT_AUDIT_FILTERS,
  activeFilterChips,
  auditQueryParams,
  filtersFromSearch,
  filtersToSearch,
  rangeBounds,
  sourceSupports,
  type AuditFilters,
} from "../audit-filters"

const NOW = new Date("2026-09-29T12:00:00Z")
const f = (over: Partial<AuditFilters> = {}): AuditFilters => ({ ...DEFAULT_AUDIT_FILTERS, ...over })

describe("rangeBounds", () => {
  it.each([
    ["1h", "2026-09-29T11:00:00.000Z"],
    ["24h", "2026-09-28T12:00:00.000Z"],
    ["7d", "2026-09-22T12:00:00.000Z"],
    ["30d", "2026-08-30T12:00:00.000Z"],
    ["90d", "2026-07-01T12:00:00.000Z"],
  ] as const)("%s starts at the right instant and is open-ended", (range, from) => {
    expect(rangeBounds(f({ range }), NOW)).toEqual({ from, to: undefined })
  })

  it("all time sends no bounds", () => {
    expect(rangeBounds(f({ range: "all" }), NOW)).toEqual({ from: undefined, to: undefined })
  })

  // The server compares created_at as text. A bare "2026-09-24" as date_to
  // would drop every event ON the 24th ("2026-09-24T…" sorts after it), so the
  // end of a custom range is the start of the following day.
  it("custom range includes the whole last day", () => {
    expect(rangeBounds(f({ range: "custom", from: "2026-09-20", to: "2026-09-24" }), NOW)).toEqual({
      from: "2026-09-20T00:00:00.000Z",
      to: "2026-09-25T00:00:00.000Z",
    })
  })

  it("custom range with one side open", () => {
    expect(rangeBounds(f({ range: "custom", from: "2026-09-20" }), NOW)).toEqual({ from: "2026-09-20T00:00:00.000Z", to: undefined })
  })
})

// The workspace trail filters by search, person and category on the server;
// the other three trails filter by time only. Sending a filter a trail ignores
// would show an unfiltered list under a filter that claims otherwise.
describe("auditQueryParams", () => {
  it("sends every filter for the workspace trail", () => {
    const p = auditQueryParams("ws", f({ q: "credential", userId: "u1", category: "CREW", range: "24h" }), 2, 50, NOW)
    expect(Object.fromEntries(p)).toEqual({
      workspace_id: "ws", page: "2", limit: "50", source: "workspace",
      search: "credential", user_id: "u1", entity_type: "CREW", date_from: "2026-09-28T12:00:00.000Z",
    })
  })

  it.each(["crews", "credentials", "keeper"] as const)("sends only time and paging for the %s trail", (source) => {
    const p = auditQueryParams("ws", f({ source, q: "x", userId: "u1", category: "CREW", range: "custom", from: "2026-09-01", to: "2026-09-02" }), 1, 50, NOW)
    expect(Object.fromEntries(p)).toEqual({
      workspace_id: "ws", page: "1", limit: "50", source,
      date_from: "2026-09-01T00:00:00.000Z", date_to: "2026-09-03T00:00:00.000Z",
    })
  })

  // "Failed runs" is the exact action the run writer records, so the server
  // narrows it (and the total) instead of the page guessing from one page.
  it("sends a run result as the exact action", () => {
    expect(auditQueryParams("ws", f({ result: "failed" }), 1, 50, NOW).get("action")).toBe("agent.run.failed")
    expect(auditQueryParams("ws", f({ source: "keeper", result: "failed" }), 1, 50, NOW).has("action")).toBe(false)
  })

  it("trims the search and drops it when blank", () => {
    expect(auditQueryParams("ws", f({ q: "   " }), 1, 50, NOW).has("search")).toBe(false)
    expect(auditQueryParams("ws", f({ q: " deploy " }), 1, 50, NOW).get("search")).toBe("deploy")
  })

  it("knows which trail supports which filter", () => {
    expect(sourceSupports("workspace")).toEqual({ search: true, person: true, category: true, result: true, entity: true })
    expect(sourceSupports("keeper")).toEqual({ search: false, person: false, category: false, result: false, entity: false })
  })

  // "Everything about <entity>": the list endpoint accepts entity_id.
  it("sends an entity for the workspace trail only", () => {
    expect(auditQueryParams("ws", f({ entityId: "crew-1" }), 1, 50, NOW).get("entity_id")).toBe("crew-1")
    expect(auditQueryParams("ws", f({ source: "crews", entityId: "crew-1" }), 1, 50, NOW).has("entity_id")).toBe(false)
    expect(auditQueryParams("ws", f(), 1, 50, NOW).has("entity_id")).toBe(false)
  })

  it("names the four records by what they hold, and the categories in the prototype's words", () => {
    expect(AUDIT_SOURCES.map((s) => [s.value, s.label, s.hint])).toEqual([
      ["workspace", "All changes", "Settings, people, crews, agents and runs"],
      ["crews", "Between crews", "Work handed over, messages and shared files"],
      ["credentials", "Secret use", "Which secret was used, revealed or replaced"],
      ["keeper", "Keeper decisions", "What the gatekeeper allowed, denied or escalated"],
    ])
    expect(AUDIT_CATEGORIES.map((c) => c.label)).toEqual(["Everything", "Runs", "Agents", "Crews", "Crew links", "Credentials", "People", "Workspace settings"])
  })
})

// Filters live in the URL so a reload, a shared link or the back button lands
// on the same slice of the log.
describe("filters in the URL", () => {
  it("round-trips every non-default filter and leaves unrelated params alone", () => {
    const filters = f({ source: "workspace", q: "role", userId: "u1", category: "WorkspaceMember", entityId: "m1", range: "custom", from: "2026-09-01", to: "2026-09-10" })
    const search = filtersToSearch(filters, "?tab=audit&x=1")
    expect(new URLSearchParams(search).get("audit_entity")).toBe("m1")
    expect(new URLSearchParams(search).get("tab")).toBe("audit")
    expect(new URLSearchParams(search).get("x")).toBe("1")
    expect(filtersFromSearch(search)).toEqual(filters)
  })

  it("writes nothing for the defaults", () => {
    expect(filtersToSearch(DEFAULT_AUDIT_FILTERS, "?tab=audit")).toBe("?tab=audit")
  })

  it("ignores junk values", () => {
    expect(filtersFromSearch("?audit_range=forever&audit_source=everything&audit_from=yesterday")).toEqual(DEFAULT_AUDIT_FILTERS)
  })
})

describe("activeFilterChips", () => {
  it("names each active filter in words, with the person's name", () => {
    const chips = activeFilterChips(f({ q: "role", userId: "u1", category: "CREW", range: "custom", from: "2026-09-01", to: "2026-09-10" }), { u1: "Demo User" })
    expect(chips.map((c) => [c.key, c.label])).toEqual([
      ["q", "“role”"],
      ["userId", "Who: Demo User"],
      ["category", "Crews"],
      ["range", "1 Sep 2026 – 10 Sep 2026"],
    ])
  })

  it("reads an entity filter back as everything about it", () => {
    expect(activeFilterChips(f({ entityId: "c1" }), {}, "Ops").map((c) => [c.key, c.label])).toEqual([["entityId", "Everything about Ops"]])
    expect(activeFilterChips(f({ entityId: "c1" }), {})[0].label).toBe("Everything about this item")
    expect(activeFilterChips(f({ source: "keeper", entityId: "c1" }), {})).toEqual([])
  })

  it("shows the default time range as nothing to clear", () => {
    expect(activeFilterChips(DEFAULT_AUDIT_FILTERS, {})).toEqual([])
  })
})
