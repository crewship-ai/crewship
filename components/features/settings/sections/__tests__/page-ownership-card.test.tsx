import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import type { PageOwnership } from "@/lib/page-ownership"
import { PageOwnershipCard } from "../page-ownership-card"

vi.mock("@/lib/api-fetch", async importOriginal => ({
  ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: vi.fn(),
}))
let client: QueryClient
let pages: PageOwnership[], readStatus: number, writeStatus: number, lostResponse: boolean
const reads: string[] = []
const writes: { url: string; body: Record<string, unknown> }[] = []
const initial = (): PageOwnership => ({ id: "page-1", slug: "private-report", name: "Private report", type: "artifact", owner_user_id: "departing", owner_name: "Departing owner", eligible_successors: [{ user_id: "reader", name: "Existing reader", email: "reader@example.test" }] })
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  pages = [initial()]; readStatus = 200; writeStatus = 200; lostResponse = false; writes.length = 0; reads.length = 0
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (url, init) => {
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body)); writes.push({ url: String(url), body })
      if (writeStatus !== 200) return Response.json({ error: writeStatus === 409 ? "Owner changed" : "Eligibility lost" }, { status: writeStatus })
      pages = [{ ...pages[0], owner_user_id: body.successor_user_id, owner_name: "Existing reader", eligible_successors: [] }]
      if (lostResponse) throw new TypeError("connection lost")
      return Response.json(pages[0])
    }
    if (readStatus !== 200) return Response.json({ error: "Ownership access denied" }, { status: readStatus })
    reads.push(String(url))
    const params = new URL(String(url), 'http://fixture.test').searchParams
    const selected = pages.filter(page => !params.get('owner_user_id') || page.owner_user_id === params.get('owner_user_id'))
    const start = Number(params.get('cursor') ?? 0), limit = Number(params.get('limit') ?? 25)
    const end = start + limit
    return Response.json({ pages: selected.slice(start, end), ...(end < selected.length ? { next_cursor: String(end) } : {}) })
  })
})
afterEach(() => { cleanup(); client.clear() })
const view = (workspaceId = "ws", role = "ADMIN") => <QueryClientProvider client={client}><PageOwnershipCard key={`${workspaceId}:${role}`} workspaceId={workspaceId} role={role} /></QueryClientProvider>
async function chooseAndReview() {
  fireEvent.change(await screen.findByLabelText("New owner for Private report"), { target: { value: "reader" } })
  fireEvent.click(screen.getByRole("button", { name: "Transfer ownership of Private report" }))
  return screen.getByRole("alertdialog")
}

it("requires an explicit privacy confirmation and sends only the reviewed owner and existing reader", async () => {
  render(view())
  await screen.findByText("Artifact · Owner: Departing owner")
  expect(document.querySelector("iframe")).toBeNull()
  // Exact visible labels must be independent of the select's option text.
  const successor = screen.getByLabelText("New owner for Private report") as HTMLSelectElement
  const ownerFilter = screen.getByLabelText("Filter by owner") as HTMLSelectElement
  expect(successor.labels?.[0].textContent).toBe("New owner for Private report")
  expect(ownerFilter.labels?.[0].textContent).toBe("Filter by owner")
  expect(successor.labels?.[0]).toHaveAttribute("for", successor.id)
  expect(ownerFilter.labels?.[0]).toHaveAttribute("for", ownerFilter.id)
  expect(successor.id).not.toBe(ownerFilter.id)
  expect(writes).toHaveLength(0)
  const dialog = await chooseAndReview()
  expect(dialog).toHaveTextContent("gaining control of this private output and its retained history")
  expect(dialog).toHaveTextContent("Existing grants are rechecked under current permissions")
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(writes).toHaveLength(0)
  await chooseAndReview()
  fireEvent.click(screen.getByRole("button", { name: "Confirm ownership transfer" }))
  await screen.findByText("Artifact · Owner: Existing reader")
  expect(writes).toEqual([{ url: "/api/v1/workspaces/ws/page-ownership/page-1?workspace_id=ws", body: { expected_owner_user_id: "departing", successor_user_id: "reader" } }])
  expect(screen.queryByRole("alertdialog")).toBeNull()
})

