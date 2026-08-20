import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// The Docker tab asked GET /api/v1/system/runtime for `data.containers` — a
// field that endpoint has never sent (#1697). `Array.isArray(undefined)` is
// false, so the missing field became an empty list and the tab rendered
// "No containers running." on every crew, forever, with every container up.
//
// Two things are pinned here, because fixing only one leaves the bug's shape
// intact:
//
//   1. WHICH endpoint. The per-crew container facts the tab draws (container,
//      image, status, CPU, RAM, agents) come from
//      GET /api/v1/crews/{crewId}/containers. /api/v1/system/runtime is the
//      host runtime INVENTORY (#1690) and must never be asked for this again.
//   2. That a shape mismatch is LOUD. A response without a `containers` array
//      must surface an error, not an empty list — otherwise the next rename
//      fails exactly the same silent way.
// =============================================================================

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { DockerTab } from "../docker-tab"
import { parseContainerList } from "../docker-tab"

const crewContext = { kind: "crew" as const, crewId: "crew-1", crewSlug: "devops" }

function jsonOk(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response)
}

/** Every URL the component has requested, in order. */
function urls(): string[] {
  return apiFetch.mock.calls.map((c) => String(c[0]))
}

describe("DockerTab — asks the endpoint that actually serves crew containers", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockImplementation(() => jsonOk({ containers: [] }))
  })

  it("fetches the selected crew's containers, not the host runtime inventory", async () => {
    render(<DockerTab workspaceId="ws-1" context={crewContext} />)

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const asked = urls()
    expect(asked.some((u) => u.includes("/api/v1/crews/crew-1/containers"))).toBe(true)
    expect(asked.some((u) => u.includes("/api/v1/system/runtime"))).toBe(false)
    expect(asked[0]).toContain("workspace_id=ws-1")
  })

  it("takes the crew from an agent selection too (the agent's owning crew)", async () => {
    render(
      <DockerTab
        workspaceId="ws-1"
        context={{
          kind: "agent",
          agentId: "agent-1",
          agentSlug: "filip",
          agentName: "Filip",
          crewId: "crew-9",
          crewSlug: "devops",
        }}
      />,
    )
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(urls()[0]).toContain("/api/v1/crews/crew-9/containers")
  })

  it("renders a row per container instead of the unconditional empty state", async () => {
    apiFetch.mockImplementation(() =>
      jsonOk({
        containers: [
          {
            name: "crewship-team-devops-crew1",
            image: "crewship/agent:latest",
            status: "running",
            cpu_percent: 3.5,
            memory_mb: 412,
            agent_count: 4,
          },
        ],
      }),
    )

    render(<DockerTab workspaceId="ws-1" context={crewContext} />)

    expect(await screen.findByText("crewship-team-devops-crew1")).toBeTruthy()
    expect(screen.getByText("crewship/agent:latest")).toBeTruthy()
    expect(screen.getByText("3.5%")).toBeTruthy()
    expect(screen.getByText("412 MB")).toBeTruthy()
    expect(screen.queryByText("No containers running.")).toBeNull()
  })

  it("says no containers only when the API actually says so", async () => {
    apiFetch.mockImplementation(() => jsonOk({ containers: [] }))
    render(<DockerTab workspaceId="ws-1" context={crewContext} />)
    expect(await screen.findByText("No containers running.")).toBeTruthy()
  })

  it("surfaces a shape mismatch as an error, never as an empty list", async () => {
    // Exactly the payload the tab used to receive: a valid 200 with no
    // `containers` field at all. It must NOT read as "nothing is running".
    apiFetch.mockImplementation(() =>
      jsonOk({ available: true, runtime: "docker", version: "27.0.3" }),
    )

    render(<DockerTab workspaceId="ws-1" context={crewContext} />)

    await waitFor(() => expect(screen.queryByText(/Failed to load/)).not.toBeNull())
    expect(screen.queryByText("No containers running.")).toBeNull()
  })

  it("does not call the API at all when no crew is in context", async () => {
    render(<DockerTab workspaceId="ws-1" context={null} />)
    await waitFor(() => expect(screen.queryByText(/Select a crew/)).not.toBeNull())
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

describe("parseContainerList — the guard that used to swallow the mismatch", () => {
  const cases: { name: string; input: unknown; want: "throws" | number }[] = [
    { name: "a well-formed envelope", input: { containers: [{ name: "a" }, { name: "b" }] }, want: 2 },
    { name: "an empty list", input: { containers: [] }, want: 0 },
    { name: "the /system/runtime payload (no containers field)", input: { available: true }, want: "throws" },
    { name: "containers as an object", input: { containers: { a: 1 } }, want: "throws" },
    { name: "containers as null", input: { containers: null }, want: "throws" },
    { name: "a bare array (envelope forgotten)", input: [{ name: "a" }], want: "throws" },
    { name: "null", input: null, want: "throws" },
    { name: "a string", input: "containers", want: "throws" },
  ]

  for (const c of cases) {
    it(`${c.name} -> ${c.want === "throws" ? "throws" : `${c.want} rows`}`, () => {
      if (c.want === "throws") {
        expect(() => parseContainerList(c.input)).toThrow()
      } else {
        expect(parseContainerList(c.input)).toHaveLength(c.want)
      }
    })
  }
})
