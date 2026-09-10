import { useState } from "react"
import { render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutineCalendar } from "../routine-calendar"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "VIEWER" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: (key: string) => useState(key === "calendar" ? "month" : "2028-01-01") }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => ({ events: [
  { id: "repeat", kind: "planned", at: "2028-01-02T09:00:00Z", slug: "recipe", inputs: { message: "Daily summary" } },
  { id: "once", kind: "pending", at: "2028-01-02T10:00:00Z", slug: "recipe", inputs: {} },
  { id: "long", kind: "pending", at: "2028-01-02T11:00:00Z", slug: "recipe", inputs: { message: "x".repeat(1000) } },
  { id: "actual", kind: "run", at: "2028-01-02T08:00:00Z", slug: "recipe", status: "completed" },
], truncated: false }) })) }))
it("shows bounded presets for both plan kinds without presenting history as a preset", async () => {
  render(<RoutineCalendar workspaceId="ws" routines={[{ slug: "recipe", name: "Recipe" }] as Pipeline[]} />)
  expect(await screen.findByText("Inputs: message: Daily summary")).toBeInTheDocument()
  expect(screen.getByText("No inputs")).toBeInTheDocument()
  expect(screen.getByText(/^Inputs: message: x/).textContent!.length).toBeLessThan(100)
  expect(screen.getByText(/Planned ·/)).toBeInTheDocument()
  expect(screen.getAllByText(/Scheduled once ·/)).toHaveLength(2)
  expect(screen.queryByText("Inputs unavailable")).not.toBeInTheDocument()
})
