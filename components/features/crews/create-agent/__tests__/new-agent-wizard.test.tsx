import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"
import { _resetWorkspaceStoreForTests, refreshWorkspaceSettings } from "@/hooks/use-workspace"

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn(), push: vi.fn() }) }))
const toastWarning = vi.fn()
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: (...a: unknown[]) => toastWarning(...a) }) }))

import { CreateAgentDialog } from "../create-agent-dialog"

async function bootstrap(role: string) {
  _resetWorkspaceStoreForTests()
  const spy = vi.spyOn(global, "fetch").mockImplementation(async (input) =>
    String(input) === "/api/v1/workspaces"
      ? new Response(JSON.stringify([{ id: "ws-1", name: "WS", slug: "ws", currentUserRole: role, currentUserCapabilities: [] }]))
      : new Response("{}", { status: 404 }),
  )
  await refreshWorkspaceSettings()
  spy.mockRestore()
}

const CREWS = [{ id: "c1", slug: "research", name: "Research" }]
const SEAT = { id: "cred-1", name: "Anthropic production", provider: "ANTHROPIC", status: "ACTIVE", login: { provider: "ANTHROPIC", mode: "api_key", refresh: { supported: false } } }
const SOURCE = { id: "src", workspace_id: "ws-1", crew_id: "c1", name: "Scout", slug: "scout", agent_role: "AGENT", cli_adapter: "CODEX", llm_provider: "OPENAI", llm_model: "gpt-5.1", tool_profile: "MINIMAL", timeout_seconds: 900, memory_enabled: true, system_prompt: "Check every source." }

function stub(overrides: (url: string, init?: RequestInit) => Response | null = () => null) {
  return vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const o = overrides(url, init)
    if (o) return o
    if (url.includes("/api/v1/integrations")) return new Response("[]")
    if (url.includes("/api/v1/notification-channels")) return new Response('{"channels":[]}')
    if (url.includes("/api/v1/skills?")) return new Response(JSON.stringify([{ id: "sk1", name: "g2-scraper" }, { id: "sk2", name: "report-writer" }]))
    if (url.includes("/api/v1/credentials?")) return new Response(JSON.stringify([SEAT]))
    if (url.includes("/api/v1/credentials/bindings") && init?.method === "POST") return new Response("{}", { status: 201 })
    if (url.includes("/api/v1/credentials/bindings")) return new Response(JSON.stringify({ bindings: [] }))
    if (url.includes("/api/v1/agents/new-1/skills")) return new Response("{}", { status: 201 })
    if (url.includes("/api/v1/agents/src/skills")) return new Response(JSON.stringify([{ skill_id: "sk2", skill_name: "report-writer" }]))
    if (url.includes("/api/v1/agents/src?")) return new Response(JSON.stringify(SOURCE))
    if (url.match(/\/api\/v1\/agents\?workspace_id=[^&]+&limit=/)) return new Response(JSON.stringify([SOURCE]))
    if (url.includes("/api/v1/agents?") && init?.method === "POST") return new Response(JSON.stringify({ id: "new-1", name: "Fact checker", slug: "fact-checker" }), { status: 201 })
    return new Response("{}", { status: 404 })
  })
}

function renderWizard() {
  const props = { workspaceId: "ws-1", open: true, onOpenChange: vi.fn(), defaultCrewSlug: "research", crews: CREWS, onCreated: vi.fn() }
  render(<CreateAgentDialog {...props} />)
  return props
}

const primary = (name: string | RegExp) => screen.getByRole("button", { name })