it("never advances an open confirmation's owner after a background read and refreshes a conflict without retry", async () => {
  render(view())
  await chooseAndReview()
  pages = [{ ...initial(), owner_user_id: "other-owner", owner_name: "Other owner" }]
  await act(async () => { await client.invalidateQueries({ queryKey: ["page-ownership", "ws"] }) })
  expect(screen.getByRole("alertdialog")).toHaveTextContent("replace Departing owner")
  writeStatus = 409
  fireEvent.click(screen.getByRole("button", { name: "Confirm ownership transfer" }))
  await screen.findByText(/Ownership changed\. Review the refreshed owner/)
  expect(writes).toHaveLength(1)
  expect(writes[0].body.expected_owner_user_id).toBe("departing")
  expect(screen.getByText("Artifact · Owner: Other owner")).toBeInTheDocument()
  expect(screen.getByLabelText("New owner for Private report")).toHaveValue("")
  expect(screen.getByRole("button", { name: "Transfer ownership of Private report" })).toBeDisabled()
})

it("purges metadata, candidates and confirmation on denied refresh and permits only a fresh explicit review", async () => {
  render(view())
  await chooseAndReview()
  readStatus = 403
  await act(async () => { await client.invalidateQueries({ queryKey: ["page-ownership", "ws"] }) })
  await screen.findByText("Ownership access denied")
  expect(screen.queryByText("Private report")).toBeNull()
  expect(screen.queryByText("reader@example.test")).toBeNull()
  expect(screen.queryByRole("alertdialog")).toBeNull()
  expect(client.getQueriesData({ queryKey: ["page-ownership", "ws"] }).every(([, data]) => data === undefined)).toBe(true)
  expect(writes).toHaveLength(0)
  readStatus = 200
  fireEvent.click(screen.getByRole("button", { name: "Refresh ownership" }))
  expect(await screen.findByLabelText("New owner for Private report")).toHaveValue("")
})

it("clears metadata on mutation permission loss without retrying or adding a grant", async () => {
  render(view())
  await chooseAndReview(); writeStatus = 403
  fireEvent.click(screen.getByRole("button", { name: "Confirm ownership transfer" }))
  await screen.findByText("Eligibility lost")
  expect(screen.queryByLabelText("New owner for Private report")).toBeNull()
  expect(screen.queryByRole("alertdialog")).toBeNull()
  expect(writes).toHaveLength(1)
  expect(vi.mocked(apiFetch).mock.calls.filter(([, init]) => init?.method && init.method !== "POST")).toHaveLength(0)
})

