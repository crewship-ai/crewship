import { useState } from "react"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { RoutineCalendar } from "../routine-calendar"
import type { Pipeline } from "@/hooks/use-pipelines"

// The month view's density rules and the day agenda, rendered against one
// busy day (39 planned starts of one routine plus a few others) and one
// quiet day — the two cases the proposal's calendar screen is built around.

const h = vi.hoisted(() => ({ view: "month" as "month" | "week", events: [] as unknown[] }))
// The year view fetches one range per month; answer each with the events
// inside it, as the server would, so a day is counted once.
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => {
    const params = new URL(url, "https://example.test").searchParams
    const from = Date.parse(params.get("from") ?? "1970-01-01"), to = Date.parse(params.get("to") ?? "2999-01-01")
    const events = (h.events as { at: string }[]).filter((e) => Date.parse(e.at) >= from && Date.parse(e.at) < to)
    return { ok: true, json: async () => ({ events, truncated: false }) }
  }),
}))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({
  useUrlSelection: (key: string) => useState(key === "calendar" ? h.view : "2028-01-16"),
}))
vi.mock("@/components/ui/crew-icon", () => ({
  CrewIcon: ({ icon }: { icon: string }) => <span data-testid="routine-icon">{icon}</span>,
}))
vi.mock("next/link", () => ({
  default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a>,
}))

const routines = [
  { slug: "classify", name: "Classify support ticket" },
  { slug: "invoice", name: "Invoice intake" },
  { slug: "briefing", name: "Morning briefing" },
  { slug: "renewal", name: "Contract renewal check" },
] as Pipeline[]

// Local instants on 16 Jan 2028 so the clocks read the same in any zone.
const at = (day: number, h: number, m = 0) => new Date(2028, 0, day, h, m).toISOString()

function busyDay() {
  const events: unknown[] = []
  for (let i = 0; i < 38; i++)
    events.push({ id: `c${i}`, kind: "pending", at: at(16, 8 + Math.floor(i / 4), (i % 4) * 15), slug: "classify", pinned_version: 2, inputs: { batch: "re-classify" } })
  events.push({ id: "b1", kind: "planned", at: at(16, 7, 30), slug: "briefing", inputs: {} })
  events.push({ id: "i1", kind: "planned", at: at(16, 8), slug: "invoice", inputs: {} })
  events.push({ id: "r1", kind: "planned", at: at(16, 12), slug: "renewal", inputs: {} })
  events.push({ id: "w1", kind: "run", at: at(16, 10, 32), slug: "invoice", status: "waiting" })
  // A quiet day: three entries, drawn as rows.
  events.push({ id: "q1", kind: "run", at: at(15, 7, 30), slug: "briefing", status: "completed" })
  events.push({ id: "q2", kind: "run", at: at(15, 8), slug: "invoice", status: "failed" })
  events.push({ id: "q3", kind: "planned", at: at(15, 17), slug: "renewal", inputs: {} })
  return events
}

beforeEach(() => {
  h.view = "month"
  h.events = busyDay()
})

