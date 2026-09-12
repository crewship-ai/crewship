import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({
  calls: [] as { url: string; body: Record<string, unknown> }[],
  appearanceFails: false,
  discardFails: false,
  draftConflict: false,
  scheduleConflict: false,
  linked: false,
  linkedExisting: false,
  baselineFails: false,
  savedDrafts: false,
  draftReads: new Map<
    string,
    { resolve: (response: Response) => void; signal?: AbortSignal | null }
  >(),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("../routine-schedules-tab", () => ({
  RoutineSchedulesTab: () => <div>Existing schedules</div>,
}))
vi.mock("../routine-webhooks-tab", () => ({
  RoutineWebhooksTab: () => <div>Existing webhooks</div>,
}))
vi.mock("../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div />,
}))
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: () => <div />,
}))
vi.mock("@/components/crew-icon-popover", () => ({
  CrewIconPopover: (p: { icon: string; onIconChange: (s: string) => void }) => (
    <button onClick={() => p.onIconChange("star")}>Icon: {p.icon}</button>
  ),
}))
vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: () => <span />,
}))
vi.mock("@/components/features/crews/crew-picker", () => ({
  CrewPicker: (p: { value: string }) => <div data-testid="crew">{p.value}</div>,
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string, init?: RequestInit) => {
    h.calls.push({
      url,
      body: init?.body ? JSON.parse(String(init.body)) : {},
    })
    if (init?.method === "DELETE" && h.discardFails)
      return new Response("<html>Bad gateway</html>", { status: 502 })
    if (h.savedDrafts && url.endsWith("/drafts") && !init?.method)
      return new Response(
        JSON.stringify([
          { slug: "first", revision: 1 },
          { slug: "second", revision: 2 },
        ]),
      )
    if (h.savedDrafts && url.endsWith("/draft"))
      return new Promise<Response>((resolve) => {
        h.draftReads.set(url.split("/").at(-2)!, {
          resolve,
          signal: init?.signal,
        })
      })
    if (url.endsWith("/pipelines/existing") && !init?.method)
      return new Response(JSON.stringify(h.baselineFails ? {error:"unavailable"} : routine), {status: h.baselineFails ? 503 : 200})
    if (url.endsWith("/draft") && h.linked)
      return {
        ok: true,
        json: async () => ({
          id: "ai-draft-1",
          slug: "existing",
          revision: 4,
          base_pipeline_id: h.linkedExisting ? "pipeline-existing" : "",
          base_revision: 0,
          document: {
            slug: "existing",
            name: "Draft from Chat",
            author_crew_id: "crew1",
            definition: routine.definition,
          },
        }),
      }
    if (url.endsWith("/draft"))
      return {
        ok: true,
        json: async () => ({
          id: "",
          slug: url.split("/").at(-2),
          revision: 0,
          base_pipeline_id: "",
          base_revision: 0,
          document: {},
        }),
      }
    if (url.endsWith("/drafts") && init?.method === "POST" && h.draftConflict)
      return {
        ok: false,
        status: 409,
        json: async () => ({
          error: "Another editor saved this draft. Reload and review.",
        }),
      }
    if (url.endsWith("/drafts") && init?.method === "POST")
      return {
        ok: true,
        json: async () => ({
          ...JSON.parse(String(init.body)),
          id: "draft-1",
          revision: 1,
        }),
      }
    if (url.includes("/test_run"))
      return {
        ok: true,
        json: async () => ({ status: "DRY_RUN_OK", save_token: "verified" }),
      }
    if (url.endsWith("/publish") && h.scheduleConflict)
      return {
        ok: false,
        status: 409,
        json: async () => ({
          error: "Publication blocked by Daily",
          schedule_conflict: {
            schedule_id: "plan1",
            name: "Daily",
            reason: "Input region is required",
          },
        }),
      }
    if (url.endsWith("/publish"))
      return { ok: true, json: async () => ({ slug: "existing" }) }
    if (url.endsWith("/appearance")) return { ok: !h.appearanceFails }
    if (url.startsWith("/api/v1/agents"))
      return {
        ok: true,
        json: async () => [
          {
            id: "a1",
            slug: "worker",
            name: "Worker",
            crew_id: "crew1",
            agent_role: "AGENT",
          },
        ],
      }
    return { ok: true, json: async () => [] }
  }),
}))
import { RoutineCreateDialog } from "../routine-create-dialog"

