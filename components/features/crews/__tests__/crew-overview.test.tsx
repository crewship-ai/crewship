import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

vi.mock("@/components/features/crews/memory-workspace", () => ({ useWorkspaceResource: (url: string) => ({ key: url, error: "offline" }) }))
vi.mock("@/components/features/crews/crew-activity-feed", () => ({ CrewActivityFeed: () => <p>recent activity</p> }))

import { CrewOverview, miseTools } from "@/components/features/crews/crew-overview"
import type { CrewNeed } from "@/components/features/crews/crew-needs"
import type { CrewRecord } from "@/components/features/crews/crew-canvas-tabs/types"

afterEach(cleanup)

const crew = {
  id: "c1", workspace_id: "ws", name: "Ops", slug: "ops", description: null, color: null, icon: null, avatar_style: null,
  issue_prefix: "OPS", network_mode: "restricted", allowed_domains: ["api.linear.app", "github.com"], container_memory_mb: 4096,
  container_cpus: 2, container_ttl_hours: 4, runtime_image: "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
  devcontainer_config: null, mise_config: "[tools]\nnode = \"22.11.0\"\npython = \"3.12.6\"\n", escalation_config: null, cached_image: null,
  created_at: "", updated_at: "",
} as CrewRecord

const needs: CrewNeed[] = [
  { id: "ops:needs-provision", tone: "warn", icon: "build", title: "Container image needs rebuild", detail: "Runtime config changed", action: { kind: "build", label: "Build now" } },
  { id: "ops:gap:gh", tone: "warn", icon: "credential", title: "gh is missing from the image", detail: "GitHub token", action: { kind: "install", label: "Install gh", feature: "ghcr.io/devcontainers/features/github-cli:1", featureId: "github-cli", tool: "gh" } },
  { id: "ops:waiting:a2", tone: "warn", icon: "decision", title: "Auditor is waiting on your decision", detail: "", action: { kind: "link", label: "Review", href: "/inbox?agent=auditor" } },
]

function renderOverview(extra: Partial<React.ComponentProps<typeof CrewOverview>> = {}) {
  const props = {
    workspaceId: "ws",
    crew,
    agents: [{ id: "a1", name: "Opsy", slug: "opsy", status: "RUNNING", role_title: "Site reliability", agent_role: "LEAD" }],
    needs,
    onBuild: vi.fn(),
    onInstall: vi.fn(),
    containers: [{ name: "crewship-team-ops", image: "x", status: "running", kind: "crew", cpu_percent: 38, memory_mb: 1946 }],
    provisioning: "needs_provision" as const,
    gapCount: 1,
    integrations: [],
    onSelectAgent: vi.fn(),
    onTab: vi.fn(),
    onNewAgent: vi.fn(),
    onHire: vi.fn(),
    ...extra,
  }
  render(<CrewOverview {...props} />)
  return props
}

describe("CrewOverview", () => {
  it("needs card lists provisioning, credential gaps and pending hires with one verb each", () => {
    const p = renderOverview()
    const card = screen.getByTestId("crew-needs-you")
    fireEvent.click(within(card).getByRole("button", { name: "Build now" }))
    expect(p.onBuild).toHaveBeenCalled()
    fireEvent.click(within(card).getByRole("button", { name: "Install gh" }))
    expect(p.onInstall).toHaveBeenCalledWith(expect.objectContaining({ id: "ops:gap:gh" }))
    expect(within(card).getByRole("link", { name: "Review" })).toHaveAttribute("href", "/inbox?agent=auditor")
  })

  it("no needs card when nothing waits", () => {
    renderOverview({ needs: [] })
    expect(screen.queryByTestId("crew-needs-you")).toBeNull()
  })

  it("health card reads container, image, access and network", () => {
    const p = renderOverview()
    const health = screen.getByTestId("crew-health")
    expect(within(health).getByText("Running")).toBeInTheDocument()
    expect(within(health).getByText(/38 % CPU/)).toBeInTheDocument()
    expect(within(health).getByText("node 22.11")).toBeInTheDocument()
    expect(within(health).getByText("Behind · rebuild")).toBeInTheDocument()
    expect(within(health).getByText(/1 credential gap/)).toBeInTheDocument()
    expect(within(health).getByText(/2 allowed/)).toBeInTheDocument()
    fireEvent.click(within(health).getByRole("button", { name: /Environment/ }))
    expect(p.onTab).toHaveBeenCalledWith("env")
  })

  // The chips themselves are pinned in parity-crew-works-on.test.tsx.
  it("works-on is a card of its own that never leaves the page blank", () => {
    renderOverview()
    expect(screen.getByTestId("crew-works-on")).toBeInTheDocument()
  })

  it("the doing card opens a running agent", () => {
    const p = renderOverview()
    fireEvent.click(screen.getByRole("button", { name: "Open" }))
    expect(p.onSelectAgent).toHaveBeenCalledWith("opsy")
  })

  it("reads mise [tools] pins", () => {
    expect(miseTools('[env]\nA = "1"\n[tools]\nnode = "22.11.0"\n"npm:pnpm" = "9"\n')).toEqual([
      { name: "node", version: "22.11.0" },
      { name: "npm:pnpm", version: "9" },
    ])
  })
})
