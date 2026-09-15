import { describe, it, expect, vi, beforeEach } from "vitest"
import {
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { IncomingWebhooksView } from "../views/incoming-webhooks-view"
import { IncomingCreateDialog } from "../views/incoming-credentials"
import {
  incomingRows,
  incomingExplorer,
  type IncomingTarget,
} from "../incoming-model"
import type { IncomingData } from "../use-incoming-endpoints"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", async (original) => ({
  ...(await original<typeof import("@/lib/api-fetch")>()),
  apiFetch: vi.fn(),
}))
const targets: IncomingTarget[] = [
  { id: "r1", slug: "review-pr", name: "PR review", kind: "routine" },
  {
    id: "a1",
    slug: "pepa",
    name: "Pepa",
    kind: "agent",
    crew_id: "crew",
    webhook_secret_set: true,
  },
  { id: "p1", slug: "report", name: "Report", kind: "page" },
]
const hook: PipelineWebhook = {
  id: "h1",
  workspace_id: "ws",
  name: "GitHub PRs",
  target_pipeline_id: "r1",
  token: "",
  signing_secret_set: true,
  enabled: true,
  fire_count: 12,
  rate_limit_per_min: 20,
  created_at: "",
  updated_at: "",
  inputs_template: {},
  ingress_profile: "github",
}
function data(): IncomingData {
  return {
    targets,
    rows: incomingRows(targets, [hook]),
    loading: false,
    error: null,
    refresh: vi.fn(),
    hooks: {
      webhooks: [hook],
      loading: false,
      error: null,
      refresh: vi.fn(),
      create: vi.fn(),
      update: vi.fn(),
      remove: vi.fn(),
    },
  }
}
function mount(node: React.ReactNode) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      {node}
    </QueryClientProvider>,
  )
}
function view(
  d = data(),
  section: "endpoints" | "routine" | "agent" | "page" = "endpoints",
  targetSlug: string | null = null,
) {
  return (
    <IncomingWebhooksView
      workspaceId="ws"
      data={d}
      section={section}
      search=""
      targetSlug={targetSlug}
      onSelect={vi.fn()}
      onBack={vi.fn()}
      onAdd={vi.fn()}
    />
  )
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(apiFetch).mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), { status: 200 }),
  )
})

