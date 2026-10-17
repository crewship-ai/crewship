import { describe, it, expect, vi, afterEach, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn() }) }))

import { AgentInstructionsExtras } from "../agent-instructions-extras"

type Layer = "agent" | "crew" | "default"
function stub({ layer = "agent" as Layer, history = [] as unknown[], crewDefault = "You work in the Research crew." } = {}) {
  return vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    if (url.includes("/agents/a1/persona/history")) return new Response(JSON.stringify({ entries: history }))
    if (url.includes("/agents/a1/persona") && method === "DELETE") return new Response(null, { status: 204 })
    if (url.includes("/agents/a1/persona")) return new Response(JSON.stringify({ layer, from_default: layer === "default", content: "x" }))
    if (url.includes("/crews/c1/persona") && method === "PUT") return new Response(JSON.stringify({ layer: "crew" }))
    if (url.includes("/crews/c1/persona")) return new Response(JSON.stringify({ layer: "crew", content: crewDefault }))
    return new Response("{}", { status: 404 })
  })
}

const props = { workspaceId: "ws1", agentId: "a1", agentName: "Scout", crew: { id: "c1", name: "Research" }, canEdit: true }

describe("Agent Instructions extras", () => {
  beforeEach(() => cleanup())
  afterEach(() => vi.restoreAllMocks())

  it("Reset to crew default asks a ConfirmDialog, then deletes the agent layer", async () => {
    const spy = stub({ layer: "agent" })
    const onReset = vi.fn()
    render(<AgentInstructionsExtras {...props} onReset={onReset} />)
    fireEvent.click(await screen.findByRole("button", { name: "Reset to crew default…" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("Reset Scout’s instructions to the Research default?")).toBeInTheDocument()
    expect(spy.mock.calls.some(([, i]) => i?.method === "DELETE")).toBe(false)
    fireEvent.click(within(dialog).getByRole("button", { name: "Reset instructions" }))
    await waitFor(() => expect(spy.mock.calls.some(([u, i]) => String(u).includes("/agents/a1/persona") && i?.method === "DELETE")).toBe(true))
    await waitFor(() => expect(onReset).toHaveBeenCalled())
  })

  it("offers no reset while the agent already follows the crew default", async () => {
    stub({ layer: "crew" })
    render(<AgentInstructionsExtras {...props} onReset={vi.fn()} />)
    expect(await screen.findByText("Uses the Research default")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Reset to crew default…" })).not.toBeInTheDocument()
  })

  it("History lists the recorded versions of its instructions", async () => {
    stub({ history: [{ id: "v1", sha256: "abc", bytes: 120, written_at: "2026-10-10T10:00:00Z", written_by: "pavel" }] })
    render(<AgentInstructionsExtras {...props} onReset={vi.fn()} />)
    fireEvent.click(await screen.findByRole("button", { name: "History" }))
    expect(await screen.findByText(/pavel/)).toBeInTheDocument()
    expect(screen.getByText(/120 bytes/)).toBeInTheDocument()
  })

  it("shows the crew default and saves an edit of it", async () => {
    const spy = stub()
    render(<AgentInstructionsExtras {...props} onReset={vi.fn()} />)
    const card = await screen.findByRole("region", { name: "Crew default · Research" })
    expect(await within(card).findByText("You work in the Research crew.")).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Edit crew default" }))
    fireEvent.change(await screen.findByRole("textbox", { name: "Crew default instructions" }), { target: { value: "Cite sources." } })
    fireEvent.click(screen.getByRole("button", { name: "Save crew default" }))
    await waitFor(() => {
      const put = spy.mock.calls.find(([u, i]) => String(u).includes("/crews/c1/persona") && i?.method === "PUT")
      expect(put && JSON.parse(String(put[1]?.body))).toEqual({ content: "Cite sources." })
    })
  })

  it("has no Suggest improvements button: the server offers no generator", async () => {
    stub()
    render(<AgentInstructionsExtras {...props} onReset={vi.fn()} />)
    await screen.findByRole("button", { name: "History" })
    expect(screen.queryByRole("button", { name: /suggest improvements/i })).not.toBeInTheDocument()
  })

  it("read-only roles see no reset and no edit", async () => {
    stub({ layer: "agent" })
    render(<AgentInstructionsExtras {...props} canEdit={false} onReset={vi.fn()} />)
    await screen.findByRole("button", { name: "History" })
    expect(screen.queryByRole("button", { name: "Reset to crew default…" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Edit crew default" })).not.toBeInTheDocument()
  })
})
