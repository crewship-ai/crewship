import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { PageBoardView } from "../page-board-view"
import { NewBoardDialog } from "../new-board-dialog"
import { apiFetch } from "@/lib/api-fetch"
import type { PageBoard } from "@/hooks/use-page-board"
vi.mock("../board-agent-work", () => ({ BoardAgentWork: () => null }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn(), useRealtimeChannelSafe: vi.fn(), usePresenceTransportSafe: () => ({ status: null }) }))
vi.mock("@/hooks/use-pages", async original => ({ ...await original<typeof import("@/hooks/use-pages")>(), usePages: () => ({ pages: [{ slug: "report", name: "Report", hasArtifact: true }], loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("next/dynamic", () => ({ default: () => function Editor({ base, onChange, disabled }: { base: PageBoard; onChange: (elements: unknown[]) => void; disabled: boolean }) {
  return <div aria-label="Mock drawing canvas"><span>Canvas sequence {base.seq}</span><button disabled={disabled} onClick={() => onChange([{ ...base.records[0].element, x: 150 }])}>Draw rectangle</button><button disabled={disabled} onClick={() => onChange([...base.records.filter(item => item.element).map(item => item.element), ...base.placements.map(item => ({ id: `board-placement:${item.id}`, type: "embeddable", x: item.x + 20, y: item.y, width: item.width, height: item.height, angle: 0, groupIds: [], frameId: null }))])}>Move pin</button></div>
} }))
let canonical: PageBoard
let readStatus: number
let lost: boolean
let initFail: boolean
let missingUndoProof: boolean
let expiredUndo: boolean
let sourceHead: number
let loseModeResponse: boolean
const modeReceipts = new Map<string, object>()
const writes: { path: string; body: Record<string, unknown> }[] = []
const clients: QueryClient[] = []
beforeEach(() => {
  canonical = { format: "crewship-board-excalidraw-0.18.1-v1", seq: 1, records: [{ element_id: "shape", element_rev: 1, deleted: false, element: { id: "shape", type: "rectangle", x: 0, y: 0, width: 100, height: 100, boundElements: null } }], placements: [], can_write: true }
  readStatus = 200; lost = false; initFail = false; missingUndoProof = false; expiredUndo = false; sourceHead = 3; loseModeResponse = false; modeReceipts.clear(); writes.length = 0
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
      if (modeReceipts.has(String(body.mutation_id))) return new Response(JSON.stringify(modeReceipts.get(String(body.mutation_id))))
      if (lost) { lost = false; throw new Error("Lost response") }
      if (body.base_seq !== canonical.seq) return new Response(JSON.stringify({ error: "board changed; reload before saving" }), { status: 409 })
      const records = new Map(canonical.records.map(record => [record.element_id, record]))
      for (const item of (body.upserts ?? []) as { element: { id: string }; expected_rev: number }[]) records.set(item.element.id, { element_id: item.element.id, element_rev: item.expected_rev + 1, element: item.element, deleted: false })
      for (const item of (body.deletes ?? []) as { id: string; expected_rev: number }[]) records.set(item.id, { element_id: item.id, element_rev: item.expected_rev + 1, element: null, deleted: true })
      const modes = (body.placement_modes ?? []) as { id: string; mode: "fixed" | "follow_latest"; source_revision: number }[]
      const placements = canonical.placements.map(item => { const mode = modes.find(change => change.id === item.id); return mode ? { ...item, mode: mode.mode, placement_rev: (item.placement_rev ?? 0) + 1, source: { ...item.source!, pinned_revision: mode.source_revision, revision: mode.mode === "fixed" ? mode.source_revision : sourceHead } } : item })
      canonical = { ...canonical, seq: canonical.seq + 1, records: [...records.values()], placements }
      if (modes.length) {
        const receipt = { seq: canonical.seq, result_seq: canonical.seq, placement_revs: Object.fromEntries(placements.map(item => [item.id, item.placement_rev])), undo_expires_at: new Date(Date.now() + 86400000).toISOString() }
        modeReceipts.set(String(body.mutation_id), receipt)
        if (loseModeResponse) { loseModeResponse = false; throw new Error("Mode response lost") }
        return new Response(JSON.stringify(receipt))
      }
      return new Response(JSON.stringify({ seq: canonical.seq, result_seq: canonical.seq, ...(missingUndoProof ? {} : { element_revs: Object.fromEntries(canonical.records.map(item => [item.element_id, item.element_rev])), undo_expires_at: new Date(Date.now() + (expiredUndo ? -1 : 86400000)).toISOString() }) }))
    }
    if (path.endsWith("/board/threads")) return new Response(JSON.stringify({ threads: [], can_comment: true, can_resolve: true }))
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
it("rebases only disjoint remote geometry and saves original local element revision", async () => {
  canonical.records.push({ element_id: "other", element_rev: 1, deleted: false, element: { id: "other", type: "rectangle", x: 0 } })
  const view = mount()
  fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" }))
  // The shallow editor's draw command replaces its scene, so preserve the other
  // record as a tombstone in this test: local deletion and remote new ID disjoint.
  canonical = { ...canonical, seq: 2, records: [...canonical.records, { element_id: "remote", element_rev: 1, deleted: false, element: { id: "remote", type: "rectangle", x: 500 } }] }
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  await screen.findByText("Canvas sequence 2")
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  expect(writes).toHaveLength(0)
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0].body).toMatchObject({ base_seq: 2, upserts: [{ expected_rev: 1, element: { id: "shape", x: 150 } }], deletes: [{ id: "other", expected_rev: 1 }] })
})

it("undoes an accepted saved command with exact applied element revision", async () => {
  mount(); fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" }))
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await screen.findByText("Canvas sequence 2")
  const undo = screen.getByRole("button", { name: "Undo last saved change" })
  await waitFor(() => expect(undo).not.toBeDisabled())
  fireEvent.click(undo)
  await screen.findByText("Canvas sequence 3")
  expect(writes[1].body).toMatchObject({ base_seq: 2, upserts: [{ expected_rev: 2, element: { id: "shape", x: 0 } }] })
})
it("refuses saved undo after the same element changes remotely", async () => {
  const view = mount(); fireEvent.click(await screen.findByRole("button", { name: "Draw rectangle" }))
  fireEvent.click(screen.getByRole("button", { name: "Save board" }))
  await screen.findByText("Canvas sequence 2")
  canonical = { ...canonical, seq: 3, records: [{ ...canonical.records[0], element_rev: 3, element: { ...canonical.records[0].element!, x: 800, y: 900 } }] }
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  await screen.findByText("Canvas sequence 3")
  fireEvent.click(screen.getByRole("button", { name: "Undo last saved change" }))
  await screen.findByText(/can no longer be undone/)
  expect(writes).toHaveLength(1)
})

it('keeps a successful save but withholds undo when legacy receipt proof is missing', async () => {
  missingUndoProof = true
  mount(); fireEvent.click(await screen.findByRole('button', { name: 'Draw rectangle' }))
  fireEvent.click(screen.getByRole('button', { name: 'Save board' }))
  await screen.findByText('Canvas sequence 2')
  await screen.findByText(/server did not provide complete revision or expiration proof/)
  expect(screen.getByRole('button', { name: 'Undo last saved change' })).toBeDisabled()
})
it('disables saved undo at the original expired server deadline', async () => {
  expiredUndo = true
  mount(); fireEvent.click(await screen.findByRole('button', { name: 'Draw rectangle' }))
  fireEvent.click(screen.getByRole('button', { name: 'Save board' }))
  await screen.findByText('Canvas sequence 2')
  expect(screen.getByRole('button', { name: 'Undo last saved change' })).toBeDisabled()
  expect(screen.getByText(/Saved undo expires after 24 hours/)).toBeTruthy()
})

it("confirms follow-latest explicitly, preserves committed-unknown mode identity and undoes to the original baseline", async () => {
  canonical.placements = [{ id: "pin", placement_rev: 1, mode: "fixed", x: 0, y: 0, width: 100, height: 100, source: { page_slug: "report", title: "Report", revision: 3, pinned_revision: 3 } }]
  const view = mount(); sourceHead = 7
  fireEvent.click(await screen.findByRole("button", { name: "Follow latest" }))
  await screen.findByText("Follow latest artifact revision?")
  expect(writes).toHaveLength(0)
  loseModeResponse = true
  fireEvent.click(screen.getByRole("button", { name: "Follow latest" }))
  await screen.findAllByText(/Mode response lost/)
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  expect(screen.getByText("Report · Revision 3")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Retry mode change" }))
  await screen.findByText("Board · Saved sequence 2")
  expect(writes[0].body).toEqual(writes[1].body)
  expect(writes[0].body).toMatchObject({ base_seq: 1, placement_modes: [{ id: "pin", expected_rev: 1, mode: "follow_latest", source_revision: 3 }] })
  await screen.findByText("Report · Revision 7")
  fireEvent.click(screen.getByRole("button", { name: "Undo last saved change" }))
  await screen.findByText("Board · Saved sequence 3")
  expect(writes[2].body).toMatchObject({ base_seq: 2, placement_modes: [{ id: "pin", expected_rev: 2, mode: "fixed", source_revision: 3 }] })
})
it("holds follow-latest preview during a local draft but removes source metadata immediately after revocation", async () => {
  canonical.placements = [{ id: "pin", placement_rev: 1, mode: "follow_latest", x: 0, y: 0, width: 100, height: 100, source: { page_slug: "report", title: "Report", revision: 3, pinned_revision: 1 } }]
  const view = mount(); await screen.findByText("Report · Revision 3")
  fireEvent.click(screen.getByRole("button", { name: "Move pin" }))
  canonical = { ...canonical, placements: [{ ...canonical.placements[0], source: { ...canonical.placements[0].source!, revision: 7 } }] }
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  expect(screen.getByText("Report · Revision 3")).toBeTruthy()
  expect(screen.queryByText("Report · Revision 7")).toBeNull()
  expect(screen.getByRole("button", { name: "Fix displayed revision" })).toBeDisabled()
  const { source: _source, ...redacted } = canonical.placements[0]
  canonical = { ...canonical, placements: [redacted] }
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  await screen.findByText("Artifact unavailable")
  expect(screen.queryByText("Report · Revision 3")).toBeNull()
  expect(view.dirty).toHaveBeenLastCalledWith(true)
})
it("closes an unsubmitted mode choice and removes its title when source access is revoked", async () => {
  canonical.placements = [{ id: "pin", placement_rev: 1, mode: "fixed", x: 0, y: 0, width: 100, height: 100, source: { page_slug: "report", title: "Private report", revision: 3, pinned_revision: 3 } }]
  const view = mount(); fireEvent.click(await screen.findByRole("button", { name: "Follow latest" }))
  await screen.findByText("Private report · Board sequence 1")
  const { source: _source, ...redacted } = canonical.placements[0]
  canonical = { ...canonical, placements: [redacted] }
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["page-board"] }) })
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(screen.queryByText(/Private report/)).toBeNull()
  expect(writes).toHaveLength(0)
})