describe("routine calendar — month density", () => {
  it("shows the two earliest starts of a day in order, then how many follow, and never a scrollbar", async () => {
    render(<RoutineCalendar workspaceId="ws" routines={routines} />)
    const quiet = await screen.findByRole("button", { name: "Open 2028-01-15, 3 entries" })
    // Earliest first: the 07:30 run, then 08:00; the 17:00 start is "later".
    expect(within(quiet).getByText("07:30")).toBeInTheDocument()
    expect(within(quiet).getByText("Morning briefing")).toBeInTheDocument()
    expect(within(quiet).getByText("08:00")).toBeInTheDocument()
    expect(within(quiet).queryByText("Contract renewal check")).toBeNull()
    expect(within(quiet).getByTestId("calendar-cell-later")).toHaveTextContent("+1 later")

    const busy = screen.getByRole("button", { name: "Open 2028-01-16, 42 entries" })
    expect(within(busy).getByText("Morning briefing")).toBeInTheDocument()
    expect(within(busy).getAllByTestId("routine-icon")).toHaveLength(2)
    expect(within(busy).getByTestId("calendar-cell-later")).toHaveTextContent("+40 later")
    expect(within(busy).queryByText(/×\d+/)).toBeNull()
    // Nothing inside the cell scrolls: the cell clips at a fixed height and lights up on hover.
    expect(busy.className).toMatch(/md:h-28/)
    expect(busy.className).toMatch(/hover:/)
    expect(busy.querySelector(".overflow-y-auto")).toBeNull()
  })

  it("opens the Day view for the clicked day, and schedules from the + without opening it", async () => {
    render(<RoutineCalendar workspaceId="ws" routines={routines} />)
    fireEvent.click(await screen.findByRole("button", { name: "Open 2028-01-15, 3 entries" }))
    // The hour grid for that date, not the agenda.
    expect(screen.queryByRole("region", { name: "Day agenda" })).toBeNull()
    expect(screen.getByRole("button", { name: "Day" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(/15 Jan 2028/)
  })

  it("filters with counted chips taken from the whole month", async () => {
    render(<RoutineCalendar workspaceId="ws" routines={routines} />)
    const chips = within(await screen.findByRole("group", { name: "Calendar events" }))
    expect(chips.getByRole("button", { name: "All 45" })).toHaveAttribute("aria-pressed", "true")
    expect(chips.getByRole("button", { name: "Planned 42" })).toBeInTheDocument()
    expect(chips.getByRole("button", { name: "Ran 3" })).toBeInTheDocument()
    expect(chips.getByRole("button", { name: "Waiting 1" })).toBeInTheDocument()
    expect(chips.getByRole("button", { name: "Failed 1" })).toBeInTheDocument()
    fireEvent.click(chips.getByRole("button", { name: "Failed 1" }))
    expect(screen.getByRole("button", { name: "Open 2028-01-15, 1 entry" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Open 2028-01-16, 0 entries" })).toBeInTheDocument()
    // The counts do not collapse to the filtered view.
    expect(chips.getByRole("button", { name: "Planned 42" })).toBeInTheDocument()
    expect(screen.queryByRole("combobox", { name: "Calendar events" })).toBeNull()
  })

  it("opens a day as an agenda grouped by routine from the year view, with the waiting run first", async () => {
    h.view = "year"
    render(<RoutineCalendar workspaceId="ws" routines={routines} />)
    fireEvent.click(await screen.findByRole("button", { name: /^Open 2028-01-16, \d+ entries$/ }, { timeout: 4000 }))
    const agenda = within(screen.getByRole("region", { name: "Day agenda" }))
    expect(screen.getByRole("heading", { name: "Sunday, 16 January 2028" })).toBeInTheDocument()
    expect(agenda.getByText("41 planned · 1 ran · 1 waiting")).toBeInTheDocument()
    // Needs you, before the groups.
    const banner = agenda.getByRole("status")
    expect(banner).toHaveTextContent("1 run waiting for your decision")
    expect(within(banner).getByRole("link", { name: "Decide" })).toHaveAttribute("href", "/routines?slug=invoice&run=w1")
    // The 38-start routine is one row with its cadence, kind, pin and preset.
    expect(agenda.getByText("×38")).toBeInTheDocument()
    expect(agenda.getByText("planned · 08:00–17:15 · every 15 min · one-time starts · pinned v2 · with: Inputs: batch: re-classify")).toBeInTheDocument()
    expect(agenda.getByRole("link", { name: "Open routine" })).toHaveAttribute("href", "/routines?slug=classify&view=plan")
    // Routines with one or two entries stay as plain rows with a pill.
    const briefing = agenda.getByRole("link", { name: /Morning briefing/ })
    expect(briefing).toHaveTextContent("07:30")
    expect(within(briefing).getByText("Planned")).toBeInTheDocument()
    const invoiceRows = agenda.getAllByRole("link", { name: /Invoice intake/ })
    expect(invoiceRows).toHaveLength(2)
    expect(invoiceRows[1]).toHaveAttribute("href", "/routines?slug=invoice&run=w1")
    expect(within(invoiceRows[1]).getByText("Waiting")).toBeInTheDocument()
    // Show all expands the starts as time chips.
    expect(agenda.queryByText("17:15")).toBeNull()
    fireEvent.click(agenda.getByRole("button", { name: "Show all 38" }))
    expect(agenda.getByText("17:15")).toBeInTheDocument()
    expect(agenda.getAllByText(/^\d\d:\d\d$/).length).toBeGreaterThanOrEqual(38)
    fireEvent.click(agenda.getByRole("button", { name: "Hide" }))
    expect(agenda.queryByText("17:15")).toBeNull()
    // ‹ Month returns to the grid.
    fireEvent.click(agenda.getByRole("button", { name: "‹ Year" }))
    expect(screen.queryByRole("region", { name: "Day agenda" })).toBeNull()
    expect(screen.getByRole("button", { name: "Open 2028-01-16, 42 entries" })).toBeInTheDocument()
  })

  it("keeps the hour grid for the week but folds several starts of one routine in an hour into one chip", async () => {
    h.view = "week"
    render(<RoutineCalendar workspaceId="ws" routines={routines} />)
    const chips = await screen.findAllByRole("button", { name: /Classify support ticket.*×4/ })
    // 38 starts every 15 min = nine hours with four starts (one chip each) and a last hour with two.
    expect(chips).toHaveLength(9)
    expect(screen.getByRole("button", { name: /Classify support ticket.*×2/ })).toBeInTheDocument()
    const chip = chips[0]
    expect(chip).toHaveTextContent("×4")
    // The single starts keep their rows (the 15th's run and the 16th's planned start).
    expect(screen.getAllByRole("link", { name: /Morning briefing/ })).toHaveLength(2)
    // The chip opens the agenda with that routine expanded.
    fireEvent.click(chip)
    const agenda = within(screen.getByRole("region", { name: "Day agenda" }))
    expect(agenda.getByRole("button", { name: "Hide" })).toBeInTheDocument()
  })
})
