import { useState } from "react"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutineCalendar } from "../routine-calendar"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "VIEWER" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: (key: string) => useState(key === "calendar" ? "year" : "2028-01-01") }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))
const at = (hour: number) => new Date(2028, 0, 2, hour).toISOString()
const EVENTS = [
  { id: "repeat", kind: "planned", at: at(9), slug: "a", inputs: { message: "Daily summary" } },
  { id: "once", kind: "pending", at: at(10), slug: "b", inputs: {} },
  { id: "long", kind: "pending", at: at(11), slug: "c", inputs: { message: "x".repeat(1000) } },
  { id: "secret", kind: "pending", at: at(12), slug: "d", pinned_version: 2, inputs: { message: "ghp_" + "x".repeat(36) } },
  { id: "actual", kind: "run", at: at(8), slug: "a", status: "completed" },
]
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async (url: string) => {
  const params = new URL(url, "https://example.test").searchParams
  const from = Date.parse(params.get("from") ?? "1970-01-01"), to = Date.parse(params.get("to") ?? "2999-01-01")
  return { ok: true, json: async () => ({ events: EVENTS.filter((e) => Date.parse(e.at) >= from && Date.parse(e.at) < to), truncated: false }) }
}) }))
const routines = ["a", "b", "c", "d"].map((slug) => ({ slug, name: `Recipe ${slug}` })) as Pipeline[]
it("shows bounded presets for both plan kinds in the day agenda without presenting history as a preset", async () => {
  render(<RoutineCalendar workspaceId="ws" routines={routines} />)
  // The month cell shows two entries and an overflow count; presets belong to the day.
  fireEvent.click(await screen.findByRole("button", { name: /^Open 2028-01-02, 5 entries$/ }, { timeout: 4000 }))
  const agenda = within(screen.getByRole("region", { name: "Day agenda" }))
  expect(agenda.getByText("Planned · schedule · latest published · Inputs: message: Daily summary")).toBeInTheDocument()
  expect(agenda.getByText("Planned · pinned v2 · Inputs: message: Hidden")).toBeInTheDocument()
  expect(agenda.getByText("Planned · one-time start · version live at dispatch · No inputs")).toBeInTheDocument()
  expect(agenda.getByText(/Inputs: message: x/).textContent!.length).toBeLessThan(160)
  expect(agenda.getByText("Ran · Completed")).toBeInTheDocument()
  expect(screen.queryByText("Inputs unavailable")).not.toBeInTheDocument()
  // A viewer cannot schedule from here.
  expect(agenda.queryByRole("button", { name: /Schedule a start/ })).toBeNull()
})
