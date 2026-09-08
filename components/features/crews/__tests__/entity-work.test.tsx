import { render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { EntityWork } from "../entity-work"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (input) => {
    const url = String(input)
    if (url.includes("/pipelines?")) return new Response(JSON.stringify([{ id: "r1", slug: "daily-check", name: "Daily check", status: "active" }]))
    return new Response(JSON.stringify([]), { headers: { "X-Total-Count": "0", "X-Status-Counts": "{}" } })
  })
})

describe("EntityWork destinations and scope", () => {
  it("shows crew routines while keeping the agent's work scoped only to that agent", async () => {
    render(<EntityWork workspaceId="work-agent-test" agentId="agent-1" crewId="crew-1" slug="ada" name="Ada" />)
    expect(await screen.findByRole("link", { name: "Daily check" })).toHaveAttribute("href", "/routines?routine=daily-check")
    expect(screen.getByRole("link", { name: "View all missions" })).toHaveAttribute("href", "/issues?assignee_id=agent-1&mission_type=mission")
    expect(screen.getByRole("link", { name: "View all issues" })).toHaveAttribute("href", "/issues?assignee_id=agent-1&mission_type=issue")
    const requests = vi.mocked(apiFetch).mock.calls.map(([url]) => new URL(String(url), "http://test"))
    const work = requests.filter(url => url.pathname === "/api/v1/issues")
    expect(work).toHaveLength(2)
    for (const url of work) {
      expect(url.searchParams.get("assignee_id")).toBe("agent-1")
      expect(url.searchParams.has("crew_id")).toBe(false)
    }
    expect(requests.find(url => url.pathname.endsWith("/pipelines"))?.searchParams.get("author_crew_id")).toBe("crew-1")
  })

  it("keeps crew work links and requests scoped to that crew", async () => {
    render(<EntityWork workspaceId="work-crew-test" crewId="crew-2" slug="research" name="Research" />)
    await waitFor(() => expect(screen.getByText("No missions assigned here yet.")).toBeInTheDocument())
    expect(screen.getByRole("link", { name: "View all missions" })).toHaveAttribute("href", "/issues?crew_id=crew-2&mission_type=mission")
    const work = vi.mocked(apiFetch).mock.calls.map(([url]) => new URL(String(url), "http://test")).filter(url => url.pathname === "/api/v1/issues")
    expect(work.every(url => url.searchParams.get("crew_id") === "crew-2" && !url.searchParams.has("assignee_id"))).toBe(true)
  })
})
