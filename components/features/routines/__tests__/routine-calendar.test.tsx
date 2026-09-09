import { useState } from "react"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { describe, it, expect, vi } from "vitest"
import { RoutineCalendar } from "../routine-calendar"
import type { Pipeline } from "@/hooks/use-pipelines"
const { fetcher } = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: (key: string) => useState(key === "calendar" ? "year" : "2028-01-01") }))
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: ({ icon }: { icon: string }) => <span data-testid="routine-icon">{icon}</span> }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))

describe("full routine calendar", () => {
  it("loads every month of the year with bounded intervals, displays identities and plans from leap day", async () => {
    fetcher.mockImplementation(async (url: string) => {
      const params = new URL(url, "https://example.test").searchParams
      const from = params.get("from")!
      return { ok: true, json: async () => ({ events: [{ id: from, kind: "planned", at: from, slug: "recipe", name: "Daily recipe" }], truncated: false }) }
    })
    render(<RoutineCalendar workspaceId="ws" routines={[{ slug: "recipe", name: "Daily recipe", icon: "alarm-clock", color: "blue" }] as Pipeline[]} />)
    await waitFor(() => expect(screen.getAllByTestId("routine-icon")).toHaveLength(12))
    expect(fetcher).toHaveBeenCalledTimes(12)
    for (const [url] of fetcher.mock.calls) {
      const params = new URL(url, "https://example.test").searchParams
      expect(Date.parse(params.get("to")!) - Date.parse(params.get("from")!)).toBeLessThanOrEqual(32 * 86400000)
    }
    fireEvent.click(screen.getByRole("button", { name: "Schedule on 2028-02-29" }))
    expect(await screen.findByRole("dialog")).toBeInTheDocument()
    expect(screen.getByLabelText("Date")).toHaveValue("2028-02-29")
    expect(screen.getByLabelText("Time")).toHaveValue("09:00")
  })
})
