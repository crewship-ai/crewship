import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({ calls: [] as { url: string; body: Record<string, unknown> }[], appearanceFails: false }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("../routine-schedules-tab", () => ({ RoutineSchedulesTab: () => <div>Existing schedules</div> }))
vi.mock("../routine-webhooks-tab", () => ({ RoutineWebhooksTab: () => <div>Existing webhooks</div> }))
vi.mock("../routine-definition-canvas", () => ({ RoutineDefinitionCanvas: () => <div /> }))
vi.mock("@/components/features/files/file-editor", () => ({ FileEditor: () => <div /> }))
vi.mock("@/components/crew-icon-popover", () => ({ CrewIconPopover: (p: { icon: string; onIconChange: (s: string) => void }) => <button onClick={() => p.onIconChange("star")}>Icon: {p.icon}</button> }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("@/components/features/crews/crew-picker", () => ({ CrewPicker: (p: { value: string }) => <div data-testid="crew">{p.value}</div> }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async (url: string, init?: RequestInit) => {
  h.calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : {} })
  if (url.includes("/test_run")) return { ok: true, json: async () => ({ status: "DRY_RUN_OK", save_token: "verified" }) }
  if (url.endsWith("/save")) return { ok: true, json: async () => ({ slug: "existing" }) }
  if (url.endsWith("/appearance")) return { ok: !h.appearanceFails }
  if (url.startsWith("/api/v1/agents")) return { ok: true, json: async () => [{ id: "a1", slug: "worker", name: "Worker", crew_id: "crew1", agent_role: "AGENT" }] }
  return { ok: true, json: async () => [] }
}) }))
import { RoutineCreateDialog } from "../routine-create-dialog"

const routine = { id: "r1", slug: "existing", name: "Existing recipe", description: "Original description", icon: "clock", color: "blue", head_version: 3, author_crew_id: "crew1", author_agent_id: "a1", definition: { dsl_version: "1.0", name: "existing", description: "Original description", inputs: [], steps: [{ id: "work", type: "agent_run", agent_slug: "worker", prompt: "Do the work" }] } } as unknown as RoutineDetail
const props = { workspaceId: "ws1", open: true, routine, onCreated: vi.fn(), onClose: vi.fn() }
beforeEach(() => { cleanup(); h.calls = []; h.appearanceFails = false; vi.clearAllMocks() })
describe("shared routine editor", () => {
  it("prefills identity and real agents, then saves the same recipe without creating a schedule", async () => {
    render(<RoutineCreateDialog {...props} />)
    expect(screen.getByLabelText("Name")).toHaveValue("Existing recipe")
    expect(screen.getByTestId("crew")).toHaveTextContent("crew1")
    await screen.findByText("Worker")
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Edited recipe" } })
    fireEvent.click(screen.getByText("Icon: clock"))
    fireEvent.click(screen.getByRole("button", { name: "Schedule", exact: true }))
    expect(screen.getByText("Existing schedules")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Validate & Save" }))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("existing"))
    const saved = h.calls.find(c => c.url.endsWith("/save"))!.body
    expect(saved).toMatchObject({ slug: "existing", name: "Edited recipe", author_crew_id: "crew1", author_agent_id: "a1", save_token: "verified", skip_test_gate: false })
    expect(saved).not.toHaveProperty("trigger")
    expect(saved.definition).toEqual(routine.definition)
    expect(h.calls.find(c => c.url.endsWith("/appearance"))!.body).toEqual({ icon: "star", color: "blue" })
  })
  it("retries an appearance failure without saving the recipe or scheduling twice", async () => {
    h.appearanceFails = true
    render(<RoutineCreateDialog {...props} />)
    fireEvent.click(screen.getByRole("button", { name: "Validate & Save" }))
    await screen.findByText(/The recipe was saved, but its icon could not be saved/)
    expect(props.onCreated).not.toHaveBeenCalled()
    h.appearanceFails = false
    fireEvent.click(screen.getByRole("button", { name: "Validate & Save" }))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled())
    expect(h.calls.filter(c => c.url.endsWith("/save"))).toHaveLength(1)
  })
  it("opens a historical version as an unsaved draft", () => {
    render(<RoutineCreateDialog {...props} initialDraft={{ ...routine.definition, steps: [] }} />)
    fireEvent.click(screen.getByRole("button", { name: "Steps", exact: true }))
    expect(screen.queryByText("1. work")).not.toBeInTheDocument()
    expect(h.calls.some(c => c.url.endsWith("/save"))).toBe(false)
  })
})
