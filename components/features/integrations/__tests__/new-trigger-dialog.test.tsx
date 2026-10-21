import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
// usePage reads only for a signed-in human since main's #3572 (the query key
// carries the human, so private Page metadata cannot cross sessions).
vi.mock("@/hooks/use-auth", async (original) => ({
  ...(await original<typeof import("@/hooks/use-auth")>()),
  useSessionSafe: () => ({ data: { user: { id: "trigger-test-human" } }, status: "authenticated" }),
}))
vi.mock("@/lib/api-fetch", async (original) => ({
  ...(await original<typeof import("@/lib/api-fetch")>()),
  apiFetch: vi.fn(),
}))

import { NewTriggerDialog } from "../new-trigger-dialog"
import { incomingRows, type IncomingTarget } from "../incoming-model"
import type { IncomingData } from "../use-incoming-endpoints"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"
import { apiFetch } from "@/lib/api-fetch"

/**
 * New trigger (Harbor 2 v67 `inepnew`): three steps — Goes to, Sender and
 * mapping, URL. The URL and secret are shown once, so the last step only
 * closes through Done.
 */

const targets: IncomingTarget[] = [
  { id: "r1", slug: "review-pr", name: "PR review", kind: "routine", version: 4 },
  { id: "a1", slug: "scout", name: "Scout", kind: "agent", crew_id: "crew", webhook_secret_set: false },
  { id: "p1", slug: "report", name: "Report", kind: "page" },
]
const hook: PipelineWebhook = {
  id: "h1",
  workspace_id: "ws",
  name: "PR review webhook",
  target_pipeline_id: "r1",
  token: "once",
  signing_secret_set: true,
  signing_secret: "sign-once",
  enabled: true,
  fire_count: 0,
  rate_limit_per_min: 60,
  created_at: "",
  updated_at: "",
  inputs_template: {},
}
function data(): IncomingData {
  return {
    targets,
    rows: incomingRows(targets, []),
    loading: false,
    error: null,
    refresh: vi.fn(),
    hooks: { webhooks: [], loading: false, error: null, refresh: vi.fn(), create: vi.fn(async () => hook), update: vi.fn(), remove: vi.fn() },
  } as unknown as IncomingData
}
function mount(d: IncomingData, initialTarget?: IncomingTarget) {
  const onCreated = vi.fn()
  const onClose = vi.fn()
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <NewTriggerDialog workspaceId="ws" data={d} initialTarget={initialTarget} onClose={onClose} onCreated={onCreated} />
    </QueryClientProvider>,
  )
  return { onCreated, onClose }
}
function pick(combobox: string, option: string) {
  fireEvent.keyDown(screen.getByRole("combobox", { name: combobox }), { key: "ArrowDown" })
  fireEvent.click(screen.getByRole("option", { name: option }))
}

afterEach(() => {
  cleanup()
  vi.mocked(apiFetch).mockReset()
})

