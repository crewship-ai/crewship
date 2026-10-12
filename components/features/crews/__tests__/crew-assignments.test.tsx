import { useEffect } from "react"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { CrewAssignments } from "../crew-assignments"
import { apiFetch } from "@/lib/api-fetch"
import type { Assignment } from "@/lib/types/assignment"
const { events } = vi.hoisted(() => ({ events: new Map<string, () => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (name: string, callback: () => void) => {
  useEffect(() => { events.set(name, callback); return () => { events.delete(name) } }, [name, callback])
} }))
const fetchMock = vi.mocked(apiFetch)
const assignment: Assignment = { id: "a", task: "Investigate", status: "PENDING", assigned_by_name: "Lead", assigned_by_slug: "lead", assigned_to_name: "Worker", assigned_to_slug: "worker", result_summary: null, error_message: null, started_at: null, finished_at: null, created_at: "2026-10-03T00:00:00Z" }
beforeEach(() => { fetchMock.mockReset(); events.clear() })
afterEach(() => { cleanup(); vi.useRealTimers() })
it("loads an empty list with an explanation and scoped request", async () => {
  fetchMock.mockResolvedValue(new Response("[]"))
  render(<CrewAssignments crewId="crew" workspaceId="workspace" />)
  expect(screen.getByText("Loading assignments...")).toBeInTheDocument()
  expect(await screen.findByText("No assignments yet.")).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/crews/crew/assignments?workspace_id=workspace&limit=50")
})
it("expands result/error details with accessible controls and ignores rows with no detail", async () => {
  fetchMock.mockResolvedValue(new Response(JSON.stringify([
    assignment,
    { ...assignment, id: "done", task: "Finished job", status: "COMPLETED", result_summary: "Result kept", started_at: "2026-10-03T00:00:00Z", finished_at: "2026-10-03T00:00:02Z" },
    { ...assignment, id: "failed", task: "Failed job", status: "FAILED", error_message: "Permission refused" },
    { ...assignment, id: "both", task: "Partial job", status: "FAILED", error_message: "Last step failed", result_summary: "Partial result" },
  ])))
  render(<CrewAssignments crewId="c" workspaceId="w" />)
  const task = await screen.findByRole("button", { name: "Finished job" })
  expect(task).toHaveAttribute("aria-expanded", "false")
  fireEvent.click(task)
  expect(task).toHaveAttribute("aria-expanded", "true")
  expect(document.getElementById(task.getAttribute("aria-controls")!)).toHaveTextContent("Result kept")
  fireEvent.click(task)
  expect(screen.queryByText("Result kept")).not.toBeInTheDocument()
  const failed = screen.getByRole("button", { name: "Failed job" })
  fireEvent.click(failed.closest("tr")!)
  expect(screen.getByText("Permission refused")).toBeInTheDocument()
  fireEvent.click(failed.closest("tr")!)
  expect(screen.queryByText("Permission refused")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Partial job" }))
  expect(screen.getByText("Last step failed")).toBeInTheDocument()
  expect(screen.getByText("Partial result")).toBeInTheDocument()
  const row = screen.getByText("Investigate").closest("tr")!
  expect(within(row).queryByRole("button")).not.toBeInTheDocument()
  fireEvent.click(row)
  expect(screen.getByRole("button", { name: "Partial job" })).toHaveAttribute("aria-expanded", "true")
  expect(within(row).getByText("@lead")).toBeInTheDocument()
  expect(within(row).getByText("@worker")).toBeInTheDocument()
})
it("refreshes on assignment events, retaining last good rows on transient refusal", async () => {
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([assignment])))
  const { unmount } = render(<CrewAssignments crewId="c" workspaceId="w" />)
  await screen.findByText("Investigate")
  fetchMock.mockResolvedValueOnce(new Response("unavailable", { status: 503 }))
  await act(async () => { events.get("assignment.updated")!() })
  expect(screen.getByText("Investigate")).toBeInTheDocument()
  expect(screen.queryByText("Loading assignments...")).not.toBeInTheDocument()
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([{ ...assignment, task: "Updated task" }])))
  await act(async () => { events.get("assignment.updated")!() })
  expect(screen.getByText("Updated task")).toBeInTheDocument()
  unmount()
  expect(events.has("assignment.updated")).toBe(false)
})
it("updates running duration every second and stops its timer on unmount", async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date("2026-10-03T00:00:01Z"))
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([
    { ...assignment, task: "Active job", status: "RUNNING", started_at: "2026-10-03T00:00:00Z" },
    { ...assignment, id: "unknown", task: "No start timestamp", status: "RUNNING" },
  ])))
  const view = render(<CrewAssignments crewId="c" workspaceId="w" />)
  await act(async () => { await Promise.resolve() })
  const row = screen.getByText("Active job").closest("tr")!
  expect(within(row).getByText("1s")).toBeInTheDocument()
  await act(async () => { vi.advanceTimersByTime(2000) })
  expect(within(row).getByText("3s")).toBeInTheDocument()
  view.unmount()
  expect(vi.getTimerCount()).toBe(0)
})
