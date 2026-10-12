import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { PipelineDetailSheet } from "../pipeline-detail-sheet"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", async (original) => ({ ...(await original<typeof import("@/lib/api-fetch")>()), apiFetch: api }))
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const versions = [
  { version: 2, definition_hash: "latest-hash-long-enough", author_type: "user", author_id: "user-one", parent_version: 1, change_summary: "Second revision", created_at: "2026-10-02T10:00:00Z" },
  { version: 1, definition_hash: "first-hash-long-enough", author_type: "agent", author_id: "agent-one", created_at: "2026-10-01T10:00:00Z" },
]
function pipeline(slug = "routine", overrides: Record<string, unknown> = {}) {
  return { id: slug, slug, name: `Routine ${slug}`, description: "Routine description", dsl_version: "1", definition_hash: "abcdef1234567890abcdef", invocation_count: 4, authored_via: "cli", created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-02T10:00:00Z", definition: { steps: [] }, ...overrides }
}
function defaultRead(url: string): Response {
  if (url.includes("/runs?")) return json([])
  if (url.endsWith("/versions")) return json(versions)
  return json(pipeline(url.split("/").at(-1)))
}
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: Error) => void; const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail }); return { promise, resolve, reject } }
function tab(name: string) { fireEvent.mouseDown(screen.getByRole("tab", { name: new RegExp(`^${name}`) }), { button: 0, ctrlKey: false }) }
async function loaded() { await screen.findByRole("button", { name: "Export bundle" }) }
beforeEach(() => {
  api.mockReset()
  api.mockImplementation((url: string) => Promise.resolve(defaultRead(url)))
  vi.stubGlobal("confirm", vi.fn(() => true))
  vi.stubGlobal("alert", vi.fn())
})
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

it.each([
  { open: false, slug: "routine", workspaceId: "ws" },
  { open: true, slug: null, workspaceId: "ws" },
  { open: true, slug: "routine", workspaceId: "" },
])("does not fetch a closed or incomplete selection: %j", (props) => {
  render(<PipelineDetailSheet {...props} onClose={vi.fn()} />)
  expect(api).not.toHaveBeenCalled()
})

it("shows routine metadata, definition, history and run details", async () => {
  api.mockImplementation((url: string) => Promise.resolve(url.includes("/runs?") ? json([
    { id: "run-one", entry_type: "pipeline.run.completed", severity: "info", summary: "Completed report", ts: "2026-10-02T10:00:00Z", run_id: "full-run-identity-long" },
    { id: "run-two", entry_type: "pipeline.run.failed", severity: "error", summary: "Failed report", ts: "2026-10-02T11:00:00Z" },
  ]) : url.endsWith("/versions") ? json(versions) : json(pipeline("routine", { author_crew_id: "author-crew", author_agent_id: "author-agent", last_invoked_at: "2026-10-02T10:00:00Z", last_invocation_status: "completed" }))))
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded()
  for (const value of ["Routine description", "author-crew", "author-agent", "4", "cli"]) expect(screen.getByText(value)).toBeVisible()
  fireEvent.click(screen.getByText("Show DSL"))
  expect(screen.getByText(/"steps": \[\]/)).toBeVisible()
  tab("Versions")
  expect(screen.getByText("Second revision")).toBeVisible()
  expect(screen.getByText("parent v1")).toBeVisible()
  expect(screen.getAllByRole("button", { name: "Rollback" })).toHaveLength(1)
  tab("Runs")
  expect(screen.getByText("Completed report")).toBeVisible()
  expect(screen.getByText("Failed report")).toBeVisible()
  expect(screen.getByText("error")).toHaveClass("text-destructive")
})

it("renders empty history and runs and optional metadata fallbacks", async () => {
  api.mockImplementation((url: string) => Promise.resolve(url.includes("/runs?") || url.endsWith("/versions") ? json([]) : json(pipeline("routine", { definition: undefined, description: undefined }))))
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded()
  expect(screen.getAllByText("—")).toHaveLength(2)
  expect(screen.queryByText("Show DSL")).not.toBeInTheDocument()
  tab("Versions")
  expect(screen.getByText("No version history yet.")).toBeVisible()
  tab("Runs")
  expect(screen.getByText(/No runs yet/)).toBeVisible()
})

it("clears old details immediately and does not present them after a new selection fails", async () => {
  const pending = deferred<Response>()
  api.mockImplementation((url: string) => url.endsWith("/new") ? pending.promise : Promise.resolve(defaultRead(url)))
  const view = render(<PipelineDetailSheet workspaceId="ws" slug="old" open onClose={vi.fn()} />)
  await loaded()
  expect(screen.getByText("Routine old")).toBeVisible()
  view.rerender(<PipelineDetailSheet workspaceId="ws" slug="new" open onClose={vi.fn()} />)
  expect(screen.queryByText("Routine old")).not.toBeInTheDocument()
  await act(async () => pending.resolve(json({}, 503)))
  expect(await screen.findByRole("alert")).toHaveTextContent("pipeline: 503")
  expect(screen.queryByRole("button", { name: "Export bundle" })).not.toBeInTheDocument()
})

