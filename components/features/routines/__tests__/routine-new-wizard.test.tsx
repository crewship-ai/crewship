// New routine — the five-step wizard (Basics · When · Steps · Limits · Test
// and save). Every request it makes is one the server already has; these
// tests pin the bodies, because a wrong field is dropped silently.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, within, cleanup } from "@testing-library/react"

import { RoutineNewWizard } from "../routine-new-wizard"
import type { Pipeline } from "@/hooks/use-pipelines"

const h = vi.hoisted(() => ({
  fetcher: vi.fn(),
  push: vi.fn(),
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
  taken: new Set<string>(),
  takenDraft: new Set<string>(),
  saveStatus: 201,
  saveBody: { slug: "weekly-report", status: "active" } as Record<string, unknown>,
  testStatus: "DRY_RUN_OK",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }), useSearchParams: () => new URLSearchParams(), usePathname: () => "/routines" }))

const routines = [
  { id: "p1", slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", head_version: 3, invocation_count: 4, author_crew_id: "crew_res" },
] as unknown as Pipeline[]

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, text: async () => JSON.stringify(body), clone() { return this } })
const calls = (suffix: string, method = "POST") =>
  h.fetcher.mock.calls.filter(([url, init]) => String(url).split("?")[0].endsWith(suffix) && (init?.method ?? "GET") === method)
const sent = (suffix: string) => JSON.parse(String(calls(suffix).at(-1)![1].body))

function mockServer() {
  h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET"
    const path = url.split("?")[0]
    if (path === "/api/v1/me/preferences" || path.startsWith("/api/v1/me/preferences/")) return json({})
    if (path === "/api/v1/crews") return json([{ id: "crew_res", name: "Research" }, { id: "crew_ops", name: "Ops" }, { id: "crew_empty", name: "Empty" }])
    if (path === "/api/v1/agents")
      return json([
        { id: "a1", name: "Scout", slug: "scout", agent_role: "WORKER", crew_id: "crew_res" },
        { id: "a2", name: "Writer", slug: "writer", agent_role: "LEAD", crew_id: "crew_res" },
        { id: "a3", name: "Opsy", slug: "opsy", agent_role: "LEAD", crew_id: "crew_ops" },
      ])
    if (path.endsWith("/pipeline-schedules/preview")) {
      const q = new URL(url, "http://x").searchParams
      if (q.get("phrase") === "whenever the moon is full") return json({ error: "could not understand that schedule" }, 400)
      if (q.get("phrase")) return json({ cron_expr: "30 8 * * 1-5", phrase: q.get("phrase"), timezone: "Europe/Prague", occurrences: ["2026-10-22T06:30:00Z"] })
      return json({ cron_expr: new URL(url, "http://x").searchParams.get("cron_expr"), timezone: "Europe/Prague", occurrences: ["2026-10-22T06:00:00Z", "2026-10-29T07:00:00Z", "2026-11-05T07:00:00Z"] })
    }
    if (path.endsWith("/pipelines/test_run")) {
      if (h.testStatus === "422") return json({ error: "1 validation error:\n  - /steps/0/agent_slug: pipeline: step \"task1\" agent_slug required" }, 422)
      return json({ status: h.testStatus, would_execute: [{ step_id: "task1", step_type: "agent_run", would_call_agent: "scout" }], save_token: h.testStatus === "DRY_RUN_OK" ? "tok-1" : undefined })
    }
    if (path.endsWith("/pipelines/save")) return json(h.saveBody, h.saveStatus)
    if (path.endsWith("/pipelines/drafts") && method === "POST") return json({ ...JSON.parse(String(init!.body)), id: "drf_1", revision: 1 })
    if (path.endsWith("/appearance")) return json({})
    if (path.endsWith("/pipelines/invoice-intake") && method === "GET")
      return json({ slug: "invoice-intake", name: "Invoice intake", head_version: 3, author_crew_id: "crew_res", definition: { name: "invoice-intake", max_cost_usd: 1, steps: [{ id: "read", type: "agent_run", agent_slug: "scout", prompt: "Read", complexity: "fast", on_fail: "abort" }] } })
    const slugMatch = /\/pipelines\/([^/]+)(\/draft)?$/.exec(path)
    if (slugMatch && method === "GET") {
      const [, slug, draft] = slugMatch
      if (draft) return h.takenDraft.has(slug) ? json({ id: "drf_x", slug, revision: 2, document: { name: "Their draft" } }) : json({ id: "", slug, revision: 0, document: {} })
      return h.taken.has(slug) ? json({ slug, name: "Existing routine" }) : json({ error: "not found" }, 404)
    }
    throw new Error(`unexpected ${method} ${url}`)
  })
}

