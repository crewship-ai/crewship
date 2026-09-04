import { render, screen, waitFor, cleanup } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch as apiFetchImport } from "@/lib/api-fetch"
import { OnboardingCreatedPanel } from "../onboarding-created-panel"

const apiFetch = vi.mocked(apiFetchImport)

function jsonOk(body: unknown) {
  return { ok: true, json: async () => body } as unknown as Response
}

/** Route each of the three list calls to its own fixture. */
function routeTo({ crews = [], routines = [], pages = [] }: {
  crews?: unknown[]
  routines?: unknown[]
  pages?: unknown[]
}) {
  apiFetch.mockImplementation(async (url: string) => {
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
  afterEach(() => cleanup())

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
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/pipelines")) return { ok: false } as unknown as Response
      if (url.startsWith("/api/v1/pages")) return jsonOk([{ slug: "p", name: "A page", panel_count: 1 }])
      return jsonOk([])
    })
    render(<OnboardingCreatedPanel workspaceId="ws_1" />)
    await waitFor(() => expect(screen.getByTestId("onboarding-created-page")).toBeTruthy())
    expect(screen.queryAllByTestId("onboarding-created-routine")).toHaveLength(0)
  })
})