it("ignores delayed reads for an abandoned selection", async () => {
  const old = deferred<Response>()
  api.mockImplementation((url: string) => url.endsWith("/old") ? old.promise : Promise.resolve(defaultRead(url)))
  const view = render(<PipelineDetailSheet workspaceId="ws" slug="old" open onClose={vi.fn()} />)
  view.rerender(<PipelineDetailSheet workspaceId="ws" slug="new" open onClose={vi.fn()} />)
  await loaded()
  await act(async () => old.resolve(json(pipeline("old"))))
  expect(screen.getByText("Routine new")).toBeVisible()
  expect(screen.queryByText("Routine old")).not.toBeInTheDocument()
})

it.each(["pipeline refusal", "versions refusal", "network", "invalid json"])("shows %s as a read error", async (kind) => {
  api.mockImplementation((url: string) => {
    if (url.includes("/runs?")) return Promise.resolve(json([]))
    if (kind === "network") return Promise.reject(new Error("offline"))
    if (kind === "invalid json") return Promise.resolve(new Response("invalid"))
    return Promise.resolve((kind === "pipeline refusal" && !url.endsWith("/versions")) || (kind === "versions refusal" && url.endsWith("/versions")) ? json({}, 403) : defaultRead(url))
  })
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  expect(await screen.findByRole("alert")).toHaveTextContent("Error:")
  expect(screen.queryByRole("button", { name: "Export bundle" })).not.toBeInTheDocument()
})

it("confirms rollback, sends the chosen historical version and refreshes the current detail", async () => {
  let restored = false
  api.mockImplementation((url: string, init?: RequestInit) => {
    if (init?.method === "POST") { restored = true; return Promise.resolve(json({})) }
    if (url.includes("/runs?") || url.endsWith("/versions")) return Promise.resolve(defaultRead(url))
    return Promise.resolve(json(pipeline("routine", { name: restored ? "Restored routine" : "Original routine" })))
  })
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded(); tab("Versions")
  vi.mocked(globalThis.confirm).mockReturnValueOnce(false)
  fireEvent.click(screen.getByRole("button", { name: "Rollback" }))
  expect(api.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0)
  fireEvent.click(screen.getByRole("button", { name: "Rollback" }))
  expect(await screen.findByText("Restored routine")).toBeVisible()
  expect(globalThis.confirm).toHaveBeenCalledWith(expect.stringContaining("next save will be version 3"))
  const [url, init] = api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(url).toBe("/api/v1/workspaces/ws/pipelines/routine/rollback")
  expect(JSON.parse(init.body)).toEqual({ version: 1 })
})

it("does not let an old rollback reload into a new workspace", async () => {
  const rollback = deferred<Response>()
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "POST" ? rollback.promise : Promise.resolve(defaultRead(url)))
  const view = render(<PipelineDetailSheet workspaceId="old" slug="old" open onClose={vi.fn()} />)
  await loaded(); tab("Versions")
  fireEvent.click(screen.getByRole("button", { name: "Rollback" }))
  view.rerender(<PipelineDetailSheet workspaceId="new" slug="new" open onClose={vi.fn()} />)
  await screen.findByText("Routine new")
  const oldReadsBefore = api.mock.calls.filter(([url, init]) => url.includes("/old/") && !init?.method).length
  await act(async () => rollback.resolve(json({})))
  expect(screen.getByText("Routine new")).toBeVisible()
  expect(screen.queryByText("Routine old")).not.toBeInTheDocument()
  expect(api.mock.calls.filter(([url, init]) => url.includes("/old/") && !init?.method)).toHaveLength(oldReadsBefore)
})

it.each(["refused", "network"])("reports a %s rollback without losing the loaded history", async (kind) => {
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "POST" ? kind === "network" ? Promise.reject(new Error("offline")) : Promise.resolve(new Response("not allowed", { status: 403 })) : Promise.resolve(defaultRead(url)))
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded(); tab("Versions")
  fireEvent.click(screen.getByRole("button", { name: "Rollback" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(/Rollback/)
  expect(screen.getByText("Second revision")).toBeVisible()
})

it("downloads the current bundle and releases its object URL", async () => {
  const bundle = { routine: "routine", history: [1, 2] }
  api.mockImplementation((url: string) => Promise.resolve(url.includes("/export?") ? json(bundle) : defaultRead(url)))
  const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test-bundle")
  const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {})
  let downloaded = ""
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { downloaded = this.download })
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded()
  fireEvent.click(screen.getByRole("button", { name: "Export bundle" }))
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
  expect(downloaded).toBe("routine-routine-bundle.json")
  expect(JSON.parse(await (create.mock.calls[0][0] as Blob).text())).toEqual(bundle)
  expect(revoke).toHaveBeenCalledWith("blob:test-bundle")
})

