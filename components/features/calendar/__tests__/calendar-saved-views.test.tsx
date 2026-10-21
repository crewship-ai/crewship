import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { apiFetch } from "@/lib/api-fetch"
import { EMPTY_CALENDAR_FILTERS, type CalendarFilters } from "@/lib/calendar/model"
import { CalendarSavedViews, decodeCalendarFilters, encodeCalendarFilters } from "../calendar-saved-views"

const ok = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body }) as unknown as Response
const row = (id: string, name: string, filters: string, extra: Record<string, unknown> = {}) => ({
  id, name, filters_json: filters, sort_json: null, view_type: "list", is_default: false, shared: false, created_at: "2026-10-01T00:00:00Z", ...extra,
})
const research: CalendarFilters = { ...EMPTY_CALENDAR_FILTERS, scope: "workspace", crewId: "research", statuses: ["risk"] }

beforeEach(() => vi.mocked(apiFetch).mockReset())

describe("calendar filters in a saved view", () => {
  it("round-trips, and carries only the filter, never access", () => {
    const json = encodeCalendarFilters(research)
    expect(JSON.parse(json)).toEqual({
      surface: "calendar",
      params: { scope: "workspace", crew: "research", status: "risk" },
    })
    expect(decodeCalendarFilters(json)).toEqual({ ...EMPTY_CALENDAR_FILTERS, scope: "workspace", crewId: "research", statuses: ["risk"] })
  })

  it("ignores values it does not know instead of breaking", () => {
    expect(decodeCalendarFilters(JSON.stringify({ surface: "calendar", params: { scope: "galaxy", status: "risk,nope", agent_facet: "none,x" } }))).toEqual({
      ...EMPTY_CALENDAR_FILTERS,
      statuses: ["risk"],
      agentFacets: ["none"],
    })
    expect(decodeCalendarFilters("not json")).toBeNull()
  })
})

describe("CalendarSavedViews", () => {
  it("lists only calendar views, applies one and saves the current filters", async () => {
    const calls: { url: string; body?: string }[] = []
    vi.mocked(apiFetch).mockImplementation(async (url, init) => {
      calls.push({ url: String(url), body: (init as RequestInit | undefined)?.body as string | undefined })
      if ((init as RequestInit | undefined)?.method === "POST") return ok({ id: "new" }, 201)
      return ok([
        row("c1", "Research crew", encodeCalendarFilters(research)),
        row("j1", "Failed digests", JSON.stringify({ surface: "journal", params: {} })),
        row("i1", "Board", '{"project_id":"p"}'),
      ])
    })
    const onApply = vi.fn()
    render(<CalendarSavedViews workspaceId="ws" filters={{ ...EMPTY_CALENDAR_FILTERS, scope: "workspace", agentId: "ag" }} onApply={onApply} />)
    const list = await screen.findByRole("button", { name: /^Research crew/ })
    expect(screen.queryByText("Failed digests")).toBeNull()
    expect(screen.queryByText("Board")).toBeNull()
    fireEvent.click(list)
    expect(onApply).toHaveBeenCalledWith(expect.objectContaining({ scope: "workspace", crewId: "research", statuses: ["risk"] }))

    fireEvent.click(screen.getByRole("button", { name: "Save this view" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Scout week" } })
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Share with the workspace" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Save view" }))
    await waitFor(() => expect(calls.some((c) => c.body)).toBe(true))
    const post = JSON.parse(calls.find((c) => c.body)!.body!)
    expect(post).toMatchObject({ name: "Scout week", view_type: "list", shared: true })
    expect(JSON.parse(post.filters_json)).toEqual({ surface: "calendar", params: { scope: "workspace", agent: "ag" } })
  })
})
