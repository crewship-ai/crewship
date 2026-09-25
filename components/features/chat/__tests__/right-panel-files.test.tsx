import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { ChatAgentProvider } from "../chat-agent-context"
import { RightPanel } from "../right-panel"
import { useArtifactStore } from "@/stores/artifact-store"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
  useCurrentWorkspaceId: () => "ws-1",
}))
vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: (_key: string, initial: unknown) => [initial, vi.fn()],
}))
vi.mock("next/dynamic", () => ({ default: () => () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const files = [
  { path: "crew-1/marena/site", name: "site", size: 0, is_dir: true, mod_time: "", },
  { path: "crew-1/marena/AGENTS.md", name: "AGENTS.md", size: 20, is_dir: false, mod_time: "", },
]

function renderPanel(props: Partial<React.ComponentProps<typeof RightPanel>> = {}) {
  return render(<ChatAgentProvider agent={{ id: "agent-1", name: "Mařena", slug: "marena", crewId: "crew-1", avatarSeed: "marena" }}>
    <RightPanel agentId="agent-1" workspaceId="ws-1" files={files} initialTab="files" {...props} />
  </ChatAgentProvider>)
}

beforeEach(() => {
  apiFetch.mockReset()
  useArtifactStore.getState().closeAll()
  apiFetch.mockImplementation((url: string) => Promise.resolve({ ok: true, json: async () => url.includes("subdir=")
    ? [{ path: "crew-1/marena/site/hero.js", name: "hero.js", size: 32, is_dir: false, mod_time: "" }]
    : [] }))
})
afterEach(cleanup)

describe("Chat agent context", () => {
  it("shows a concise section heading when the right rail supplies navigation", () => {
    renderPanel({ hideTabs: true })
    expect(screen.getByRole("heading", { name: "Files" })).toBeInTheDocument()
    expect(screen.getByText("Files available to Mařena")).toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /Agent card/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Files" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Artifacts" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Work" })).toBeNull()
  })

  it("keeps section switching available when the mobile rail is absent", () => {
    renderPanel()
    expect(screen.getByRole("button", { name: "Files" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Artifacts" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Work" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^Team$/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /^Crew$/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /^Workspace$/ })).toBeNull()
  })

  it("keeps the agent tree visible and selects a file for the main workspace", async () => {
    const onOpenFile = vi.fn()
    renderPanel({ onOpenFile, selectedFile: "crew-1/marena/site/hero.js" })
    expect(screen.queryByText("AGENTS.md")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /Show .* internal file/ }))
    expect(screen.getByText("AGENTS.md")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /site/i }))
    await waitFor(() => expect(screen.getByText("hero.js")).toBeInTheDocument())
    fireEvent.click(screen.getByText("hero.js"))
    expect(onOpenFile).toHaveBeenCalledWith({ path: "crew-1/marena/site/hero.js", name: "hero.js", scope: { kind: "agent" } })
    expect(screen.getByText("hero.js")).toBeInTheDocument()
    expect(screen.queryByText("Workspace-level files")).toBeNull()
  })

  it("reports file list failure and offers retry", () => {
    const retry = vi.fn()
    renderPanel({ files: [], filesError: "Couldn't load agent files.", onRetryFiles: retry })
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't load agent files.")
    fireEvent.click(screen.getByRole("button", { name: /Retry loading files/ }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it("lists previewable agent artifacts and opens the selected artifact", async () => {
    apiFetch.mockResolvedValue({ ok: true, json: async () => [
      { path: "crew-1/marena/report.pdf", name: "report.pdf", is_dir: false },
      { path: "crew-1/marena/AGENTS.md", name: "AGENTS.md", is_dir: false },
    ] })
    renderPanel({ initialTab: "artifacts" })
    fireEvent.click(await screen.findByRole("button", { name: /report.pdf/ }))
    expect(useArtifactStore.getState().activeId).toBe("agent-1:crew-1/marena/report.pdf")
    expect(useArtifactStore.getState().open).toBe(true)
    expect(screen.queryByRole("button", { name: /AGENTS.md/ })).toBeNull()
  })

  it("shows only work assigned to or authored by the selected agent", async () => {
    apiFetch.mockImplementation((url: string) => Promise.resolve({ ok: true, json: async () => url.includes("/issues?")
      ? [{ id: "issue-1", identifier: "COPY-12", title: "Review headline", status: "IN_PROGRESS" }]
      : [{ id: "routine-1", slug: "copy-review", name: "Copy review", author_agent_id: "agent-1" }, { id: "routine-2", slug: "other", name: "Other", author_agent_id: "agent-2" }] }))
    renderPanel({ initialTab: "work" })
    expect(await screen.findByRole("link", { name: /COPY-12/ })).toHaveAttribute("href", "/issues/COPY-12")
    expect(screen.queryByRole("link", { name: /Copy review/ })).toBeNull()
    fireEvent.click(screen.getByRole("tab", { name: /Routines/ }))
    expect(screen.getByRole("link", { name: /Copy review/ })).toHaveAttribute("href", "/routines?routine=copy-review")
    expect(screen.queryByRole("link", { name: /COPY-12/ })).toBeNull()
    expect(screen.queryByText("Other")).toBeNull()
    expect(apiFetch.mock.calls.some(([url]) => String(url).includes("assignee_id=agent-1"))).toBe(true)
  })
})
