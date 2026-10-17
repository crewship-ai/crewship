import { describe, it, expect, vi, afterEach, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn() }) }))

import { AgentSkillsPicker, agentSkillDiff } from "../agent-skills-picker"
import type { AgentSkillRow } from "../agent-canvas-tabs/types"

// A-TOOLS "Add skill" (K09) opens the Give to picker turned around: one agent,
// the workspace's skills. Same contract as the Skills area's picker (L11):
// tri-state-free checklist, "N added · M removed", Apply sends only the diff,
// a partial failure keeps the dialog open with what failed and a Retry.

const LIBRARY = [
  { id: "s1", name: "web-research", display_name: "Web research" },
  { id: "s2", name: "pdf-reports", display_name: "PDF reports" },
  { id: "s3", name: "code-review", display_name: "Code review" },
]
const HELD: AgentSkillRow[] = [
  { id: "as1", skill_id: "s1", enabled: true, skill: { id: "s1", name: "web-research", slug: "web-research", display_name: "Web research" } },
]

function stub({ failPost = new Set<string>() } = {}) {
  return vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    if (url.includes("/api/v1/skills?")) return new Response(JSON.stringify(LIBRARY))
    if (url.includes("/agents/a1/skills") && method === "POST") {
      const id = JSON.parse(String(init?.body)).skill_id as string
      return failPost.has(id) ? new Response(JSON.stringify({ error: "nope" }), { status: 500 }) : new Response(JSON.stringify({ id: `as-${id}` }), { status: 201 })
    }
    if (url.includes("/agents/a1/skills/") && method === "DELETE") return new Response(null, { status: 204 })
    return new Response("{}", { status: 404 })
  })
}

const props = { open: true, agentId: "a1", agentName: "Scout", workspaceId: "ws1", assigned: HELD }

function calls(spy: ReturnType<typeof stub>, method: string) {
  return spy.mock.calls.filter(([u, i]) => String(u).includes("/agents/a1/skills") && (i?.method ?? "GET") === method)
}

describe("agentSkillDiff", () => {
  it("adds what is newly checked and removes held skills that were unchecked", () => {
    const held = new Map([["s1", "as1"], ["s2", "as2"]])
    expect(agentSkillDiff(held, new Set(["s2", "s3"]))).toEqual({ add: ["s3"], remove: [{ skillId: "s1", assignmentId: "as1" }] })
    expect(agentSkillDiff(held, new Set(["s1", "s2"]))).toEqual({ add: [], remove: [] })
  })
})

describe("Give skills to an agent (A-TOOLS, K09)", () => {
  beforeEach(() => cleanup())
  afterEach(() => vi.restoreAllMocks())

  it("lists the library with held skills checked; Apply waits for a change", async () => {
    stub()
    render(<AgentSkillsPicker {...props} onClose={vi.fn()} onApplied={vi.fn()} />)
    const dialog = await screen.findByRole("dialog", { name: /Give skills to Scout/ })
    expect(await within(dialog).findByRole("checkbox", { name: "Web research" })).toBeChecked()
    expect(within(dialog).getByRole("checkbox", { name: "PDF reports" })).not.toBeChecked()
    expect(within(dialog).getByText("0 added · 0 removed")).toBeInTheDocument()
    expect(within(dialog).getByRole("button", { name: "Apply" })).toBeDisabled()
  })

  it("Apply sends only the difference, then closes", async () => {
    const spy = stub()
    const onClose = vi.fn()
    const onApplied = vi.fn()
    render(<AgentSkillsPicker {...props} onClose={onClose} onApplied={onApplied} />)
    const dialog = await screen.findByRole("dialog", { name: /Give skills to Scout/ })
    fireEvent.click(await within(dialog).findByRole("checkbox", { name: "PDF reports" }))
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Web research" }))
    expect(within(dialog).getByText("1 added · 1 removed")).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply" }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(calls(spy, "POST").map(([, i]) => JSON.parse(String(i?.body)))).toEqual([{ skill_id: "s2" }])
    expect(calls(spy, "DELETE").map(([u]) => String(u))).toEqual([expect.stringContaining("/agents/a1/skills/as1")])
    expect(onApplied).toHaveBeenCalled()
  })

  it("a partial failure keeps the dialog open with the failed skill and retries only that", async () => {
    const spy = stub({ failPost: new Set(["s3"]) })
    const onClose = vi.fn()
    render(<AgentSkillsPicker {...props} onClose={onClose} onApplied={vi.fn()} />)
    const dialog = await screen.findByRole("dialog", { name: /Give skills to Scout/ })
    fireEvent.click(await within(dialog).findByRole("checkbox", { name: "PDF reports" }))
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Code review" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply" }))
    expect(await screen.findByText(/1 change failed for Code review/)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    const before = calls(spy, "POST").length
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    await waitFor(() => expect(calls(spy, "POST").length).toBe(before + 1))
    expect(JSON.parse(String(calls(spy, "POST").at(-1)?.[1]?.body))).toEqual({ skill_id: "s3" })
  })

  it("search narrows the list", async () => {
    stub()
    render(<AgentSkillsPicker {...props} onClose={vi.fn()} onApplied={vi.fn()} />)
    const dialog = await screen.findByRole("dialog", { name: /Give skills to Scout/ })
    await within(dialog).findByRole("checkbox", { name: "PDF reports" })
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Search skills" }), { target: { value: "code" } })
    expect(within(dialog).getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["Code review"])
  })
})

describe("SkillsManager › Add skill", () => {
  beforeEach(() => cleanup())
  afterEach(() => vi.restoreAllMocks())

  it("opens the Give to picker instead of the old Assign skill sheet", async () => {
    vi.spyOn(global, "fetch").mockImplementation(async (input) => {
      const url = String(input)
      if (url.includes("/agents/a1/skills?")) return new Response(JSON.stringify(HELD))
      if (url.includes("/api/v1/skills?")) return new Response(JSON.stringify(LIBRARY))
      return new Response("{}", { status: 404 })
    })
    const { SkillsManager } = await import("../agent-canvas-managers")
    render(<SkillsManager agentId="a1" agentSlug="scout" agentName="Scout" workspaceId="ws1" onChange={vi.fn()} />)
    expect(screen.queryByRole("button", { name: /Assign skill/ })).not.toBeInTheDocument()
    fireEvent.click(await screen.findByRole("button", { name: "Add skill" }))
    const dialog = await screen.findByRole("dialog", { name: /Give skills to Scout/ })
    expect(await within(dialog).findByRole("checkbox", { name: "Web research" })).toBeChecked()
  })
})
