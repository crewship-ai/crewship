import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { ImportRoutineDialog, definitionAgents } from "../routine-import-dialog"

const h = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({
  useWorkspaceAgentDirectory: () => ({ agents: [{ id: "a1", slug: "scout", name: "Scout", crew_id: "crew_research" }], error: false }),
}))

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, clone() { return this }, text: async () => JSON.stringify(body) })
const bundle = JSON.stringify({
  format: "crewship-pipeline-bundle/v1",
  pipeline: { slug: "weekly-scan", name: "Weekly scan", description: "Scans", definition: { name: "weekly-scan", steps: [{ id: "a", type: "agent_run", agent_slug: "scout", prompt: "x" }, { id: "b", type: "agent_run", agent_slug: "writer", prompt: "y" }] } },
})
const crews = [
  { id: "crew_research", name: "Research" },
  { id: "crew_ops", name: "Ops" },
]

beforeEach(() => h.fetcher.mockReset())

describe("definitionAgents", () => {
  it("names the agents of the steps and loop bodies", () => {
    expect(definitionAgents({ steps: [{ id: "a", agent_slug: "x" }, { id: "l", type: "foreach", foreach: { steps: [{ id: "c", agent_slug: "y" }] } }] })).toEqual(["x", "y"])
  })
})

describe("<ImportRoutineDialog>", () => {
  it("JSON bundle becomes a draft in the chosen crew, never a live routine", async () => {
    h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (String(url).endsWith("/pipelines/drafts") && init?.method === "POST") return json({ ...JSON.parse(String(init.body)), id: "d1", revision: 1 })
      return json([])
    })
    const imported = vi.fn()
    render(<ImportRoutineDialog workspaceId="ws" crews={crews} onClose={() => {}} onImported={imported} />)
    const save = screen.getByRole("button", { name: "Import as draft" })
    fireEvent.change(screen.getByLabelText("Or paste it"), { target: { value: bundle } })
    // Crew is required and not picked for you.
    expect(save).toBeDisabled()
    fireEvent.click(screen.getByRole("radio", { name: "Research" }))
    const checks = within(screen.getByTestId("import-checks"))
    expect(checks.getByText(/Valid routine · Weekly scan · 2 steps/)).toBeInTheDocument()
    expect(checks.getByText(/writer is not in Research/)).toBeInTheDocument()
    fireEvent.click(save)
    await waitFor(() => expect(imported).toHaveBeenCalledWith("weekly-scan"))
    const urls = h.fetcher.mock.calls.map(([u]) => String(u))
    expect(urls.some((u) => u.endsWith("/pipelines/import"))).toBe(false)
    const post = h.fetcher.mock.calls.find(([, init]) => init?.method === "POST")!
    const body = JSON.parse(String(post[1].body))
    expect(body).toMatchObject({ id: "", revision: 0, slug: "weekly-scan", document: { slug: "weekly-scan", name: "Weekly scan", author_crew_id: "crew_research", definition: { name: "weekly-scan" } } })
  })

  it("409 shows a rename field and the reason in words, not a raw status", async () => {
    h.fetcher.mockImplementation(async (_url: string, init?: RequestInit) => {
      if (init?.method !== "POST") return json([])
      const sent = JSON.parse(String(init?.body))
      if (sent.slug === "weekly-scan") return json({ error: "conflict" }, 409)
      return json({ ...sent, id: "d2", revision: 1 })
    })
    const imported = vi.fn()
    render(<ImportRoutineDialog workspaceId="ws" crews={crews} defaultCrewId="crew_ops" onClose={() => {}} onImported={imported} />)
    fireEvent.change(screen.getByLabelText("Or paste it"), { target: { value: bundle } })
    fireEvent.click(screen.getByRole("button", { name: "Import as draft" }))
    expect(await screen.findByText("A routine called “weekly-scan” already exists. Pick another ID.")).toBeInTheDocument()
    expect(screen.queryByText(/409/)).toBeNull()
    fireEvent.change(screen.getByLabelText("ID"), { target: { value: "weekly-scan-2" } })
    fireEvent.click(screen.getByRole("button", { name: "Import as draft" }))
    await waitFor(() => expect(imported).toHaveBeenCalledWith("weekly-scan-2"))
  })

  // The import never replaces: an ID in use is refused and nothing saved is
  // overwritten. The shell carries the same breadcrumb as every other door.
  it("does not promise a replace, and carries the breadcrumb", () => {
    render(<ImportRoutineDialog workspaceId="ws" crews={crews} onClose={() => {}} onImported={() => {}} />)
    const body = document.querySelector('[data-slot="dialog-content"]')?.textContent ?? ""
    expect(body).not.toMatch(/existing routine is replaced/i)
    expect(body).toMatch(/nothing already saved is overwritten/i)
    expect(screen.getByText("Routines")).toBeInTheDocument()
  })

  it("explains a paste that is not a routine", () => {
    render(<ImportRoutineDialog workspaceId="ws" crews={crews} onClose={() => {}} onImported={() => {}} />)
    fireEvent.change(screen.getByLabelText("Or paste it"), { target: { value: "not json" } })
    expect(screen.getByText(/This is not valid JSON/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Import as draft" })).toBeDisabled()
  })
})
