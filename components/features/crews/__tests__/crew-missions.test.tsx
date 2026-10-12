import { useEffect } from "react"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { CrewMissions } from "../crew-missions"
import { apiFetch } from "@/lib/api-fetch"

const { events } = vi.hoisted(() => ({ events: new Map<string, () => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (name: string, callback: () => void) => {
  useEffect(() => { events.set(name, callback); return () => { events.delete(name) } }, [name, callback])
} }))
vi.mock("@/components/features/missions/create-mission-dialog", () => ({ CreateMissionDialog: ({ onCreated, crewId, workspaceId }: { onCreated: () => void; crewId: string; workspaceId: string }) => <button onClick={onCreated}>Create for {crewId} in {workspaceId}</button> }))
const fetchMock = vi.mocked(apiFetch)
const props = { crewId: "crew", workspaceId: "workspace", canCreate: false, leadAgents: [] }
const mission = { id: "mission", title: "Publish report", status: "IN_PROGRESS", lead_agent_slug: "researcher", task_stats: { total: 3, completed: 1 } }
beforeEach(() => { fetchMock.mockReset(); events.clear() })
afterEach(cleanup)

it("loads scoped missions, shows an empty state and respects creation permission", async () => {
  fetchMock.mockResolvedValue(new Response("[]"))
  const { rerender } = render(<CrewMissions {...props} />)
  expect(screen.getByText("Loading missions...")).toBeInTheDocument()
  expect(await screen.findByText("No missions yet.")).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/crews/crew/missions?workspace_id=workspace&limit=5")
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
  rerender(<CrewMissions {...props} canCreate />)
  expect(screen.getByRole("button", { name: "Create for crew in workspace" })).toBeInTheDocument()
})

it("links to each mission and only reports progress when tasks exist", async () => {
  fetchMock.mockResolvedValue(new Response(JSON.stringify([
    mission,
    { ...mission, id: "no-stats", title: "Draft proposal", task_stats: null },
    { ...mission, id: "zero", title: "Plan research", task_stats: { total: 0, completed: 0 } },
  ])))
  render(<CrewMissions {...props} />)
  expect(await screen.findByRole("link", { name: /Publish report/ })).toHaveAttribute("href", "/crews/crew/missions/mission")
  expect(screen.getByText("1/3 tasks")).toBeInTheDocument()
  expect(screen.getAllByRole("progressbar")).toHaveLength(1)
  expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "33")
  expect(screen.queryByText("0/0 tasks")).not.toBeInTheDocument()
  expect(screen.getAllByText("Lead: @researcher")).toHaveLength(3)
})

it("shows post-create refresh progress, retains missions on failure and accepts realtime updates", async () => {
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([mission])))
  const { unmount } = render(<CrewMissions {...props} canCreate />)
  await screen.findByText(mission.title)
  let finish!: (r: Response) => void
  fetchMock.mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve }))
  fireEvent.click(screen.getByRole("button", { name: "Create for crew in workspace" }))
  expect(screen.getByRole("status")).toHaveTextContent("Updating...")
  expect(screen.getByText(mission.title)).toBeInTheDocument()
  await act(async () => { finish(new Response("unavailable", { status: 503 })) })
  expect(screen.getByRole("status")).toHaveTextContent("Live")
  expect(screen.getByText(mission.title)).toBeInTheDocument()
  for (const name of ["mission.updated", "task.updated"]) {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([{ ...mission, title: name }])))
    await act(async () => { events.get(name)!() })
    expect(screen.getByText(name)).toBeInTheDocument()
    expect(screen.queryByText("Loading missions...")).not.toBeInTheDocument()
  }
  unmount()
  expect(events.size).toBe(0)
})