const routine = {
  id: "r1",
  slug: "existing",
  name: "Existing recipe",
  description: "Original description",
  icon: "clock",
  color: "blue",
  head_version: 3,
  author_crew_id: "crew1",
  author_agent_id: "a1",
  definition: {
    dsl_version: "1.0",
    name: "existing",
    description: "Original description",
    inputs: [],
    steps: [
      {
        id: "work",
        type: "agent_run",
        agent_slug: "worker",
        prompt: "Do the work",
      },
    ],
  },
} as unknown as RoutineDetail
const props = {
  workspaceId: "ws1",
  open: true,
  routine,
  onCreated: vi.fn(),
  onClose: vi.fn(),
}
beforeEach(() => {
  cleanup()
  h.calls = []
  h.appearanceFails = false
  h.discardFails = false
  h.draftConflict = false
  h.scheduleConflict = false
  h.linked = false
  h.linkedExisting = false
  h.baselineFails = false
  h.savedDrafts = false
  h.draftReads.clear()
  vi.clearAllMocks()
})
describe("shared routine editor", () => {
  it("will not publish an existing linked draft without a readable comparison baseline", async () => {
    h.linked = true; h.linkedExisting = true; h.baselineFails = true
    render(<RoutineCreateDialog {...props} routine={undefined} savedDraftLink={{slug:"existing", id:"ai-draft-1", workspaceId:"ws1"}} />)
    await waitFor(() => expect(screen.getByLabelText("Name")).toHaveValue("Draft from Chat"))
    fireEvent.click(screen.getByRole("button", {name:"Publish"}))
    await screen.findByText(/The published recipe could not be loaded for comparison/)
    expect(screen.getByRole("button", {name:"Confirm and publish"})).toBeDisabled()
    expect(h.calls.some(c => c.url.endsWith("/publish"))).toBe(false)
    fireEvent.click(screen.getByRole("button", {name:"Back to editing"}))
    expect(screen.getByLabelText("Name")).toHaveValue("Draft from Chat")
    h.baselineFails = false
    fireEvent.click(screen.getByRole("button", {name:"Publish"}))
    await waitFor(() => expect(screen.getByRole("button", {name:"Confirm and publish"})).toBeEnabled())
    expect(h.calls.some(c => c.url.endsWith("/publish"))).toBe(false)
  })

  it("requires a change review and confirmation, and lets the user return to editing", async () => {
    render(<RoutineCreateDialog {...props} />)
    await screen.findByText("Worker")
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Reviewed name" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    expect(
      screen.getByRole("heading", { name: "Confirm publication" }),
    ).toBeInTheDocument()
    expect(
      h.calls.some((c) => c.url.endsWith("/publish") || c.url.endsWith("/test_run")),
    ).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Back to editing" }))
    expect(screen.getByLabelText("Name")).toHaveValue("Reviewed name")
    expect(props.onClose).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    fireEvent.click(screen.getByRole("button", { name: "Confirm and publish" }))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled())
  })

  it("keeps one recipe document and preserves edits when opening Code", async () => {
    render(<RoutineCreateDialog {...props} />)
    await waitFor(() =>
      expect(screen.queryByText("Loading saved draft…")).not.toBeInTheDocument(),
    )
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Draft name" },
    })
    expect(screen.queryByText("Existing schedules")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Continue", exact: true }),
    ).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Code", exact: true }))
    fireEvent.click(screen.getByRole("button", { name: "Back to recipe" }))
    expect(screen.getByLabelText("Name")).toHaveValue("Draft name")
    expect(
      h.calls.some((c) => c.url.endsWith("/publish") || c.url.includes("/test_run")),
    ).toBe(false)
  })
  it("prefills identity and real agents, then saves the same recipe without creating a schedule", async () => {
    render(<RoutineCreateDialog {...props} />)
    expect(screen.getByLabelText("Name")).toHaveValue("Existing recipe")
    expect(screen.getByTestId("crew")).toHaveTextContent("crew1")
    await screen.findByText("Worker")
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Edited recipe" },
    })
    fireEvent.click(screen.getByText("Icon: clock"))
    expect(screen.queryByText("Existing schedules")).not.toBeInTheDocument()
    expect(screen.queryByText("Existing webhooks")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    fireEvent.click(screen.getByRole("button", { name: "Confirm and publish" }))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("existing"))
    const saved = h.calls.find((c) => c.url.endsWith("/drafts") && c.body.document)!.body
      .document as Record<string, unknown>
    expect(saved).toMatchObject({
      slug: "existing",
      name: "Edited recipe",
      author_crew_id: "crew1",
      author_agent_id: "a1",
      save_token: "verified",
      skip_test_gate: false,
    })
    expect(saved).not.toHaveProperty("trigger")
    expect(saved.definition).toEqual(routine.definition)
    expect(h.calls.find((c) => c.url.endsWith("/appearance"))!.body).toEqual({
      icon: "star",
      color: "blue",
    })
  })
  it("retries an appearance failure without saving the recipe or scheduling twice", async () => {
    h.appearanceFails = true
    render(<RoutineCreateDialog {...props} />)
    await waitFor(() =>
      expect(screen.queryByText("Loading saved draft…")).not.toBeInTheDocument(),
    )
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    fireEvent.click(screen.getByRole("button", { name: "Confirm and publish" }))
    await screen.findByText(/The recipe was saved, but its icon could not be saved/)
    expect(props.onCreated).not.toHaveBeenCalled()
    h.appearanceFails = false
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    fireEvent.click(screen.getByRole("button", { name: "Confirm and publish" }))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled())
    expect(h.calls.filter((c) => c.url.endsWith("/publish"))).toHaveLength(1)
  })
  it("starts a fresh recipe after successful creation instead of overwriting the previous recipe", async () => {
    const creation = { ...props, routine: undefined }
    const view = render(<RoutineCreateDialog {...creation} />)
    fireEvent.click(screen.getByText("Write it yourself", { exact: true }))
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "First recipe" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    fireEvent.click(screen.getByRole("button", { name: "Confirm and publish" }))
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled())
    view.rerender(<RoutineCreateDialog {...creation} open={false} />)
    view.rerender(<RoutineCreateDialog {...creation} open />)
    fireEvent.click(screen.getByText("Write it yourself", { exact: true }))
    expect(screen.getByLabelText("Name")).toHaveValue("")
    expect(screen.getByLabelText("Routine identifier")).toHaveValue("my-routine")
  })
  it("saves a durable draft without validating or publishing work", async () => {
    render(<RoutineCreateDialog {...props} />)
    await waitFor(() =>
      expect(screen.queryByText("Loading saved draft…")).not.toBeInTheDocument(),
    )
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Unfinished draft" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save draft", exact: true }))
    await screen.findByText(/Saved draft/)
    expect(
      h.calls.some(
        (c) =>
          c.url.includes("/test_run") ||
          c.url.endsWith("/publish") ||
          c.url.endsWith("/appearance"),
      ),
    ).toBe(false)
    expect(props.onCreated).not.toHaveBeenCalled()
    expect(screen.getByLabelText("Name")).toHaveValue("Unfinished draft")
  })
  it("keeps local work after an editor conflict and does not publish", async () => {
    h.draftConflict = true
    render(<RoutineCreateDialog {...props} />)
    await waitFor(() =>
      expect(screen.queryByText("Loading saved draft…")).not.toBeInTheDocument(),
    )
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Keep my edit" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save draft", exact: true }))
    await screen.findByText(/Another editor saved this draft/)
    expect(screen.getByLabelText("Name")).toHaveValue("Keep my edit")
    expect(h.calls.some((c) => c.url.endsWith("/publish"))).toBe(false)
  })
  it("links a blocked publication to the schedule while preserving the saved draft", async () => {
    h.scheduleConflict = true
    render(<RoutineCreateDialog {...props} />)
    await waitFor(() =>
      expect(screen.queryByText("Loading saved draft…")).not.toBeInTheDocument(),
    )
    fireEvent.click(screen.getByRole("button", { name: "Publish" }))
    fireEvent.click(screen.getByRole("button", { name: "Confirm and publish" }))
    const link = await screen.findByRole("link", {
      name: "Review schedule: Daily",
    })
    expect(link).toHaveAttribute(
      "href",
      "/routines?slug=existing&view=plan#schedule-plan1",
    )
    expect(link).toHaveAttribute("target", "_blank")
    expect(props.onCreated).not.toHaveBeenCalled()
    expect(props.onClose).not.toHaveBeenCalled()
    expect(screen.getByText(/Saved draft/)).toBeInTheDocument()
  })
  it("opens the same saved AI draft without creating a second recipe", async () => {
    h.linked = true
    render(
      <RoutineCreateDialog
        {...props}
        routine={undefined}
        savedDraftLink={{
          slug: "existing",
          id: "ai-draft-1",
          workspaceId: "ws1",
        }}
      />,
    )
    await waitFor(() =>
      expect(screen.getByLabelText("Name")).toHaveValue("Draft from Chat"),
    )
    fireEvent.click(screen.getByRole("button", { name: "Save draft", exact: true }))
    await waitFor(() =>
      expect(h.calls.some((c) => c.url.endsWith("/drafts") && c.body.document)).toBe(
        true,
      ),
    )
    expect(
      h.calls.find((c) => c.url.endsWith("/drafts") && c.body.document)?.body,
    ).toMatchObject({
      id: "ai-draft-1",
      revision: 4,
      slug: "existing",
    })
    expect(h.calls.some((c) => c.url.endsWith("/publish"))).toBe(false)
  })
  it("rejects a link to a discarded and recreated draft", async () => {
    h.linked = true
    render(
      <RoutineCreateDialog
        {...props}
        routine={undefined}
        savedDraftLink={{ slug: "existing", id: "old-draft" }}
      />,
    )
    await screen.findByText(/draft link is no longer current/)
    expect(screen.getByRole("button", { name: "Save draft", exact: true })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Publish", exact: true })).toBeDisabled()
  })
  it("opens a historical version as an unsaved draft", () => {
    render(
      <RoutineCreateDialog
        {...props}
        initialDraft={{ ...routine.definition, steps: [] }}
      />,
    )
    expect(screen.queryByText("1. work")).not.toBeInTheDocument()
    expect(h.calls.some((c) => c.url.endsWith("/publish"))).toBe(false)
  })
})