describe("incoming explorer model", () => {
  it("counts targets with endpoints once and does not invent Page totals", () => {
    const model = incomingExplorer(
      targets,
      incomingRows(targets, [hook, { ...hook, id: "h2" }]),
      "endpoints",
      "",
    )
    expect(model.sections.map((s) => [s.key, s.count])).toEqual([
      ["endpoints", 3],
      ["routine", 1],
      ["agent", 1],
      ["page", "per Page"],
    ])
    expect(model.items.find((i) => i.id === "agent:pepa")).toMatchObject({
      label: "Pepa",
      dot: "bg-success",
    })
    expect(incomingExplorer(targets, [], "routine", "report").items).toEqual([])
  })
})
describe("actual incoming surfaces", () => {
  it("renders KPI strip, cross-target endpoint table, and honest unknown 24h metric", () => {
    mount(view())
    expect(screen.getByText("Received · 24h")).toBeVisible()
    expect(
      screen.getByText("No aggregate count available from the API"),
    ).toBeVisible()
    expect(
      within(screen.getByRole("table")).getByRole("button", {
        name: "GitHub PRs",
      }),
    ).toBeVisible()
    expect(screen.getByText(/Page endpoints are read on demand/)).toBeVisible()
  })
  it("groups URL copy and secret rotation in the overview credentials column", () => {
    mount(view())
    const table = screen.getByRole("table")
    expect(within(table).getByRole("columnheader", { name: "Credentials" })).toBeVisible()
    const copy = within(table).getByRole("button", { name: "Copy receiving URL" })
    const cell = copy.closest("td")!
    expect(within(cell).getByRole("button", { name: "Rotate secret" })).toBeVisible()
    expect(cell.cellIndex).toBe(7)
    fireEvent.click(within(cell).getByRole("button", { name: "Rotate secret" }))
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Rotate secret for Pepa?")
    expect(apiFetch).not.toHaveBeenCalledWith(expect.stringContaining("webhook-secret/rotate"), expect.anything())
  })
  it("uses the shared detail vocabulary and calls the toggle mutation", () => {
    const d = data()
    mount(view(d, "routine", "review-pr"))
    expect(
      screen.getByRole("button", { name: "Back to endpoints" }),
    ).toBeVisible()
    expect(screen.queryByText("Schedule")).not.toBeInTheDocument()
    expect(
      screen.getByText("/api/v1/webhooks/•••••/github-pull-request"),
    ).toBeVisible()
    fireEvent.click(
      screen.getByRole("switch", { name: "Disable endpoint GitHub PRs" }),
    )
    expect(d.hooks.update).toHaveBeenCalledWith("h1", { enabled: false })
  })
  it("uses real Page endpoint markup with revoke, not the Page editor", async () => {
    vi.mocked(apiFetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          webhooks: [
            {
              id: "page-hook",
              panel: "result",
              name: "Result updates",
              live: true,
              fire_count: 4,
            },
          ],
        }),
        { status: 200 },
      ),
    )
    mount(view(data(), "page", "report"))
    expect(await screen.findByText("Result updates")).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Delete endpoint Result updates" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Mint" }),
    ).not.toBeInTheDocument()
    expect(screen.getByText("/api/v1/page-webhooks/•••••")).toBeVisible()
  })
  it("creates the default signed endpoint without opening advanced settings", async () => {
    const d = data()
    vi.mocked(d.hooks.create).mockResolvedValue({ ...hook, token: "once", signing_secret: "sign-once" })
    mount(<IncomingCreateDialog workspaceId="ws" data={d} initialTarget={targets[0]} onClose={vi.fn()} onCreated={vi.fn()} />)
    expect(screen.queryByRole("combobox", { name: "Webhook format" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Create endpoint" }))
    await waitFor(() => expect(d.hooks.create).toHaveBeenCalledWith(expect.objectContaining({ ingress_profile: "crewship", target_pipeline_id: "r1" })))
    expect(await screen.findByTestId("incoming-receiving-url")).toHaveTextContent("/api/v1/webhooks/once")
    expect(screen.getByText("sign-once")).toBeVisible()
  })
  it("creates a Page endpoint with an absolute receiving URL from the real form", async () => {
    vi.mocked(apiFetch).mockImplementation(async (_url, init) => new Response(JSON.stringify(
      init?.method === "POST"
        ? {id:"new-page-hook",url:"/api/v1/page-webhooks/one-time",live:true}
        : {id:"p1",slug:"report",name:"Report",panels:[{id:"result",schema:"status.v1",producer:"webhook/test"}]}
    ), {status: init?.method === "POST" ? 201 : 200}))
    mount(<IncomingCreateDialog workspaceId="ws" data={data()} initialTarget={targets[2]} onClose={vi.fn()} onCreated={vi.fn()}/>)
    const panel = await screen.findByRole("combobox", {name:"Panel"})
    fireEvent.keyDown(panel, {key:"ArrowDown"})
    fireEvent.click(screen.getByRole("option", {name:"result"}))
    fireEvent.click(screen.getByRole("button", {name:"Create endpoint"}))
    expect(await screen.findByTestId("incoming-receiving-url")).toHaveTextContent(`${window.location.origin}/api/v1/page-webhooks/one-time`)
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/pages/report/webhooks?workspace_id=ws", expect.objectContaining({method:"POST",body:JSON.stringify({panel:"result"})}))
  })
  it("presents load errors as errors and provides a retry", () => {
    const d = data()
    d.error = "backend unavailable"
    mount(view(d))
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(d.refresh).toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("backend unavailable")
  })
  it("creates a GitHub endpoint through the real form and reveals its URL once", async () => {
    const d = data()
    vi.mocked(d.hooks.create).mockResolvedValue({
      ...hook,
      token: "once",
      signing_secret: "sign-once",
    })
    const done = vi.fn()
    mount(
      <IncomingCreateDialog
        workspaceId="ws"
        data={d}
        initialTarget={targets[0]}
        onClose={vi.fn()}
        onCreated={done}
      />,
    )
    expect(screen.queryByRole("combobox", { name: "Webhook format" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Advanced settings" }))
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Webhook format" }), {
      key: "ArrowDown",
    })
    fireEvent.click(
      screen.getByRole("option", { name: "GitHub pull requests" }),
    )
    fireEvent.change(screen.getByLabelText("Endpoint name"), {
      target: { value: "PR events" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create endpoint" }))
    await waitFor(() =>
      expect(d.hooks.create).toHaveBeenCalledWith({
        name: "PR events",
        target_pipeline_id: "r1",
        ingress_profile: "github",
        enabled: true,
      }),
    )
    expect(
      await screen.findByTestId("incoming-receiving-url"),
    ).toHaveTextContent("/api/v1/webhooks/once/github-pull-request")
    expect(screen.getByText("sign-once")).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Done" }))
    expect(done).toHaveBeenCalledWith(targets[0])
  })
})
