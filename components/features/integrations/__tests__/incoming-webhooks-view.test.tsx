import { describe, it, expect, vi, beforeEach } from "vitest"
import {
  render,
  screen,
  fireEvent,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { IncomingWebhooksView } from "../views/incoming-webhooks-view"
import {
  incomingRows,
  resolveIncomingTarget,
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
      targetId={targetSlug}
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
  it("keeps ID selection after rename and excludes endpoints with no crew", () => {
    const renamed = {...targets[1], slug: "renamed", crew_id: undefined}
    expect(resolveIncomingTarget([renamed], "agent", "a1")).toBe(renamed)
    expect(resolveIncomingTarget(targets, "agent", "pepa")).toBe(targets[1])
    expect(incomingRows([renamed], [])).toEqual([])
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
  it("presents load errors as errors and provides a retry", () => {
    const d = data()
    d.error = "backend unavailable"
    mount(view(d))
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(d.refresh).toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("backend unavailable")
  })
})
