import { describe, it, expect } from "vitest"
import { applySavedView, parseSavedViews, isJournalView, issueViews, journalViews } from "@/lib/saved-views"
import type { SavedView } from "@/lib/types/mission"

// =============================================================================
// /journal and the issue board share one saved-views table and one list
// endpoint, so each has to recognise its own rows. The obvious discriminator
// is `view_type` — and it does not work: saved_views.view_type carries
// CHECK (view_type IN ('board','list')), so writing "journal" is a 500 from
// the insert, not a new category. The marker lives inside filters_json, which
// is a free-form TEXT column.
//
// This suite is here so that a future "let's just use view_type" refactor
// fails in CI instead of on the first save a user attempts.
// =============================================================================

function view(overrides: Partial<SavedView> = {}): SavedView {
  return {
    id: "v1",
    name: "A view",
    filters_json: "{}",
    sort_json: null,
    view_type: "list",
    is_default: false,
    shared: false,
    created_at: "2026-08-01T00:00:00Z",
    ...overrides,
  }
}

describe("saved-view surface discriminator", () => {
  it("recognises a journal view by its filters payload, not its view_type", () => {
    const v = view({
      view_type: "list",
      filters_json: JSON.stringify({ surface: "journal", params: { tab: "runs" } }),
    })
    expect(isJournalView(v)).toBe(true)
  })

  it("does not claim an issue view", () => {
    expect(isJournalView(view({ filters_json: '{"project_id":"p1"}' }))).toBe(false)
    expect(isJournalView(view({ view_type: "board", filters_json: "{}" }))).toBe(false)
  })

  it("does not claim a view it cannot parse", () => {
    expect(isJournalView(view({ filters_json: "not json" }))).toBe(false)
    expect(isJournalView(view({ filters_json: "" }))).toBe(false)
    expect(isJournalView(view({ filters_json: "null" }))).toBe(false)
    expect(isJournalView(view({ filters_json: '"journal"' }))).toBe(false)
  })

  it("splits a mixed workspace so neither dropdown shows the other's rows", () => {
    const journal = view({
      id: "j",
      filters_json: JSON.stringify({ surface: "journal", params: {} }),
    })
    const board = view({ id: "b", view_type: "board", filters_json: '{"crew_id":"c1"}' })
    const all = [journal, board]
    expect(journalViews(all).map((v) => v.id)).toEqual(["j"])
    expect(issueViews(all).map((v) => v.id)).toEqual(["b"])
  })
})

describe("saved-view API compatibility", () => {
  it.each([null, undefined, 0, "text", {}, { views: null }, { views: {} }])("ignores malformed list %j", raw => {
    expect(parseSavedViews(raw)).toEqual([])
  })
  it.each([false, true])("accepts only named identified rows (wrapped=%s)", wrapped => {
    const valid = view()
    const rows = [null, false, "x", {}, { id: 1, name: "x" }, { id: "x", name: 2 }, valid]
    expect(parseSavedViews(wrapped ? { views: rows } : rows)).toEqual([valid])
  })
  it.each([undefined, "", "{broken", "null", "{}", '{"project_id":1,"crew_id":false,"assignee_id":[],"search":{}}'])("uses neutral filters for %s", filters_json => {
    expect(applySavedView(view({ filters_json }))).toEqual({ projectId: null, crewId: null, agentId: null, search: "" })
  })
  it.each([
    { project_id: "p", crew_id: "c", assignee_id: "a", search: "find" },
    { projectId: "p", crewId: "c", assigneeId: "a", query: "find" },
    { project_id: null, projectId: "p", crew_id: null, crewId: "c", assignee_id: null, assigneeId: null, agent_id: "a", search: null, query: "find" },
  ])("maps supported filter vocabulary %j", filters => {
    expect(applySavedView(view({ filters_json: JSON.stringify(filters) }))).toEqual({ projectId: "p", crewId: "c", agentId: "a", search: "find" })
  })
  it("prefers explicit snake-case values, including empty strings", () => {
    expect(applySavedView(view({ filters_json: JSON.stringify({ project_id: "", projectId: "ignored", crew_id: "c", crewId: "ignored", assignee_id: "a", assigneeId: "ignored", agent_id: "ignored", search: "", query: "ignored" }) }))).toEqual({ projectId: "", crewId: "c", agentId: "a", search: "" })
  })
})