describe("NewTriggerDialog", () => {
  it("has the three v67 steps and only a webhook can start it", () => {
    mount(data())
    const steps = screen.getByRole("navigation", { name: "Steps" })
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["1Goes to", "2Sender and mapping", "3URL"])
    const startedBy = screen.getByRole("group", { name: "Started by" })
    expect(within(startedBy).getAllByRole("button").map((b) => b.textContent)).toEqual(["A webhook"])
    expect(screen.queryByText(/An app event|Email/)).not.toBeInTheDocument()
    expect(screen.getAllByRole("radio").map((r) => r.getAttribute("aria-label"))).toEqual(["Routine", "Agent", "Artifact"])
    expect(screen.getByText("Step 1 of 3")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  })

  it("routine: GitHub sender, mapping, limit and pin are sent in one create, then URL and secret once", async () => {
    const d = data()
    const { onCreated } = mount(d)
    pick("Routine", "PR review")
    const version = screen.getByRole("group", { name: "Version" })
    expect(within(version).getByRole("button", { name: "Pin v4" })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))

    expect(screen.getByText("Step 2 of 3")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("radio", { name: "GitHub" }))
    fireEvent.click(screen.getByRole("button", { name: "Add input" }))
    fireEvent.change(screen.getByLabelText("Input name 1"), { target: { value: "repo" } })
    fireEvent.change(screen.getByLabelText("Value of input 1"), { target: { value: "{{ inputs.event.repository.full_name }}" } })
    fireEvent.change(screen.getByLabelText("Calls per minute"), { target: { value: "30" } })
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "github-prs" } })
    fireEvent.click(screen.getByRole("button", { name: "Create" }))

    await waitFor(() =>
      expect(d.hooks.create).toHaveBeenCalledWith({
        name: "github-prs",
        target_pipeline_id: "r1",
        target_pipeline_version: 4,
        ingress_profile: "github",
        inputs_template: { repo: "{{ inputs.event.repository.full_name }}" },
        rate_limit_per_min: 30,
        enabled: true,
      }),
    )
    expect(await screen.findByTestId("incoming-receiving-url")).toHaveTextContent("/api/v1/webhooks/once/github-pull-request")
    expect(screen.getByText("sign-once")).toBeVisible()
    expect(screen.getByText("Step 3 of 3")).toBeInTheDocument()
    // The secret step cannot be closed by Esc before Done.
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
    expect(screen.getByTestId("incoming-receiving-url")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Back" })).not.toBeInTheDocument()
    expect(onCreated).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Done" }))
    expect(onCreated).toHaveBeenCalledWith(targets[0])
  })

  it("Always latest sends no pin and the default sender is Any system", async () => {
    const d = data()
    mount(d, targets[0])
    fireEvent.click(within(screen.getByRole("group", { name: "Version" })).getByRole("button", { name: "Always latest" }))
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    expect(screen.getByRole("radio", { name: "Any system" })).toHaveAttribute("aria-checked", "true")
    fireEvent.click(screen.getByRole("button", { name: "Create" }))
    await waitFor(() => expect(d.hooks.create).toHaveBeenCalled())
    const body = vi.mocked(d.hooks.create).mock.calls[0][0]
    expect(body).toEqual(expect.objectContaining({ ingress_profile: "crewship", name: "PR review webhook", rate_limit_per_min: 60 }))
    expect(body).not.toHaveProperty("target_pipeline_version")
    expect(body).not.toHaveProperty("inputs_template")
  })

  it("a mapping that would overwrite event, raw or headers cannot be created", () => {
    const d = data()
    mount(d, targets[0])
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    fireEvent.click(screen.getByRole("button", { name: "Add input" }))
    fireEvent.change(screen.getByLabelText("Input name 1"), { target: { value: "event" } })
    fireEvent.change(screen.getByLabelText("Value of input 1"), { target: { value: "x" } })
    expect(screen.getByText("event, raw and headers come from the call and cannot be mapped")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Create" })).toBeDisabled()
    expect(d.hooks.create).not.toHaveBeenCalled()
  })

  it("Back keeps what was chosen in step one", () => {
    mount(data(), targets[0])
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    fireEvent.click(screen.getByRole("button", { name: "Back" }))
    expect(screen.getByRole("radio", { name: "Routine" })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("combobox", { name: "Routine" })).toHaveTextContent("PR review")
  })

  it("agent: only Any system, no mapping; Create mints the agent's secret", async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ webhook_secret: "agent-secret" }), { status: 200 }))
    const d = data()
    mount(d, targets[1])
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    expect(screen.getAllByRole("radio").map((r) => r.getAttribute("aria-label"))).toEqual(["Any system"])
    expect(screen.queryByRole("button", { name: "Add input" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Create" }))
    expect(await screen.findByText("agent-secret")).toBeVisible()
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/agents/a1/webhook-secret/rotate?workspace_id=ws", { method: "POST" })
    expect(screen.getByTestId("incoming-receiving-url")).toHaveTextContent("/api/v1/webhooks/crew/a1/trigger")
  })

  it("artifact: the panel is chosen in step one, step two explains the token, Create returns an absolute URL", async () => {
    vi.mocked(apiFetch).mockImplementation(async (_url, init) =>
      new Response(
        JSON.stringify(
          init?.method === "POST"
            ? { id: "new-page-hook", url: "/api/v1/page-webhooks/one-time", live: true }
            : { id: "p1", slug: "report", name: "Report", panels: [{ id: "result", schema: "status.v1", producer: "webhook/test" }] },
        ),
        { status: init?.method === "POST" ? 201 : 200 },
      ),
    )
    mount(data(), targets[2])
    await screen.findByRole("combobox", { name: "Artifact panel" })
    pick("Artifact panel", "result")
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    expect(screen.getByText(/Artifact webhooks use a token in the URL/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Create" }))
    expect(await screen.findByTestId("incoming-receiving-url")).toHaveTextContent(`${window.location.origin}/api/v1/page-webhooks/one-time`)
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/pages/report/webhooks?workspace_id=ws", expect.objectContaining({ method: "POST", body: JSON.stringify({ panel: "result" }) }))
  })
})
