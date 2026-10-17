import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"
import { _resetWorkspaceStoreForTests, refreshWorkspaceSettings } from "@/hooks/use-workspace"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), loading: vi.fn() }) }))

import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { CrewEnvironment } from "../crew-environment"
import type { CrewRecord } from "../crew-canvas-tabs/types"

const CREW = {
  id: "c1", workspace_id: "ws-1", name: "Research", slug: "research", description: "", icon: "code", color: "blue",
  container_memory_mb: 4096, container_cpus: 2, container_ttl_hours: 4,
  network_mode: "restricted", allowed_domains: ["example.com"], network_mode_enforced: true,
  runtime_image: "", mise_config: JSON.stringify({ tools: { node: "22.11.0", python: "3.12" } }),
  devcontainer_config: JSON.stringify({ image: "mcr.microsoft.com/devcontainers/javascript-node:22", features: { "ghcr.io/devcontainers/features/github-cli:1": {} }, containerEnv: { TZ: "UTC" } }),
  allow_private_endpoints: false,
} as unknown as CrewRecord

function stub() {
  return vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    if (url.includes("/port-expose")) return new Response("[]")
    if (url.includes("/crews/c1/provision") && method === "POST") return new Response("{}", { status: 202 })
    if (url.includes("/crews/c1/rebuild") && method === "POST") return new Response("{}", { status: 202 })
    if (url.includes("/container-stop") && method === "POST") return new Response("{}")
    return new Response("{}", { status: 404 })
  })
}

function renderEnv(props: Partial<Parameters<typeof CrewEnvironment>[0]> = {}) {
  return render(
    <PageSaveProvider>
      <CrewEnvironment
        workspaceId="ws-1"
        crew={CREW}
        onSaved={vi.fn()}
        canEdit
        isAdmin
        containers={[{ name: "crew-research", image: "crewship/research:abc", status: "running", kind: "crew", cpu_percent: 12, memory_mb: 900 }]}
        runningAgents={2}
        {...props}
      />
      <PageSaveBar />
    </PageSaveProvider>,
  )
}

const cardTitles = () => screen.getAllByRole("region").map((r) => r.getAttribute("aria-label")).filter((l) => l && l !== "Unsaved changes")

describe("Crew Environment (C-ENV)", () => {
  beforeEach(async () => {
    cleanup()
    _resetWorkspaceStoreForTests()
    const boot = vi.spyOn(global, "fetch").mockImplementation(async (input) =>
      String(input) === "/api/v1/workspaces"
        ? new Response(JSON.stringify([{ id: "ws-1", name: "WS", slug: "ws", currentUserRole: "OWNER", currentUserCapabilities: [] }]))
        : new Response("{}", { status: 404 }))
    await refreshWorkspaceSettings()
    boot.mockRestore()
  })
  afterEach(() => vi.restoreAllMocks())

  it("the segment filters the cards", () => {
    stub()
    renderEnv()
    expect(cardTitles()).toEqual(expect.arrayContaining(["Container", "Image", "Tools and versions", "Features", "Network", "Resources", "Services next to the agents", "Preview links", "Environment variables", "Security", "Folders in the container"]))
    fireEvent.click(screen.getByRole("radio", { name: "Tools" }))
    expect(cardTitles()).toEqual(["Tools and versions", "Features"])
    fireEvent.click(screen.getByRole("radio", { name: "Container and image" }))
    expect(cardTitles()).toEqual(["Container", "Image"])
    fireEvent.click(screen.getByRole("radio", { name: "Advanced" }))
    expect(cardTitles()).toEqual(expect.arrayContaining(["Environment variables", "Security", "Folders in the container"]))
  })

  it("lists pinned tools and features; removing one is a pending change on the one save bar, with the restart warning on screen", () => {
    stub()
    renderEnv()
    expect(screen.getByText(/Saving network, resources or tools restarts the container/)).toBeInTheDocument()
    const tools = screen.getByRole("region", { name: "Tools and versions" })
    expect(within(tools).getByText("node")).toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument()
    fireEvent.click(within(tools).getByRole("button", { name: "Remove node" }))
    expect(within(tools).queryByText("node")).not.toBeInTheDocument()
    fireEvent.click(within(screen.getByRole("region", { name: "Features" })).getByRole("button", { name: /^Remove / }))
    const bar = screen.getByRole("region", { name: "Unsaved changes" })
    expect(bar).toHaveTextContent("2 unsaved changes")
  })

  it("Rebuild offers Build changes (cached) and Rebuild from scratch", async () => {
    const spy = stub()
    renderEnv()
    fireEvent.click(screen.getByRole("button", { name: /Rebuild…/ }))
    const group = await screen.findByRole("radiogroup", { name: "How to build" })
    expect(within(group).getAllByRole("radio").map((r) => r.querySelector("b")?.textContent)).toEqual(["Build changes", "Rebuild from scratch"])
    fireEvent.click(within(group).getByRole("radio", { name: /Rebuild from scratch/ }))
    fireEvent.click(screen.getByRole("button", { name: "Build" }))
    await waitFor(() => expect(spy.mock.calls.some(([u, i]) => String(u).includes("/crews/c1/rebuild") && i?.method === "POST")).toBe(true))
  })

  it("Stop container asks a warning confirm that counts the running agents", async () => {
    const spy = stub()
    renderEnv()
    fireEvent.click(screen.getByRole("button", { name: "Stop container" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("2 running agents stop mid-run")).toBeInTheDocument()
    expect(spy.mock.calls.some(([u]) => String(u).includes("/container-stop"))).toBe(false)
    fireEvent.click(within(dialog).getByRole("button", { name: "Stop container" }))
    await waitFor(() => expect(spy.mock.calls.some(([u, i]) => String(u).includes("/container-stop") && i?.method === "POST")).toBe(true))
  })

  it("Services carries the service disk quotas that left Settings", () => {
    stub()
    renderEnv({ patch: vi.fn() })
    const services = screen.getByRole("region", { name: "Services next to the agents" })
    expect(within(services).getByRole("region", { name: "Service disk quotas" })).toBeInTheDocument()
  })

  it("a viewer sees the environment without its controls", () => {
    stub()
    renderEnv({ canEdit: false, isAdmin: false })
    expect(screen.getByText("A manager or an admin changes the environment.")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Stop container" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Add a tool/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Rebuild…/ })).not.toBeInTheDocument()
  })
})