describe("New agent wizard", () => {
  beforeEach(async () => { cleanup(); toastWarning.mockReset(); await bootstrap("OWNER") })
  afterEach(() => vi.restoreAllMocks())

  it("is four steps Who → Model → Access → Instructions at M 640, titled for the crew", () => {
    stub()
    renderWizard()
    expect(screen.getByText("New agent in Research")).toBeInTheDocument()
    const steps = screen.getByRole("navigation", { name: "Steps" })
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent?.replace(/^\d/, ""))).toEqual(["Who", "Model", "Access", "Instructions"])
    expect(document.querySelector('[data-slot="dialog-content"]')!.className).toContain("sm:max-w-[640px]")
    expect(screen.getAllByText("Step 1 of 4").length).toBeGreaterThan(0)
    expect(primary("Continue")).toBeDisabled()
  })

  it("the footer keeps 'Step x of 4' while Continue is blocked, with the reason beside it", () => {
    stub()
    renderWizard()
    const footer = primary("Continue").closest("div")!
    expect(within(footer).getByTestId("new-agent-step")).toHaveTextContent("Step 1 of 4")
    expect(footer).toHaveTextContent(/Name must be at least 2 characters/)
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Fact checker" } })
    fireEvent.click(primary("Continue"))
    expect(within(primary("Continue").closest("div")!).getByTestId("new-agent-step")).toHaveTextContent("Step 2 of 4")
    expect(primary("Continue").closest("div")!).toHaveTextContent(/Choose a provider and model/)
  })

  it("Back keeps what was typed; Model needs a provider before Continue", () => {
    stub()
    renderWizard()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Fact checker" } })
    fireEvent.click(primary("Continue"))
    expect(screen.getByRole("radio", { name: "Anthropic" })).toBeInTheDocument()
    expect(primary("Continue")).toBeDisabled()
    fireEvent.click(screen.getByRole("radio", { name: "Anthropic" }))
    expect(primary("Continue")).not.toBeDisabled()
    fireEvent.click(primary("Back"))
    expect((screen.getByPlaceholderText("Filip") as HTMLInputElement).value).toBe("Fact checker")
  })

  it("Access binds Pays with and assigns the picked skills after the agent is created", async () => {
    const spy = stub()
    const props = renderWizard()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Fact checker" } })
    fireEvent.click(primary("Continue"))
    fireEvent.click(screen.getByRole("radio", { name: "Anthropic" }))
    fireEvent.click(primary("Continue"))
    fireEvent.click(screen.getByRole("button", { name: "Pays with" }))
    fireEvent.click(await screen.findByRole("option", { name: /Anthropic production/ }))
    fireEvent.click(await screen.findByRole("button", { name: "g2-scraper" }))
    fireEvent.click(primary("Continue"))
    const summary = screen.getByTestId("new-agent-summary")
    expect(summary).toHaveTextContent("pays with Anthropic production · 1 skill")
    fireEvent.click(primary("Create agent"))

    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("fact-checker"))
    const calls = spy.mock.calls.map(([u, i]) => [String(u), i?.method ?? "GET", i?.body ? JSON.parse(String(i.body)) : null] as const)
    const create = calls.findIndex(([u, m]) => u.includes("/api/v1/agents?") && m === "POST")
    const bind = calls.findIndex(([u, m]) => u.includes("/credentials/bindings") && m === "POST")
    const skill = calls.findIndex(([u, m]) => u.includes("/agents/new-1/skills") && m === "POST")
    expect(create).toBeGreaterThan(-1)
    expect(bind).toBeGreaterThan(create)
    expect(calls[bind][2]).toMatchObject({ credential_id: "cred-1", scope: "AGENT", agent_id: "new-1" })
    expect(skill).toBeGreaterThan(create)
    expect(calls[skill][2]).toEqual({ skill_id: "sk1" })
    expect(toastWarning).not.toHaveBeenCalled()
  })

  it("a refused skill keeps the agent and says what was not assigned", async () => {
    stub((url, init) => (url.includes("/agents/new-1/skills") && init?.method === "POST" ? new Response("{}", { status: 403 }) : null))
    const props = renderWizard()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Fact checker" } })
    fireEvent.click(primary("Continue"))
    fireEvent.click(screen.getByRole("radio", { name: "Anthropic" }))
    fireEvent.click(primary("Continue"))
    fireEvent.click(await screen.findByRole("button", { name: "report-writer" }))
    fireEvent.click(primary("Continue"))
    fireEvent.click(primary("Create agent"))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("fact-checker"))
    expect(toastWarning).toHaveBeenCalledWith(expect.stringContaining("1 grant did not apply"), expect.objectContaining({ description: expect.stringContaining("skill report-writer") }))
  })

  it("copy an agent prefills model, limits and skills from GET /agents/{id}", async () => {
    stub()
    renderWizard()
    fireEvent.click(screen.getByRole("radio", { name: /Copy an agent/ }))
    const select = await screen.findByRole("combobox", { name: "Agent to copy" })
    await waitFor(() => expect(within(select).getByRole("option", { name: "Scout" })).toBeInTheDocument())
    fireEvent.change(select, { target: { value: "src" } })
    fireEvent.click(screen.getByRole("button", { name: "Step 2: Model" }))
    await waitFor(() => expect(screen.getByRole("radio", { name: "OpenAI" })).toHaveAttribute("aria-checked", "true"))
    fireEvent.click(screen.getByRole("button", { name: "Step 3: Access" }))
    await waitFor(() => expect(screen.getByRole("button", { name: "report-writer" })).toHaveAttribute("aria-pressed", "true"))
  })

  it("a MANAGER sees Pays with locked to administrators", async () => {
    await bootstrap("MANAGER")
    stub()
    renderWizard()
    fireEvent.click(screen.getByRole("button", { name: "Step 3: Access" }))
    expect(screen.getByText("Provider accounts are managed by workspace administrators.")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Pays with" })).not.toBeInTheDocument()
  })
})
