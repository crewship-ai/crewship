import { act, render, screen, waitFor, cleanup } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch as apiFetchImport } from "@/lib/api-fetch"
import { OnboardingCreatedPanel } from "../onboarding-created-panel"

const apiFetch = vi.mocked(apiFetchImport)
beforeEach(() => { apiFetch.mockReset() })
afterEach(() => { cleanup(); vi.useRealTimers() })

function jsonOk(body: unknown) {
  return { ok: true, json: async () => body } as unknown as Response
}

/** Route each of the three list calls to its own fixture. */
function routeTo({ crews = [], routines = [], pages = [] }: {
  crews?: unknown[]
  routines?: unknown[]
  pages?: unknown[]
}) {
  apiFetch.mockImplementation(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.startsWith("/api/v1/crews")) return jsonOk(crews)
    if (url.includes("/pipelines")) return jsonOk(routines)
    if (url.startsWith("/api/v1/pages")) return jsonOk(pages)
    throw new Error("unexpected url " + url)
  })
}

describe("OnboardingCreatedPanel", () => {
  beforeEach(() => {
    apiFetch.mockReset()
  })
  afterEach(() => { cleanup(); vi.useRealTimers() })

  it("renders nothing when the workspace is still empty", async () => {
    routeTo({})
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(screen.queryByTestId("onboarding-created-panel")).toBeNull()
  })

  // The bug this component exists for: the Guide creates a routine and a page
  // by calling its own tools inside a container, which the browser never
  // hears about. The person was told in prose that both existed and shown an
  // empty panel — the agent's word was the only evidence.
  it("reports how many real crews exist, so a reloaded wizard can still launch", async () => {
    routeTo({
      crews: [
        { id: "c0", slug: "_crewship-setup", name: "Setup", agent_count: 1 },
        { id: "c1", slug: "web-watch", name: "Web Watch", agent_count: 2 },
      ],
    })
    const onCrewsFound = vi.fn()
    render(<OnboardingCreatedPanel workspaceId="ws_1" onCrewsFound={onCrewsFound} />)
    // The reserved setup crew is not a crew the person built.
    await waitFor(() => expect(onCrewsFound).toHaveBeenCalledWith(1))
  })

  it("lists routines and pages the agent created, not just crews", async () => {
    routeTo({
      crews: [{ id: "c1", slug: "web-watch", name: "Web Watch", agent_count: 2 }],
      routines: [{ slug: "seznam-uptime-check", name: "Seznam uptime check", status: "proposed" }],
      pages: [{ slug: "dostupnost", name: "Dostupnost seznam.cz", panel_count: 6 }],
    })
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)

    await waitFor(() => expect(screen.getByTestId("onboarding-created-panel")).toBeTruthy())
    expect(screen.getByTestId("onboarding-created-crew").textContent).toContain("Web Watch")
    expect(screen.getByTestId("onboarding-created-routine").textContent).toContain("Seznam uptime check")
    expect(screen.getByTestId("onboarding-created-page").textContent).toContain("Dostupnost seznam.cz")
  })

  // A routine an agent saves lands "proposed", not running. Saying it "runs
  // automatically" would repeat in the panel whatever optimism the transcript
  // carried — the panel exists to be the thing that does not do that.
  it("distinguishes a routine awaiting approval from one that runs", async () => {
    routeTo({ routines: [{ slug: "a", name: "Pending one", status: "proposed" }] })
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)
    await waitFor(() => expect(screen.getByTestId("onboarding-created-routine")).toBeTruthy())
    expect(screen.getByTestId("onboarding-created-routine").textContent).toContain("waiting for approval")

    cleanup()
    routeTo({ routines: [{ slug: "b", name: "Live one", status: "active" }] })
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)
    await waitFor(() => expect(screen.getByTestId("onboarding-created-routine")).toBeTruthy())
    expect(screen.getByTestId("onboarding-created-routine").textContent).toContain("runs automatically")
  })

  // _crewship-setup is the Guide's own machinery. Listing it would make every
  // brand-new workspace look like it already had a crew the person built.
  it("hides the Guide's own setup crew", async () => {
    routeTo({
      crews: [
        { id: "s", slug: "_crewship-setup", name: "Crewship Guide", agent_count: 1 },
        { id: "c1", slug: "web-watch", name: "Web Watch", agent_count: 1 },
      ],
    })
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)
    await waitFor(() => expect(screen.getByTestId("onboarding-created-panel")).toBeTruthy())
    const crews = screen.getAllByTestId("onboarding-created-crew").map((el) => el.textContent)
    expect(crews).toHaveLength(1)
    expect(crews[0]).toContain("Web Watch")
    expect(crews[0]).not.toContain("Crewship Guide")
  })

  it("survives one endpoint failing rather than showing nothing", async () => {
    apiFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes("/pipelines")) return { ok: false } as unknown as Response
      if (url.startsWith("/api/v1/pages")) return jsonOk([{ slug: "p", name: "A page", panel_count: 1 }])
      return jsonOk([])
    })
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)
    await waitFor(() => expect(screen.getByTestId("onboarding-created-page")).toBeTruthy())
    expect(screen.queryAllByTestId("onboarding-created-routine")).toHaveLength(0)
  })
})