beforeEach(() => {
  h.fetcher.mockReset()
  h.push.mockReset()
  Object.values(h.toast).forEach((f) => f.mockReset())
  h.taken = new Set()
  h.takenDraft = new Set()
  h.saveStatus = 201
  h.saveBody = { slug: "weekly-report", status: "active", trigger: { kind: "manual" } }
  h.testStatus = "DRY_RUN_OK"
  window.localStorage.clear()
  mockServer()
})
afterEach(cleanup)

function renderWizard(over: Partial<React.ComponentProps<typeof RoutineNewWizard>> = {}) {
  const props = { open: true, onOpenChange: vi.fn(), workspaceId: "ws", routines, onOpenRoutine: vi.fn(), onCreated: vi.fn(), onImport: vi.fn(), ...over }
  render(<RoutineNewWizard {...props} />)
  return props
}
const primary = (name: RegExp | string) => screen.getByRole("button", { name })
const goOn = () => fireEvent.click(primary("Continue"))

async function basics(name = "Weekly report") {
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: name } })
  await screen.findByText("Free")
  fireEvent.click(await screen.findByRole("radio", { name: /^Research/ }))
  goOn()
}
async function manualWhen() {
  fireEvent.click(screen.getByRole("radio", { name: /^Manually/ }))
  goOn()
}
async function oneAgentStep() {
  const agent = await screen.findByRole("radiogroup", { name: "Agent" })
  fireEvent.click(within(agent).getByRole("radio", { name: "Scout" }))
  fireEvent.change(screen.getByLabelText("Instructions"), { target: { value: "List the price changes." } })
  goOn()
}