it("guides an owner to existing sharing controls when no candidate is eligible and filters metadata by owner", async () => {
  pages.push({ ...initial(), id: "board", slug: "private-board", name: "Private Board", type: "board", owner_user_id: "other", owner_name: "Other owner", eligible_successors: [] })
  render(view())
  await screen.findByText("Board · Owner: Other owner")
  expect(screen.getByText(/Ask the current owner to open this Page's sharing settings/)).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Transfer ownership of Private Board" })).toBeNull()
  fireEvent.change(screen.getByLabelText("Filter by owner"), { target: { value: "other" } })
  expect(screen.queryByText("Private report")).toBeNull()
  expect(await screen.findByRole("link", { name: "Private Board" })).toHaveAttribute("href", "/pages/private-board")
  expect(writes).toHaveLength(0)
})

it("does not automatically retry an unconfirmed transfer and reads back the already-transferred owner", async () => {
  render(view()); await chooseAndReview(); lostResponse = true
  fireEvent.click(screen.getByRole("button", { name: "Confirm ownership transfer" }))
  await screen.findByText("Transfer could not be confirmed. Refresh ownership before trying again.")
  await screen.findByText("Artifact · Owner: Existing reader")
  expect(writes).toHaveLength(1)
  expect(screen.queryByRole("alertdialog")).toBeNull()
})

it("hides cached ownership immediately on role loss and never requests it for a manager", async () => {
  const { rerender } = render(view())
  await screen.findByLabelText("New owner for Private report")
  const calls = vi.mocked(apiFetch).mock.calls.length
  rerender(view("ws", "MANAGER"))
  expect(screen.queryByText("Artifact and Board ownership")).toBeNull()
  expect(screen.queryByText("Private report")).toBeNull()
  expect(vi.mocked(apiFetch).mock.calls).toHaveLength(calls)
})

it("removes the previous workspace metadata while the next workspace is still loading", async () => {
  const { rerender } = render(view())
  await chooseAndReview()
  let release!: (response: Response) => void
  const next = new Promise<Response>(resolve => { release = resolve })
  vi.mocked(apiFetch).mockImplementation(async () => next)
  rerender(view("other"))
  expect(screen.queryByText("Private report")).toBeNull()
  expect(screen.queryByRole("alertdialog")).toBeNull()
  await act(async () => { release(Response.json({ pages: [{ ...initial(), id: "other-page", name: "Other workspace output" }] })); await next })
  expect(await screen.findByRole("link", { name: "Other workspace output" })).toBeInTheDocument()
  expect(writes).toHaveLength(0)
})

it("loads every output page and resets the opaque cursor and rows when the owner filter changes", async () => {
  pages = Array.from({ length: 26 }, (_, index) => ({ ...initial(), id: `page-${index}`, name: `Output ${index}`, slug: `output-${index}` }))
  pages.push({ ...initial(), id: "other-page", name: "Other output", owner_user_id: "other", owner_name: "Other owner" })
  render(view())
  await screen.findByRole("link", { name: "Output 0" })
  expect(screen.queryByRole("link", { name: "Output 25" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Load more outputs" }))
  await screen.findByRole("link", { name: "Output 25" })
  expect(screen.getAllByRole("link")).toHaveLength(27)
  expect(screen.queryByRole("button", { name: "Load more outputs" })).toBeNull()
  expect(new URL(reads.at(-1)!, "http://fixture.test").searchParams.get("cursor")).toBe("25")
  fireEvent.change(screen.getByLabelText("Filter by owner"), { target: { value: "other" } })
  await screen.findByRole("link", { name: "Other output" })
  expect(screen.getAllByRole("link")).toHaveLength(1)
  const query = new URL(reads.at(-1)!, "http://fixture.test").searchParams
  expect(query.get("owner_user_id")).toBe("other")
  expect(query.has("cursor")).toBe(false)
  expect(query.get("limit")).toBe("25")
})


it.each([ ["document", "Doc"], ["slides", "Slides"] ])("renders %s custody metadata with its own label and reviewed transfer", async (type,label) => {
  let row={...initial(),type}
  vi.mocked(apiFetch).mockImplementation(async (_url,init)=> {
    if(init?.method==="POST") {
      const body=JSON.parse(String(init.body));writes.push({url:String(_url),body})
      row={...row,owner_user_id:body.successor_user_id,owner_name:"Existing reader",eligible_successors:[]}
      return Response.json(row)
    }
    return Response.json({pages:[row]})
  })
  render(view())
  await screen.findByText(`${label} · Owner: Departing owner`)
  expect(screen.queryByText("Could not read output ownership.")).toBeNull()
  expect(document.querySelector("iframe")).toBeNull()
  await chooseAndReview()
  expect(writes).toHaveLength(0)
  fireEvent.click(screen.getByRole("button",{name:"Confirm ownership transfer"}))
  await screen.findByText(`${label} · Owner: Existing reader`)
  expect(writes).toEqual([{url:"/api/v1/workspaces/ws/page-ownership/page-1?workspace_id=ws",body:{expected_owner_user_id:"departing",successor_user_id:"reader"}}])
})

it("keeps a mixed four-type custody list visible instead of dropping the entire response",async()=>{
  const rows=["artifact","board","document","slides"].map((type,index)=>({...initial(),id:`page-${index}`,slug:`private-${type}`,name:`Private ${type}`,type}))
  vi.mocked(apiFetch).mockResolvedValue(Response.json({pages:rows}))
  render(view())
  for(const row of rows) await screen.findByRole("link",{name:row.name})
  for(const label of ["Artifact","Board","Doc","Slides"])expect(screen.getByText(`${label} · Owner: Departing owner`)).toBeInTheDocument()
  expect(writes).toHaveLength(0)
})
