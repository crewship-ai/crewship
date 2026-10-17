// Give to agents (Harbor 2): one binding per chosen agent, crew or the
// workspace, under one variable name; targets that already have the secret
// are not written again; a refusal keeps the dialog open with what failed.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { GiveToAgentsDialog } from "../give-to-agents-dialog"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: h.toast }))

const AGENTS = [
  { id: "ag-rev", name: "Reviewer", crew_id: "crew-code" },
  { id: "ag-fix", name: "Fixer", crew_id: "crew-code" },
  { id: "ag-solo", name: "Solo", crew_id: null },
]
const CREWS = { "crew-code": { id: "crew-code", name: "Code review", icon: null, color: null } }

function json(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body } as unknown as Response
}

let postStatus = 201
beforeEach(() => {
  postStatus = 201
  h.apiFetch.mockReset()
  h.toast.success.mockReset()
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.startsWith("/api/v1/agents?")) return json(AGENTS)
    if (url.startsWith("/api/v1/credentials/bindings?") && !init?.method) {
      return json({ bindings: [{ id: "b1", credential_id: "gh", scope: "AGENT", crew_id: null, agent_id: "ag-rev", slot: "GITHUB_TOKEN" }] })
    }
    if (init?.method === "POST") return json(postStatus < 400 ? { id: "new" } : { error: "taken" }, postStatus)
    return json({})
  })
})

function renderDialog() {
  const props = { workspaceId: "ws1", credential: { id: "gh", name: "GitHub token", provider: "GITHUB" }, open: true, onOpenChange: vi.fn(), crewsById: CREWS, onGiven: vi.fn() }
  render(<GiveToAgentsDialog {...props} />)
  return props
}

describe("GiveToAgentsDialog", () => {
  it("gives the secret to the chosen agents under the variable it already uses", async () => {
    const props = renderDialog()
    // Reviewer already has it: shown, not offered again.
    const reviewer = await screen.findByRole("checkbox", { name: "Give to Reviewer" })
    expect(reviewer).toBeDisabled()
    expect(screen.getByLabelText("Variable name")).toHaveValue("GITHUB_TOKEN")
    fireEvent.click(screen.getByRole("checkbox", { name: "Give to Fixer" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Give to Solo" }))
    fireEvent.click(screen.getByRole("button", { name: "Give to 2 agents" }))
    await waitFor(() => expect(props.onOpenChange).toHaveBeenCalledWith(false))
    const posts = h.apiFetch.mock.calls.filter(([, i]) => i?.method === "POST").map(([, i]) => JSON.parse(i.body))
    expect(posts).toEqual([
      { credential_id: "gh", scope: "AGENT", crew_id: "", agent_id: "ag-fix", slot: "GITHUB_TOKEN" },
      { credential_id: "gh", scope: "AGENT", crew_id: "", agent_id: "ag-solo", slot: "GITHUB_TOKEN" },
    ])
    expect(props.onGiven).toHaveBeenCalled()
    expect(h.toast.success).toHaveBeenCalled()
  })

  it("gives it to a crew, or to the whole workspace", async () => {
    renderDialog()
    await screen.findByRole("checkbox", { name: "Give to Fixer" })
    fireEvent.click(screen.getByRole("radio", { name: /Crews/ }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Give to Code review" }))
    fireEvent.click(screen.getByRole("button", { name: "Give to 1 crew" }))
    await waitFor(() => expect(h.apiFetch.mock.calls.some(([, i]) => i?.method === "POST")).toBe(true))
    expect(JSON.parse(h.apiFetch.mock.calls.find(([, i]) => i?.method === "POST")![1].body)).toMatchObject({ scope: "CREW", crew_id: "crew-code", agent_id: "" })
  })

  it("a refusal keeps the dialog open and says what did not land", async () => {
    postStatus = 409
    const props = renderDialog()
    fireEvent.click(await screen.findByRole("checkbox", { name: "Give to Fixer" }))
    fireEvent.click(screen.getByRole("button", { name: "Give to 1 agent" }))
    expect(await screen.findByText(/Not given — Fixer: GITHUB_TOKEN is already taken/)).toBeInTheDocument()
    expect(props.onOpenChange).not.toHaveBeenCalledWith(false)
  })

  it("refuses a variable name the runtime cannot use", async () => {
    renderDialog()
    fireEvent.click(await screen.findByRole("checkbox", { name: "Give to Fixer" }))
    fireEvent.change(screen.getByLabelText("Variable name"), { target: { value: "not a name" } })
    expect(screen.getByRole("button", { name: "Give to 1 agent" })).toBeDisabled()
    expect(within(screen.getByRole("dialog")).getByText(/letters, digits and underscores/)).toBeInTheDocument()
  })
})