describe("<RoutineNewWizard> step 1 · Basics", () => {
  it("derives a slug, rejects bad characters and reports Taken when GET slug or GET draft returns 200", async () => {
    h.taken.add("weekly-report")
    h.takenDraft.add("daily-digest")
    renderWizard()
    expect(screen.getByRole("navigation", { name: "Steps" })).toHaveTextContent("Basics")
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Weekly Report!" } })
    expect(screen.getByLabelText("ID")).toHaveValue("weekly-report")
    expect(await screen.findByText("Taken by Existing routine")).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Daily digest" } })
    expect(await screen.findByText("Taken by a draft")).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("ID"), { target: { value: "Bad ID" } })
    expect(screen.getByText(/Lowercase letters, digits, - and _/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("ID"), { target: { value: "fresh-one" } })
    expect(await screen.findByText("Free")).toBeInTheDocument()
    // Renaming no longer moves an ID the person typed.
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Something else" } })
    expect(screen.getByLabelText("ID")).toHaveValue("fresh-one")
  })

  it("crew is required and never defaults to the first crew silently", async () => {
    renderWizard()
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Weekly report" } })
    await screen.findByText("Free")
    const crews = await screen.findByRole("radiogroup", { name: "Crew" })
    for (const radio of within(crews).getAllByRole("radio")) expect(radio).toHaveAttribute("aria-checked", "false")
    expect(primary("Continue")).toBeDisabled()
    // PAR-147: only crews with an agent are offered, as inline chips; one that
    // cannot run a step is not a choice, and a line says why it is missing.
    expect(within(crews).queryByRole("radio", { name: /Empty/ })).toBeNull()
    expect(within(crews).getAllByRole("radio")).toHaveLength(2)
    expect(within(crews).getAllByRole("radio")[0].className).toMatch(/rounded-full/)
    expect(screen.getByText(/1 crew has no agent yet/)).toBeInTheDocument()
    fireEvent.click(within(crews).getByRole("radio", { name: /^Research/ }))
    expect(primary("Continue")).toBeEnabled()
  })

  it("PAR-147: the ID field shows the ID the name would give, before anything is typed", async () => {
    renderWizard()
    expect(screen.getByLabelText("ID")).toHaveAttribute("placeholder", "competitor-price-watch")
  })

  it("Describe it hands the person's words to the crew lead's chat", async () => {
    const props = renderWizard()
    fireEvent.click(screen.getByRole("radio", { name: /Describe it/ }))
    fireEvent.click(await screen.findByRole("radio", { name: /^Research/ }))
    fireEvent.change(screen.getByLabelText("What should happen, and when?"), { target: { value: "Every Thursday compare competitor prices" } })
    fireEvent.click(primary("Ask Writer to draft it"))
    const url = decodeURIComponent(String(h.push.mock.calls[0][0]))
    expect(url.startsWith("/chat/writer?prompt=")).toBe(true)
    expect(url).toContain("Every Thursday compare competitor prices")
    expect(url).not.toContain("save_routine_draft")
    expect(props.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("Copy a routine fills the steps and limits, not the triggers, and opens Steps", async () => {
    renderWizard()
    fireEvent.click(screen.getByRole("radio", { name: /Copy a routine/ }))
    fireEvent.click(await screen.findByTestId("routine-copy-invoice-intake"))
    expect(await screen.findByText(/Copied from/)).toHaveTextContent("Triggers and history are not copied")
    expect(screen.getByRole("navigation", { name: "Steps" }).querySelector('[aria-current="step"]')).toHaveTextContent("Steps")
    expect(screen.getByRole("button", { name: /Step 1: read/ })).toBeInTheDocument()
    expect(within(screen.getByRole("complementary", { name: "Summary" })).getByText("Invoice intake (copy)")).toBeInTheDocument()
  })

  it("Import and the command line are offered as other ways in", async () => {
    const props = renderWizard()
    fireEvent.click(screen.getByRole("button", { name: "Import a routine" }))
    expect(props.onImport).toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: /Build it on the command line/ }))
    expect(screen.getByText(/crewship routine save --name/)).toBeInTheDocument()
  })
})