it("keeps the latest selected draft when an earlier read finishes last", async () => {
  h.savedDrafts = true
  render(<RoutineCreateDialog {...props} routine={undefined} />)
  fireEvent.click(await screen.findByRole("button", { name: "first · r1" }))
  fireEvent.click(screen.getByRole("button", { name: "second · r2" }))
  const response = (slug: string) =>
    new Response(
      JSON.stringify({
        id: slug,
        slug,
        revision: 1,
        base_pipeline_id: "",
        base_revision: 0,
        document: { name: slug, slug, definition: { name: slug, steps: [] } },
      }),
    )
  await act(async () => {
    h.draftReads.get("second")!.resolve(response("second"))
  })
  expect(await screen.findByLabelText("Name")).toHaveValue("second")
  await act(async () => {
    h.draftReads.get("first")!.resolve(response("first"))
  })
  expect(screen.getByLabelText("Name")).toHaveValue("second")
  expect(h.draftReads.get("first")!.signal?.aborted).toBe(true)
})

it("explains a non-JSON discard failure without losing the draft", async () => {
  h.linked = true
  h.discardFails = true
  const confirm = window.confirm
  window.confirm = vi.fn(() => true)
  try {
    render(
      <RoutineCreateDialog
        {...props}
        routine={undefined}
        savedDraftLink={{ slug: "existing", id: "ai-draft-1" }}
      />,
    )
    await screen.findByDisplayValue("Draft from Chat")
    fireEvent.click(screen.getByRole("button", { name: "Discard saved draft" }))
    await screen.findByText(/Could not discard the draft/)
    expect(screen.getByDisplayValue("Draft from Chat")).toBeInTheDocument()
  } finally {
    window.confirm = confirm
  }
})
