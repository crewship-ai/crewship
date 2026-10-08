import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ toastError: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: { error: (...a: unknown[]) => h.toastError(...a), success: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(() => Promise.resolve(new Response("{}", { status: 404 }))) }))
vi.mock("@/components/features/mcp/hooks/use-credentials", () => ({
  useCredentials: () => ({ credentials: [], loading: false, fetchCredentials: vi.fn(), addCredential: vi.fn() }),
}))

import { ExpandedPanel } from "../expanded-panel"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import type { CrewIntegration } from "../types"

const SERVER: CrewIntegration = {
  id: "int-1", crew_id: "crew-1", crew_name: "Ops", crew_slug: "ops",
  name: "github", display_name: "GitHub", transport: "stdio", endpoint: null,
  command: "npx", args_json: JSON.stringify(["-y", "server-github"]), env_json: JSON.stringify({ GITHUB_TOKEN: "{{cred:gh}}" }),
  icon: null, enabled: true, created_at: "", updated_at: "", agent_binding_count: 0,
} as CrewIntegration

function show(onPatch = vi.fn(() => Promise.resolve())) {
  render(
    <PageSaveProvider>
      <ExpandedPanel server={SERVER} crews={[]} agents={[]} agentBindings={{}} bindingIds={{}} confirmDeleteId={null}
        canManage workspaceId={null} onPatch={onPatch} onCrewMove={vi.fn()} onAgentToggle={vi.fn()}
        onDelete={vi.fn()} onConfirmDeleteChange={vi.fn()} onRefresh={vi.fn()} />
      <PageSaveBar />
    </PageSaveProvider>,
  )
  return onPatch
}
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })

beforeEach(() => h.toastError.mockReset())
afterEach(() => cleanup())

// The connector's command, URL and env decide what runs inside the crew's
// containers. Editing them is a draft on the page's Save bar: nothing is
// written on blur, one Save sends one PATCH.
describe("MCP server panel on the page Save bar", () => {
  it("counts each edited field and writes nothing before Save, not even on blur", () => {
    const onPatch = show()
    const cmd = screen.getByLabelText("Command")
    fireEvent.change(cmd, { target: { value: "uvx" } })
    fireEvent.blur(cmd)
    const key = screen.getByLabelText("Environment variable key 1")
    fireEvent.change(key, { target: { value: "GH_TOKEN" } })
    fireEvent.blur(key)
    expect(bar()).toHaveTextContent("2 unsaved changes")
    expect(onPatch).not.toHaveBeenCalled()
  })

  it("Save sends one PATCH with the changed fields", async () => {
    const onPatch = show()
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: "uvx" } })
    fireEvent.click(screen.getByLabelText("Remove environment variable GITHUB_TOKEN"))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(onPatch).toHaveBeenCalledTimes(1))
    expect(onPatch).toHaveBeenCalledWith({ command: "uvx", env_json: "{}" })
  })

  it("Discard puts the saved values back", () => {
    const onPatch = show()
    const cmd = screen.getByLabelText("Command") as HTMLInputElement
    fireEvent.change(cmd, { target: { value: "uvx" } })
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(cmd.value).toBe("npx")
    expect(bar()).toBeNull()
    expect(onPatch).not.toHaveBeenCalled()
  })

  it("a refused save keeps the edits and says so in the corner", async () => {
    show(vi.fn(() => Promise.reject(new Error("command not allowed"))))
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: "rm" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(h.toastError).toHaveBeenCalled())
    expect(h.toastError.mock.calls[0][0]).toBe("Couldn’t save github")
    expect((screen.getByLabelText("Command") as HTMLInputElement).value).toBe("rm")
  })
})
