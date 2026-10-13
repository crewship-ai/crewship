import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { PageBoardView } from "../page-board-view"
import { NewBoardDialog } from "../new-board-dialog"
import { apiFetch } from "@/lib/api-fetch"
import type { PageBoard } from "@/hooks/use-page-board"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/hooks/use-pages", async original => ({ ...await original<typeof import("@/hooks/use-pages")>(), usePages: () => ({ pages: [{ slug: "report", name: "Report", hasArtifact: true }], loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("next/dynamic", () => ({ default: () => function Editor({ base, onChange, disabled }: { base: PageBoard; onChange: (elements: unknown[]) => void; disabled: boolean }) {
  return <div aria-label="Mock drawing canvas"><span>Canvas sequence {base.seq}</span><button disabled={disabled} onClick={() => onChange([{ ...base.records[0].element, x: 150 }])}>Draw rectangle</button><button disabled={disabled} onClick={() => onChange([...base.records.filter(item => item.element).map(item => item.element), ...base.placements.map(item => ({ id: `board-placement:${item.id}`, type: "embeddable", x: item.x + 20, y: item.y, width: item.width, height: item.height, angle: 0, groupIds: [], frameId: null }))])}>Move pin</button></div>
} }))
let canonical: PageBoard
let readStatus: number
let lost: boolean
let initFail: boolean
const writes: { path: string; body: Record<string, unknown> }[] = []
const clients: QueryClient[] = []
beforeEach(() => {
  canonical = { format: "board/v1", seq: 1, records: [{ element_id: "shape", element_rev: 1, deleted: false, element: { id: "shape", type: "rectangle", x: 0, y: 0, width: 100, height: 100, boundElements: null } }], placements: [], can_write: true }
  readStatus = 200; lost = false; initFail = false; writes.length = 0
  vi.mocked(apiFetch).mockImplementation(async (url, options) => {
    const path = new URL(String(url), "https://studio.test").pathname
    if (options?.method) {
      const body = JSON.parse(String(options.body)); writes.push({ path, body })
      if (path === "/api/v1/pages") return new Response(JSON.stringify({ slug: "design" }), { status: 201 })
      if (path.endsWith("/board")) {
        if (initFail) { initFail = false; return new Response(JSON.stringify({ error: "Initialization unavailable" }), { status: 500 }) }
        return new Response(JSON.stringify(canonical))
      }
      if (path.endsWith("/placements")) {
        canonical = { ...canonical, seq: canonical.seq + 1, placements: [{ id: "pin1", x: Number(body.x), y: Number(body.y), width: Number(body.width), height: Number(body.height), source: { page_slug: String(body.source_page_slug), revision: Number(body.source_revision), title: "Report" } }] }
        return new Response(JSON.stringify({ seq: canonical.seq, result_seq: canonical.seq, placement_id: "pin1" }))
      }
      if (lost) { lost = false; throw new Error("Lost response") }
      if (body.base_seq !== canonical.seq) return new Response(JSON.stringify({ error: "board changed; reload before saving" }), { status: 409 })
      canonical = { ...canonical, seq: canonical.seq + 1 }
      return new Response(JSON.stringify({ seq: canonical.seq, result_seq: canonical.seq }))
    }
    if (path.endsWith("/artifact")) return new Response(JSON.stringify({ head_rev: 3, revision: 3, html: "<h1>Report</h1>", revisions: [{ revision: 3 }], can_write: true }))
    return new Response(JSON.stringify(readStatus === 200 ? canonical : { error: "Access denied" }), { status: readStatus })
  })
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.clearAllMocks() })
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); clients.push(client)
  const dirty = vi.fn()
  render(<QueryClientProvider client={client}><PageBoardView workspaceId="ws" slug="design" onDirtyChange={dirty} /></QueryClientProvider>)
  return { client, dirty }
}
it("does not auto-submit drawing and preserves its original CAS base across remote refresh/conflict", async () => {
  const view = mount()
  fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" }))
  expect(writes).toHaveLength(0)
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  canonical = { ...canonical, seq: 2, records: [{ ...canonical.records[0], element_rev: 2, element: { ...canonical.records[0].element!, x: 300 } }] }
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  expect(screen.getByText("Canvas sequence 1")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await screen.findByText(/board changed; reload/)
  expect(writes[0].body).toMatchObject({ base_seq: 1, upserts: [{ expected_rev: 1, element: { id: "shape", x: 150 } }] })
  expect(screen.getByText("Canvas sequence 1")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Reload latest" }))
  await screen.findByText("Discard unsaved drawing?")
  expect(screen.getByText("Canvas sequence 1")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Discard and reload" }))
  await screen.findByText("Canvas sequence 2")
  await waitFor(() => expect(view.dirty).toHaveBeenLastCalledWith(false))
})
it("retries an uncertain explicit save with the same mutation identity", async () => {
  mount(); fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" })); lost = true
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await screen.findByText(/Lost response/)
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0].body).toEqual(writes[1].body)
  expect(writes[0].body.mutation_id).toEqual(expect.any(String))
})
it("pins a concrete source revision only after explicit confirmation", async () => {
  mount(); fireEvent.click(await screen.findByRole("button", { name: "Pin artifact" }))
  fireEvent.change(screen.getByLabelText("Artifact"), { target: { value: "report" } })
  await screen.findByRole("option", { name: "Revision 3 · latest" })
  expect(writes).toHaveLength(0)
  fireEvent.click(screen.getByRole("button", { name: "Pin saved revision" }))
  await screen.findByText("Report · Revision 3")
  expect(writes[0]).toMatchObject({ path: "/api/v1/pages/design/board/placements", body: { base_seq: 1, source_page_slug: "report", source_revision: 3 } })
})
it("unmounts private canvas and clears navigation dirtiness after access denial", async () => {
  const view = mount(); fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" })); readStatus = 403
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  await waitFor(() => expect(screen.queryByLabelText("Mock drawing canvas")).toBeNull())
  await waitFor(() => expect(view.dirty).toHaveBeenLastCalledWith(false))
})
it("retries partial board initialization on the same created page and mutation", async () => {
  const client = new QueryClient(); clients.push(client); initFail = true
  render(<QueryClientProvider client={client}><NewBoardDialog workspaceId="ws" onClose={vi.fn()} onCreated={vi.fn()} /></QueryClientProvider>)
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Design" } })
  fireEvent.click(screen.getByRole("button", { name: "Create board" }))
  await screen.findByText("Initialization unavailable")
  fireEvent.click(screen.getByRole("button", { name: "Retry initialization" }))
  await waitFor(() => expect(writes).toHaveLength(3))
  expect(writes.filter(item => item.path === "/api/v1/pages")).toHaveLength(1)
  expect(writes[1].body).toEqual(writes[2].body)
})

it("saves pin movement in the same atomic batch without serializing source URLs into drawing geometry", async () => {
  canonical.placements = [{ id: "pin", x: 10, y: 20, width: 400, height: 300, source: { page_slug: "report", revision: 3, title: "Report" } }]
  mount(); fireEvent.click(await screen.findByRole("button", { name: "Move pin" }))
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatchObject({ path: "/api/v1/pages/design/board/batches", body: { base_seq: 1, upserts: [], deletes: [], placement_updates: [{ id: "pin", x: 30, y: 20, width: 400, height: 300 }], placement_deletes: [] } })
})
it("retains the unsaved drawing and guard through a transient background read failure", async () => {
  const view = mount(); fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" })); readStatus = 500
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  await screen.findByText(/Could not refresh board access/)
  expect(screen.getByText("Canvas sequence 1")).toBeTruthy()
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  expect(writes).toHaveLength(0)
})
it("shows a read-only canvas without mutation controls", async () => {
  canonical.can_write = false; mount()
  await screen.findByText("Read-only board. Your access does not allow saving changes.")
  expect(screen.queryByRole("button", { name: "Save board" })).toBeNull()
  expect(screen.getByRole("button", { name: "Draw rectangle" })).toBeDisabled()
  expect(writes).toHaveLength(0)
})
