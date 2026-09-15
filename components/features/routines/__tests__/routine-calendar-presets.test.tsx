import { useState } from "react"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutineCalendar } from "../routine-calendar"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "VIEWER" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: (key: string) => useState(key === "calendar" ? "month" : "2028-01-01") }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => ({ events: [
  { id: "repeat", kind: "planned", at: "2028-01-02T09:00:00Z", slug: "a", inputs: { message: "Daily summary" } },
  { id: "once", kind: "pending", at: "2028-01-02T10:00:00Z", slug: "b", inputs: {} },
  { id: "long", kind: "pending", at: "2028-01-02T11:00:00Z", slug: "c", inputs: { message: "x".repeat(1000) } },
  { id: "secret", kind: "pending", at: "2028-01-02T12:00:00Z", slug: "d", pinned_version: 2, inputs: { message: "ghp_" + "x".repeat(36) } },
  { id: "actual", kind: "run", at: "2028-01-02T08:00:00Z", slug: "a", status: "completed" },
], truncated: false }) })) }))
const routines = ["a", "b", "c", "d"].map((slug) => ({ slug, name: `Recipe ${slug}` })) as Pipeline[]
it("shows bounded presets for both plan kinds in the day agenda without presenting history as a preset", async () => {
  render(<RoutineCalendar workspaceId="ws" routines={routines} />)
  // The month cell folds five entries into routine rows; presets belong to the day.
  fireEvent.click(await screen.findByRole("button", { name: "Open 2028-01-02, 5 entries" }))
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
