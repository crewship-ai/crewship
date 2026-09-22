import { useState } from "react"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { beforeEach, describe, it, expect, vi } from "vitest"
import { RoutineCalendar } from "../routine-calendar"
import type { Pipeline } from "@/hooks/use-pipelines"
const { fetcher, access } = vi.hoisted(() => ({ fetcher: vi.fn(), access: { role: "OWNER", capabilities: [] as string[] } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => access }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: (key: string) => useState(key === "calendar" ? "year" : "2028-01-01") }))
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: ({ icon }: { icon: string }) => <span data-testid="routine-icon">{icon}</span> }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))

describe("full routine calendar", () => {
  it("loads every month of the year with bounded intervals, marks density per day and plans from the selected day", async () => {
    fetcher.mockImplementation(async (url: string) => {
      const params = new URL(url, "https://example.test").searchParams
      const from = params.get("from")!
      return { ok: true, json: async () => ({ events: [{ id: from, kind: "planned", at: from, slug: "recipe", name: "Daily recipe" }], truncated: false }) }
    })
    render(<RoutineCalendar workspaceId="ws" routines={[{ slug: "recipe", name: "Daily recipe", icon: "alarm-clock", color: "blue" }] as Pipeline[]} />)
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(12))
    for (const [url] of fetcher.mock.calls) {
      const params = new URL(url, "https://example.test").searchParams
      expect(Date.parse(params.get("to")!) - Date.parse(params.get("from")!)).toBeLessThanOrEqual(32 * 86400000)
    }
    // The year view shows a mark per day, not the routines' icons.
    await waitFor(() => expect(screen.getAllByRole("button", { name: /^Open 2028-\d\d-01, 1 entry$/ })).toHaveLength(12))
    expect(screen.queryByTestId("routine-icon")).toBeNull()
    // Opening a day from the year view shows its agenda, not the hour grid.
    fireEvent.click(screen.getByRole("button", { name: "Open 2028-02-01, 1 entry" }))
    const agenda = screen.getByRole("region", { name: "Day agenda" })
    expect(within(agenda).getByText("1 planned")).toBeInTheDocument()
    expect(within(agenda).getByRole("link", { name: /Daily recipe/ })).toHaveAttribute("href", "/routines?slug=recipe&view=plan")
    fireEvent.click(within(agenda).getByRole("button", { name: /Schedule a start/ }))
    expect(await screen.findByRole("dialog")).toBeInTheDocument()
    expect(screen.getByLabelText("Date")).toHaveValue("2028-02-01")
    expect(screen.getByLabelText("Time")).toHaveValue("09:00")
    fireEvent.click(screen.getByRole("button", { name: "Close" }))
    fireEvent.click(within(agenda).getByRole("button", { name: "‹ Year" }))
    expect(screen.queryByRole("region", { name: "Day agenda" })).toBeNull()
  })
})


beforeEach(() => { access.role = "OWNER"; access.capabilities = [] })
it.each([
  ["MEMBER", [], false],
  ["VIEWER", ["routine.run"], true],
  ["MEMBER", ["routine.create"], false],
] as const)("calendar one-time start: %s %j", async (role, caps, allowed) => {
  access.role = role; access.capabilities = [...caps]
  fetcher.mockResolvedValue({ok:true,json:async()=>({events:[],truncated:false})})
  render(<RoutineCalendar workspaceId="ws" routines={[]} />)
  await waitFor(() => expect(screen.getByRole("button", {name:"Open 2028-02-01, 0 entries"})).toBeEnabled())
  fireEvent.click(screen.getByRole("button", {name:"Open 2028-02-01, 0 entries"}))
  const start=within(screen.getByRole("region", {name:"Day agenda"})).queryByRole("button", {name:/Schedule a start/})
  expect(!!start).toBe(allowed)
})