it("reads wrapped lists, tolerates malformed rows and falls back to slugs and counts", async () => {
  apiFetch.mockImplementation(async (input) => {
    const url = String(input)
    if (url.startsWith("/api/v1/crews")) return jsonOk({ items: [null, 4, [], { id: "c", slug: "solo", agents: [{}] }, { slug: "empty" }] })
    if (url.includes("pipelines")) return jsonOk({ items: [{ slug: "timer", status: "active" }] })
    return jsonOk({ items: [{ slug: "one", panelCount: 1 }, { slug: "empty-page" }] })
  })
  render(<OnboardingCreatedPanel workspaceId="ws/space" />)
  expect(await screen.findByText("solo")).toBeVisible()
  expect(screen.getByText("1 agent")).toBeVisible(); expect(screen.getByText("1 panel")).toBeVisible()
  expect(screen.getByText("empty-page")).toBeVisible(); expect(screen.getByText("timer")).toBeVisible()
  expect(apiFetch).toHaveBeenCalledWith("/api/v1/workspaces/ws%2Fspace/pipelines?workspace_id=ws%2Fspace")
})
it.each(["network", "http", "json", "shape"])("retains known inventory and count after a %s poll failure", async (kind) => {
  vi.useFakeTimers()
  routeTo({ crews: [{ slug: "known", agentCount: 2 }], routines: [{ slug: "routine" }], pages: [{ slug: "page" }] })
  const report = vi.fn()
  await act(async () => { render(<OnboardingCreatedPanel workspaceId="ws" onCrewsFound={report} />) })
  expect(report).toHaveBeenLastCalledWith(1)
  apiFetch.mockImplementation(async () => {
    if (kind === "network") throw new Error("offline")
    if (kind === "http") return { ok: false } as Response
    if (kind === "json") return { ok: true, json: async () => { throw new Error("bad JSON") } } as unknown as Response
    return jsonOk({ items: "invalid" })
  })
  await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
  expect(screen.getByText("known")).toBeVisible(); expect(screen.getByText("routine")).toBeVisible(); expect(screen.getByText("page")).toBeVisible()
  expect(report).toHaveBeenLastCalledWith(1)
  routeTo({}); await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
  expect(screen.queryByTestId("onboarding-created-panel")).toBeNull(); expect(report).toHaveBeenLastCalledWith(0)
  cleanup(); vi.useRealTimers()
})
it("ignores old workspace responses and clears inventory when scope disappears", async () => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => { finish = resolve })
  apiFetch.mockReturnValue(pending)
  const report = vi.fn(); const view = render(<OnboardingCreatedPanel workspaceId="old" onCrewsFound={report} />)
  routeTo({ crews: [{ slug: "new-crew" }] }); view.rerender(<OnboardingCreatedPanel workspaceId="new" onCrewsFound={report} />)
  await screen.findByText("new-crew")
  await act(async () => { finish(jsonOk([{ slug: "stale" }])) })
  expect(screen.queryByText("stale")).toBeNull(); expect(screen.getByText("new-crew")).toBeVisible()
  view.rerender(<OnboardingCreatedPanel workspaceId={null} onCrewsFound={report} />)
  expect(screen.queryByTestId("onboarding-created-panel")).toBeNull(); expect(report).toHaveBeenLastCalledWith(0)
})
it("does not overlap slow polls or publish after unmount", async () => {
  vi.useFakeTimers(); let finish!: (response: Response) => void
  apiFetch.mockReturnValue(new Promise<Response>((resolve) => { finish = resolve }))
  const report = vi.fn(); const view = render(<OnboardingCreatedPanel workspaceId="ws" onCrewsFound={report} />)
  await act(async () => { await vi.advanceTimersByTimeAsync(12000) })
  expect(apiFetch).toHaveBeenCalledTimes(3)
  view.unmount(); report.mockClear()
  await act(async () => { finish(jsonOk([{ slug: "late" }])); await vi.advanceTimersByTimeAsync(8000) })
  expect(report).not.toHaveBeenCalled(); expect(apiFetch).toHaveBeenCalledTimes(3)
  vi.useRealTimers()
})