it.each(["refused", "network", "invalid json"])("reports %s exports and permits retry", async (kind) => {
  api.mockImplementation((url: string) => !url.includes("/export?") ? Promise.resolve(defaultRead(url)) : kind === "network" ? Promise.reject(new Error("offline")) : Promise.resolve(kind === "refused" ? json({}, 403) : new Response("invalid")))
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded()
  fireEvent.click(screen.getByRole("button", { name: "Export bundle" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(/Export/)
  expect(screen.getByRole("button", { name: "Export bundle" })).toBeEnabled()
})

it("cancels an abandoned export and closes through the sheet control", async () => {
  const pending = deferred<Response>()
  api.mockImplementation((url: string) => url.includes("/export?") ? pending.promise : Promise.resolve(defaultRead(url)))
  const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:abandoned")
  const close = vi.fn()
  const view = render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={close} />)
  await loaded()
  fireEvent.click(screen.getByRole("button", { name: "Export bundle" }))
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  expect(close).toHaveBeenCalledTimes(1)
  view.rerender(<PipelineDetailSheet workspaceId="ws" slug="routine" open={false} onClose={close} />)
  await act(async () => pending.resolve(json({})))
  expect(create).not.toHaveBeenCalled()
})

it("handles a legacy non-array history response and an invocation without status", async () => {
  api.mockImplementation((url: string) => Promise.resolve(url.endsWith("/versions") ? json({ versions: [] }) : url.includes("/runs?") ? json([]) : json(pipeline("routine", { last_invoked_at: "2026-10-02T10:00:00Z" }))))
  render(<PipelineDetailSheet workspaceId="ws" slug="routine" open onClose={vi.fn()} />)
  await loaded()
  expect(screen.getByText("Last invoked").parentElement).not.toHaveTextContent("undefined")
  tab("Versions")
  expect(screen.getByText("No version history yet.")).toBeVisible()
})

it.each(["rollback body", "rollback reload", "export body", "rollback rejection", "export rejection", "read rejection"])("ignores stale %s after the panel changes routine", async (kind) => {
  const pending = deferred<Response>()
  const body = deferred<unknown>()
  const text = deferred<string>()
  const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:stale")
  let blocked = false
  if (kind === "read rejection") api.mockImplementation((url: string) => url.endsWith("/old") ? pending.promise : Promise.resolve(defaultRead(url)))
  const view = render(<PipelineDetailSheet workspaceId="ws" slug="old" open onClose={vi.fn()} />)
  if (kind !== "read rejection") {
    await loaded()
    api.mockImplementation((url: string) => {
      if (url.endsWith("/rollback")) {
        if (kind === "rollback rejection") { blocked = true; return pending.promise }
        const response = json({}, kind === "rollback body" ? 403 : 200)
        if (kind === "rollback body") vi.spyOn(response, "text").mockImplementation(() => { blocked = true; return text.promise })
        return Promise.resolve(response)
      }
      if (url.includes("/export?")) {
        if (kind === "export rejection") { blocked = true; return pending.promise }
        const response = json({})
        vi.spyOn(response, "json").mockImplementation(() => { blocked = true; return body.promise })
        return Promise.resolve(response)
      }
      if (url.endsWith("/old") && kind === "rollback reload") { blocked = true; return pending.promise }
      return Promise.resolve(defaultRead(url))
    })
    if (kind.startsWith("rollback")) { tab("Versions"); fireEvent.click(screen.getByRole("button", { name: "Rollback" })) }
    else fireEvent.click(screen.getByRole("button", { name: "Export bundle" }))
    await waitFor(() => expect(blocked).toBe(true))
  }
  view.rerender(<PipelineDetailSheet workspaceId="ws" slug="new" open onClose={vi.fn()} />)
  await screen.findByText("Routine new")
  await act(async () => {
    if (kind.endsWith("rejection")) pending.reject(new Error("stale request failed"))
    else if (kind === "rollback body") text.resolve("stale refusal")
    else if (kind === "export body") body.resolve({ abandoned: true })
    else pending.resolve(json(pipeline("old")))
  })
  expect(screen.getByText("Routine new")).toBeVisible()
  expect(screen.queryByText("Routine old")).not.toBeInTheDocument()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  expect(create).not.toHaveBeenCalled()
})