describe("<RoutineNewWizard> step 2 · When", () => {
  it("schedule presets produce the expected cron and fetch 3 next starts with the timezone", async () => {
    renderWizard()
    await basics()
    fireEvent.click(screen.getByRole("radio", { name: /^On a schedule/ }))
    fireEvent.click(screen.getByRole("radio", { name: "Weekdays" }))
    fireEvent.change(screen.getByLabelText("At"), { target: { value: "09:30" } })
    fireEvent.change(screen.getByLabelText("Time zone"), { target: { value: "Europe/Prague" } })
    await waitFor(() => expect(screen.getByTestId("routine-cron")).toHaveTextContent("30 9 * * 1-5"))
    await waitFor(() => {
      const last = calls("/pipeline-schedules/preview", "GET").at(-1)
      expect(last).toBeTruthy()
      const q = new URL(String(last![0]), "http://x").searchParams
      expect(q.get("cron_expr")).toBe("30 9 * * 1-5")
      expect(q.get("timezone")).toBe("Europe/Prague")
      expect(q.get("count")).toBe("3")
    })
    expect(await screen.findAllByTestId("routine-next-start")).toHaveLength(3)
    fireEvent.click(screen.getByRole("radio", { name: "Weekly" }))
    fireEvent.click(screen.getByRole("radio", { name: "Mo" }))
    await waitFor(() => expect(screen.getByTestId("routine-cron")).toHaveTextContent("30 9 * * 1"))
  })

  it("Custom takes the schedule in words: the server turns it into a cron (ODCH-RT-15)", async () => {
    renderWizard()
    await basics()
    fireEvent.click(screen.getByRole("radio", { name: /^On a schedule/ }))
    fireEvent.click(screen.getByRole("radio", { name: "Custom (cron)" }))
    fireEvent.change(screen.getByLabelText("Or say it in words"), { target: { value: "every weekday at 8:30" } })
    fireEvent.click(screen.getByRole("button", { name: "Use these words" }))
    await waitFor(() => expect(screen.getByTestId("routine-cron")).toHaveTextContent("30 8 * * 1-5"))
    const asked = calls("/pipeline-schedules/preview", "GET").find(([url]) => String(url).includes("phrase="))!
    expect(new URL(String(asked[0]), "http://x").searchParams.get("phrase")).toBe("every weekday at 8:30")
    expect(screen.getByLabelText("Cron expression")).toHaveValue("30 8 * * 1-5")
  })

  it("says so, in the server's words, when the words are not a schedule", async () => {
    renderWizard()
    await basics()
    fireEvent.click(screen.getByRole("radio", { name: /^On a schedule/ }))
    fireEvent.click(screen.getByRole("radio", { name: "Custom (cron)" }))
    fireEvent.change(screen.getByLabelText("Or say it in words"), { target: { value: "whenever the moon is full" } })
    fireEvent.click(screen.getByRole("button", { name: "Use these words" }))
    expect(await screen.findByText("could not understand that schedule")).toBeInTheDocument()
  })

  it("a one-time start must be in the future", async () => {
    renderWizard()
    await basics()
    fireEvent.click(screen.getByRole("radio", { name: /^Once/ }))
    fireEvent.change(screen.getByLabelText("Date and time"), { target: { value: "2020-01-01T08:00" } })
    expect(screen.getByText("Pick a time in the future")).toBeInTheDocument()
    expect(primary("Continue")).toBeDisabled()
  })
})

