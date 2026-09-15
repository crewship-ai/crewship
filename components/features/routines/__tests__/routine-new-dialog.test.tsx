// New routine: continue a draft by name, or one of three ways in.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RoutineNewDialog, describePrompt, leadOf, CLI_COMMANDS } from "../routine-new-dialog"
import type { Pipeline } from "@/hooks/use-pipelines"

const h = vi.hoisted(() => ({ fetcher: vi.fn(), push: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }), useSearchParams: () => new URLSearchParams(), usePathname: () => "/routines" }))

const routines = [
  { id: "p1", slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", head_version: 3, invocation_count: 4 },
  { id: "p2", slug: "contract-renewal-check", name: "Contract renewal check", head_version: 0, invocation_count: 0, draft: { id: "d", revision: 1, updated_at: "" } },
] as unknown as Pipeline[]
const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, clone() { return this } })

function mockServer() {
  h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/pipelines/drafts") && !init?.method) return json([{ slug: "contract-renewal-check", revision: 1, updated_at: "" }, { slug: "orphan-draft", revision: 2, updated_at: "" }])
    if (url.startsWith("/api/v1/crews?")) return json([{ id: "crew_fin", name: "Finance" }, { id: "crew_ops", name: "Ops" }])
    if (url.startsWith("/api/v1/agents?")) return json([{ id: "a1", name: "Nora", slug: "nora", agent_role: "WORKER", crew_id: "crew_fin" }, { id: "a2", name: "Lead", slug: "fin-lead", agent_role: "LEAD", crew_id: "crew_fin" }])
    if (url.endsWith("/pipelines/invoice-intake") && !init?.method) return json({ slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", definition: { name: "invoice-intake", steps: [] }, author_crew_id: "crew_fin" })
    if (url.endsWith("/pipelines/save")) return json({ slug: "invoice-intake-copy" })
    throw new Error(`unexpected ${init?.method ?? "GET"} ${url}`)
  })
}

beforeEach(() => {
  h.fetcher.mockReset()
  h.push.mockReset()
  mockServer()
})

describe("helpers", () => {
  it("picks the LEAD of a crew, else any agent in it", () => {
    const agents = [{ id: "1", name: "A", slug: "a", agent_role: "WORKER", crew_id: "c" }, { id: "2", name: "B", slug: "b", agent_role: "LEAD", crew_id: "c" }]
    expect(leadOf(agents, "c")?.slug).toBe("b")
    expect(leadOf(agents.slice(0, 1), "c")?.slug).toBe("a")
    expect(leadOf(agents, null)).toBeNull()
  })
  it("asks the lead for a draft, never a publish", () => {
    const prompt = describePrompt("Contract renewal check", "Find contracts ending soon")
    expect(prompt).toContain("save_routine_draft")
    expect(prompt).toContain("do not publish with save_routine")
    expect(prompt).toContain('Call it "Contract renewal check"')
    expect(prompt).toContain("Goal: Find contracts ending soon")
  })
  it("spells the CLI commands as the reference does", () => {
    const text = CLI_COMMANDS("x", "finance").map((c) => c.command).join("\n")
    expect(text).toContain("crewship routine get x -f yaml")
    expect(text).toContain("crewship routine init -o routine.json")
    expect(text).toContain("crewship crew files save finance shared/scripts/")
    expect(text).toContain("crewship routine validate x.yaml")
    expect(text).toContain("crewship routine draft save draft.json")
    expect(text).toContain("crewship routine draft publish saved.json")
    expect(text).not.toContain("--publish")
  })
})

describe("<RoutineNewDialog>", () => {
  it("lists drafts by routine name (slug when there is no routine) and opens one", async () => {
    const open = vi.fn()
    render(<RoutineNewDialog open onOpenChange={() => {}} workspaceId="ws" routines={routines} onOpenRoutine={open} />)
    const chips = await screen.findByTestId("routine-continue-drafts")
    expect(chips).toHaveTextContent("Contract renewal check · r1")
    expect(chips).toHaveTextContent("orphan-draft · r2")
    fireEvent.click(screen.getByRole("button", { name: "Contract renewal check · r1" }))
    expect(open).toHaveBeenCalledWith("contract-renewal-check")
    expect(screen.getByRole("button", { name: /Describe it/ })).toHaveTextContent("recommended")
    expect(screen.getByRole("button", { name: /Copy an existing routine/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Build it with the CLI/ })).toBeInTheDocument()
  })

  it("Describe it lays out like New issue and hands the goal to the crew lead's chat", async () => {
    const close = vi.fn()
    render(<RoutineNewDialog open onOpenChange={close} workspaceId="ws" routines={routines} onOpenRoutine={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /Describe it/ }))
    await waitFor(() => expect(screen.getByRole("button", { name: /Crew · Finance/ })).toBeInTheDocument())
    expect(screen.getByText("Schedule · not set")).toBeInTheDocument()
    expect(screen.getByText("Needs approval · choose")).toBeInTheDocument()
    const create = screen.getByRole("button", { name: "Create draft with the lead" })
    expect(create).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Routine name"), { target: { value: "Contract renewal check" } })
    fireEvent.change(screen.getByLabelText("What should happen"), { target: { value: "Find contracts ending in 60 days" } })
    fireEvent.click(create)
    expect(h.push).toHaveBeenCalledTimes(1)
    const url = String(h.push.mock.calls[0][0])
    expect(url.startsWith("/chat/fin-lead?prompt=")).toBe(true)
    expect(decodeURIComponent(url)).toContain("Find contracts ending in 60 days")
    expect(close).toHaveBeenCalledWith(false)
  })

  it("Copy reads the routine and saves it under a new name through the save path", async () => {
    const open = vi.fn()
    const created = vi.fn()
    render(<RoutineNewDialog open onOpenChange={() => {}} workspaceId="ws" routines={routines} onOpenRoutine={open} onCreated={created} />)
    fireEvent.click(screen.getByRole("button", { name: /Copy an existing routine/ }))
    fireEvent.click(screen.getByTestId("routine-copy-invoice-intake"))
    await waitFor(() => expect(open).toHaveBeenCalledWith("invoice-intake-copy"))
    const save = h.fetcher.mock.calls.find(([url]) => String(url).endsWith("/pipelines/save"))!
    const body = JSON.parse(String(save[1].body))
    expect(body).toMatchObject({ slug: "invoice-intake-copy", name: "Invoice intake (copy)", author_crew_id: "crew_fin", skip_test_gate: true })
    expect(body.skip_governance_gate).toBeUndefined()
    expect(created).toHaveBeenCalled()
  })

  it("Build it with the CLI shows the commands", () => {
    render(<RoutineNewDialog open onOpenChange={() => {}} workspaceId="ws" routines={routines} onOpenRoutine={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /Build it with the CLI/ }))
    expect(screen.getByText(/crewship routine draft publish saved.json/)).toBeInTheDocument()
    expect(screen.getByText(/crewship crew files save/)).toBeInTheDocument()
  })
})
