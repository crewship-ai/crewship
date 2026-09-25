import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { ChatAgentProvider } from "../chat-agent-context"
import { RightPanel } from "../right-panel"
import { useArtifactStore } from "@/stores/artifact-store"
import { useDrawerStore } from "@/stores/drawer-store"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
  useCurrentWorkspaceId: () => "ws-1",
}))
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const outputs = [
  { path: "crew-1/marena/report.pdf", name: "report.pdf", is_dir: false },
  { path: "crew-1/marena/plan.csv", name: "plan.csv", is_dir: false },
  { path: "crew-1/marena/runs/msg_123/AGENTS.md", name: "AGENTS.md", is_dir: false },
  { path: "crew-1/marena/runs/msg_123/report.pdf", name: "report.pdf", is_dir: false },
  { path: "crew-1/marena/brief.md", name: "brief.md", is_dir: false },
]

function renderPanel(props: Partial<React.ComponentProps<typeof RightPanel>> = {}) {
  return render(<ChatAgentProvider agent={{ id: "agent-1", name: "Mařena", slug: "marena", crewId: "crew-1", avatarSeed: "marena" }}>
    <RightPanel agentId="agent-1" workspaceId="ws-1" initialTab="artifacts" {...props} />
  </ChatAgentProvider>)
}

beforeEach(() => {
  apiFetch.mockReset()
  useArtifactStore.getState().closeAll()
  useDrawerStore.setState({ open: true, activeTab: "artifacts" })
  apiFetch.mockImplementation((url: string) => Promise.resolve({ ok: true, json: async () =>
    String(url).includes("/files?") ? outputs : [] }))
})
afterEach(cleanup)

describe("Chat agent context", () => {
  it("shows the unified Artifacts panel without a Files or configuration entry", async () => {
    renderPanel({ hideTabs: true })
    expect(screen.getByRole("heading", { name: "Artifacts" })).toBeInTheDocument()
    expect(screen.getByText("Documents and outputs for Mařena")).toBeInTheDocument()
    expect(await screen.findByRole("button", { name: /plan.csv/ })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /AGENTS.md/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /brief.md/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Files" })).toBeNull()
  })

  it("switches between Artifacts and Work when the mobile rail is absent", async () => {
    renderPanel()
    expect(screen.queryByRole("tab", { name: "Files" })).toBeNull()
    fireEvent.click(screen.getByRole("tab", { name: "Work" }))
    expect(screen.getByRole("heading", { name: "Work" })).toBeInTheDocument()
    expect(useDrawerStore.getState().activeTab).toBe("work")
    fireEvent.click(screen.getByRole("tab", { name: "Artifacts" }))
    expect(await screen.findByRole("button", { name: /report.pdf/ })).toBeInTheDocument()
  })

  it("opens a deliverable through the live artifact pane", async () => {
    renderPanel()
    fireEvent.click(await screen.findByRole("button", { name: /plan.csv/ }))
    expect(useArtifactStore.getState().activeId).toBe("agent-1:crew-1/marena/plan.csv")
    expect(useArtifactStore.getState().open).toBe(true)
  })

  it("shows only work assigned to or authored by the selected agent", async () => {
    apiFetch.mockImplementation((url: string) => Promise.resolve({ ok: true, json: async () => String(url).includes("/issues?")
      ? [{ id: "issue-1", identifier: "COPY-12", title: "Review headline", status: "IN_PROGRESS" }]
      : [{ id: "routine-1", slug: "copy-review", name: "Copy review", author_agent_id: "agent-1" }, { id: "routine-2", slug: "other", name: "Other", author_agent_id: "agent-2" }] }))
    renderPanel({ initialTab: "work" })
    expect(await screen.findByRole("link", { name: /COPY-12/ })).toHaveAttribute("href", "/issues/COPY-12")
    fireEvent.click(screen.getByRole("tab", { name: /Routines/ }))
    expect(screen.getByRole("link", { name: /Copy review/ })).toHaveAttribute("href", "/routines?routine=copy-review")
    expect(screen.queryByText("Other")).toBeNull()
  })
})