describe("<RoutineNewWizard> step 3 · Steps", () => {
  it("insert offers only inputs and earlier steps", async () => {
    renderWizard()
    await basics()
    await manualWhen()
    const agent = await screen.findByRole("radiogroup", { name: "Agent" })
    fireEvent.click(within(agent).getByRole("radio", { name: "Scout" }))
    fireEvent.change(screen.getByLabelText("Instructions"), { target: { value: "Collect" } })
    // The first step has nothing above it to insert.
    expect(screen.queryByRole("button", { name: /\{\{ steps\./ })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Done" }))
    fireEvent.click(screen.getByRole("button", { name: "Add a step" }))
    fireEvent.click(screen.getByRole("button", { name: /Notify people/ }))
    fireEvent.click(screen.getByRole("button", { name: "Insert {{ steps.task1.output }}" }))
    expect(screen.getByLabelText("Message")).toHaveValue("{{ steps.task1.output }}")
    expect(screen.queryByRole("button", { name: /steps\.notify1/ })).not.toBeInTheDocument()
  })

  it("a web call says it needs a Manager review", async () => {
    renderWizard()
    await basics()
    await manualWhen()
    fireEvent.click(screen.getByRole("button", { name: "Add a step" }))
    fireEvent.click(screen.getByRole("button", { name: /Call a web API/ }))
    expect(screen.getAllByText(/Manager review/).length).toBeGreaterThan(0)
  })
})

describe("<RoutineNewWizard> step 4 · Limits", () => {
  it("Skip the new start sets concurrency_policy skip with one run at a time (ODCH-RT-25)", async () => {
    renderWizard()
    await basics()
    await manualWhen()
    await oneAgentStep()
    fireEvent.click(screen.getByRole("radio", { name: "Skip the new start" }))
    expect(screen.getByText(/a start that finds it running is dropped/)).toBeInTheDocument()
    goOn()
    fireEvent.click(screen.getByRole("button", { name: "Run the test" }))
    await screen.findByText("Test passed")
    expect(sent("/pipelines/test_run").definition).toMatchObject({ max_concurrent: 1, concurrency_key: "weekly-report", concurrency_policy: "skip" })
  })

  it("side by side sets max_concurrent and no policy", async () => {
    renderWizard()
    await basics()
    await manualWhen()
    await oneAgentStep()
    fireEvent.click(screen.getByRole("radio", { name: "Run side by side" }))
    fireEvent.change(screen.getByLabelText("Runs at the same time"), { target: { value: "4" } })
    fireEvent.change(screen.getByLabelText("Cost limit per run"), { target: { value: "2" } })
    goOn()
    fireEvent.click(screen.getByRole("button", { name: "Run the test" }))
    await screen.findByText("Test passed")
    const def = sent("/pipelines/test_run").definition
    expect(def).toMatchObject({ max_concurrent: 4, concurrency_key: "weekly-report", max_cost_usd: 2 })
    expect(def).not.toHaveProperty("concurrency_policy")
  })
})

describe("<RoutineNewWizard> step 5 · Test and save", () => {
  it("test_run → save sends save_token, trigger and author_crew_id", async () => {
    const props = renderWizard()
    await basics()
    // Schedule: weekly on Thursday at 08:00, the default zone typed in.
    fireEvent.click(screen.getByRole("radio", { name: /^On a schedule/ }))
    fireEvent.click(screen.getByRole("radio", { name: "Weekly" }))
    fireEvent.click(screen.getByRole("radio", { name: "Th" }))
    fireEvent.change(screen.getByLabelText("Time zone"), { target: { value: "Europe/Prague" } })
    await screen.findAllByTestId("routine-next-start")
    goOn()
    await oneAgentStep()
    goOn()
    expect(primary("Save and turn on")).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Run the test" }))
    await screen.findByText("Test passed")
    expect(screen.getByText(/to save this version/)).toBeInTheDocument()
    const test = sent("/pipelines/test_run")
    expect(test).toMatchObject({ author_crew_id: "crew_res", sample_inputs: {} })
    h.saveBody = { slug: "weekly-report", status: "active", trigger: { kind: "schedule", first_fire_at: "2026-10-22T06:00:00Z" } }
    fireEvent.click(primary("Save and turn on"))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled())
    const body = sent("/pipelines/save")
    expect(body).toMatchObject({
      slug: "weekly-report",
      name: "Weekly report",
      author_crew_id: "crew_res",
      save_token: "tok-1",
      change_summary: "initial version",
      trigger: { kind: "schedule", cron: "0 8 * * 4", timezone: "Europe/Prague", catchup_policy: "once", max_consecutive_failures: 5 },
    })
    // Exactly what was tested — the token is bound to the definition.
    expect(body.definition).toEqual(test.definition)
    expect(body).not.toHaveProperty("skip_governance_gate")
    expect(body).not.toHaveProperty("skip_test_gate")
    expect(h.toast.success).toHaveBeenCalledWith(expect.stringMatching(/^Weekly report is live · first start /), expect.objectContaining({ action: expect.objectContaining({ label: "Open" }) }))
    expect(props.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("a save refused because the test is older than 5 minutes asks to test again", async () => {
    renderWizard()
    await basics()
    await manualWhen()
    await oneAgentStep()
    goOn()
    fireEvent.click(screen.getByRole("button", { name: "Run the test" }))
    await screen.findByText("Test passed")
    h.saveStatus = 422
    h.saveBody = { error: "save requires a fresh, passing test_run within 5 minutes" }
    fireEvent.click(primary("Save and turn on"))
    expect(await screen.findByText(/test is no longer valid/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Test again" })).toBeInTheDocument()
    expect(primary("Save and turn on")).toBeDisabled()
  })

  it("validation errors point at the step", async () => {
    h.testStatus = "422"
    renderWizard()
    await basics()
    await manualWhen()
    await oneAgentStep()
    goOn()
    fireEvent.click(screen.getByRole("button", { name: "Run the test" }))
    expect(await screen.findByText(/agent_slug required/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Go to step 1" }))
    expect(screen.getByRole("navigation", { name: "Steps" }).querySelector('[aria-current="step"]')).toHaveTextContent("Steps")
  })

  it("a risky definition shows Save and send for review and lands as proposed", async () => {
    renderWizard()
    await basics()
    await manualWhen()
    // The first step opens ready to fill in; this routine only calls out.
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    fireEvent.click(screen.getByRole("button", { name: "Add a step" }))
    fireEvent.click(screen.getByRole("button", { name: /Call a web API/ }))
    fireEvent.change(screen.getByLabelText("URL"), { target: { value: "https://tracker.example.com/api" } })
    goOn()
    expect(screen.getByText(/Manager approves it in Inbox/)).toBeInTheDocument()
    goOn()
    fireEvent.click(screen.getByRole("button", { name: "Run the test" }))
    await screen.findByText("Test passed")
    h.saveBody = { slug: "weekly-report", status: "proposed" }
    fireEvent.click(primary("Save and send for review"))
    await waitFor(() => expect(h.toast.success).toHaveBeenCalledWith("Weekly report is waiting for a Manager review", expect.anything()))
    expect(sent("/pipelines/save").definition.egress_targets).toEqual(["tracker.example.com"])
  })

  it("once is refused for Save as draft with a reason", async () => {
    renderWizard()
    await basics()
    fireEvent.click(screen.getByRole("radio", { name: /^Once/ }))
    const future = new Date(Date.now() + 3 * 86_400_000)
    const local = new Date(future.getTime() - future.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
    fireEvent.change(screen.getByLabelText("Date and time"), { target: { value: local } })
    goOn()
    await oneAgentStep()
    goOn()
    expect(screen.getByRole("button", { name: "Save as draft" })).toBeDisabled()
    expect(screen.getByText(/A one-time start needs a live routine/)).toBeInTheDocument()
  })

  it("Save as draft posts to /pipelines/drafts and opens the draft page", async () => {
    const props = renderWizard()
    await basics()
    await manualWhen()
    await oneAgentStep()
    goOn()
    fireEvent.click(screen.getByRole("button", { name: "Save as draft" }))
    await waitFor(() => expect(props.onOpenRoutine).toHaveBeenCalledWith("weekly-report"))
    const body = sent("/pipelines/drafts")
    expect(body).toMatchObject({ id: "", slug: "weekly-report", revision: 0, base_pipeline_id: "", base_revision: 0 })
    expect(body.document).toMatchObject({ slug: "weekly-report", name: "Weekly report", author_crew_id: "crew_res", definition: { name: "weekly-report" } })
    expect(calls("/pipelines/save")).toHaveLength(0)
  })
})

describe("<RoutineNewWizard> keyboard", () => {
  it("⌘↵ advances; Esc with input asks before discarding", async () => {
    const props = renderWizard()
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Weekly report" } })
    await screen.findByText("Free")
    fireEvent.click(await screen.findByRole("radio", { name: /^Research/ }))
    fireEvent.keyDown(screen.getByLabelText("Name"), { key: "Enter", metaKey: true })
    expect(screen.getByRole("navigation", { name: "Steps" }).querySelector('[aria-current="step"]')).toHaveTextContent("When")
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
    expect(await screen.findByText("Discard this routine?")).toBeInTheDocument()
    expect(props.onOpenChange).not.toHaveBeenCalledWith(false)
  })
})

describe("COM-15: phone sheet height", () => {
  it("fills the phone viewport through the kit's phoneHeight, not a local class", () => {
    renderWizard()
    const dialog = screen.getByRole("dialog")
    expect(dialog.className).toContain("max-sm:h-dvh")
    expect(dialog.className).not.toContain("max-sm:h-[100dvh]")
  })
})

