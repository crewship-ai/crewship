import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RestrictedIssues } from "../restricted-issues"

const fetchMock = vi.hoisted(() => vi.fn())
const identity = vi.hoisted(() => ({ user: "guest", workspace: "w" }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: identity.workspace, workspace: { name: "Workspace" } }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: identity.user } } }), useSessionSafe: () => ({ data: null }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useRestrictedSurfaces: () => ["issues", "account_security"] }))
afterEach(() => { cleanup(); fetchMock.mockReset(); identity.user = "guest"; identity.workspace = "w" })
const row = { id: "issue-1", identifier: "CLIENT-1", title: "Shared issue", status: "TODO", project_id: "project-1", project_name: "Client project", can_read: true, can_contribute: false }
const project = { id: "project-1", name: "Client project", can_contribute: true, intake_ready: true }
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
function show(identifier?: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const view = render(<QueryClientProvider client={client}><RestrictedIssues identifier={identifier} /></QueryClientProvider>)
  return { client, rerender: () => view.rerender(<QueryClientProvider client={client}><RestrictedIssues identifier={identifier} /></QueryClientProvider>) }
}

it("submits a literal search from the first page and can clear the filter", async () => {
  fetchMock.mockImplementation(async (path: string) => {
    if (path.includes("restricted-issue-projects")) return response([])
    const params = new URL(path, "http://fixture.test").searchParams
    if (params.get("q")) return response([{ ...row, title: "Matched request" }])
    if (params.get("offset") === "100") return response([{ ...row, title: "Second page" }])
    return response(Array.from({ length: 100 }, (_, index) => ({ ...row, id: `row-${index}`, title: `Row ${index}` })))
  })
  show()
  await screen.findByRole("link", { name: "Row 0" })
  fireEvent.click(screen.getByRole("button", { name: "Next", exact: true }))
  await screen.findByRole("link", { name: "Second page" })
  fireEvent.change(screen.getByLabelText("Search shared issues"), { target: { value: "CLIENT & %_" } })
  expect(fetchMock.mock.calls.some(([path]) => new URL(path, "http://fixture.test").searchParams.has("q"))).toBe(false)
  fireEvent.click(screen.getByRole("button", { name: "Search", exact: true }))
  await screen.findByRole("link", { name: "Matched request" })
  const searched = fetchMock.mock.calls.map(([path]) => new URL(path, "http://fixture.test")).find(url => url.searchParams.has("q"))!
  expect(searched.searchParams.get("q")).toBe("CLIENT & %_")
  expect(searched.searchParams.get("offset")).toBe("0")
  expect(screen.queryByRole("link", { name: "Second page" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Clear search" }))
  await screen.findByRole("link", { name: "Row 0" })
  expect(screen.getByLabelText("Search shared issues")).toHaveValue("")
})

it("does not present an empty search as an access denial", async () => {
  fetchMock.mockImplementation(async () => response([]))
  show()
  fireEvent.change(screen.getByLabelText("Search shared issues"), { target: { value: "missing" } })
  fireEvent.click(screen.getByRole("button", { name: "Search", exact: true }))
  await screen.findByText(/No matching shared issues/)
  expect(screen.queryByText("No issues shared here")).toBeNull()
})

it("keeps an unsubmitted new issue draft when searching the catalog", async () => {
  fetchMock.mockImplementation(async (path: string) => response(path.includes("restricted-issue-projects") ? [project] : path.includes("q=") ? [] : [row]))
  show()
  fireEvent.click(await screen.findByText("New issue"))
  fireEvent.change(screen.getByLabelText("Project", { exact: true }), { target: { value: "project-1" } })
  fireEvent.change(screen.getByLabelText("Title", { exact: true }), { target: { value: "Keep my request" } })
  fireEvent.change(screen.getByLabelText("Description", { exact: true }), { target: { value: "Keep my brief" } })
  fireEvent.change(screen.getByLabelText("Search shared issues"), { target: { value: "another request" } })
  fireEvent.click(screen.getByRole("button", { name: "Search", exact: true }))
  await screen.findByText(/No matching shared issues/)
  expect(screen.getByLabelText("Title", { exact: true })).toHaveValue("Keep my request")
  expect(screen.getByLabelText("Description", { exact: true })).toHaveValue("Keep my brief")
  expect(screen.getByLabelText("Project", { exact: true })).toHaveValue("project-1")
  expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
})

it("does not retain another account or workspace's search input", async () => {
  fetchMock.mockImplementation(async (path: string) => response(path.includes("restricted-issue-projects") ? [] : [row]))
  const { rerender } = show()
  await screen.findByRole("link", { name: "Shared issue" })
  fireEvent.change(screen.getByLabelText("Search shared issues"), { target: { value: "Client confidential term" } })
  fireEvent.click(screen.getByRole("button", { name: "Search", exact: true }))
  await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path.includes("q="))).toBe(true))
  identity.user = "second-guest"
  identity.workspace = "other-workspace"
  rerender()
  expect(screen.getByLabelText("Search shared issues")).toHaveValue("")
  await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/workspaces/other-workspace/restricted-issues?limit=100&offset=0")).toBe(true))
})

it("does not show old search results when a different search fails", async () => {
  fetchMock.mockImplementation(async (path: string) => path.includes("restricted-issue-projects") ? response([]) : path.includes("q=") ? response({}, 503) : response([row]))
  show()
  await screen.findByRole("link", { name: "Shared issue" })
  fireEvent.change(screen.getByLabelText("Search shared issues"), { target: { value: "another filter" } })
  fireEvent.click(screen.getByRole("button", { name: "Search", exact: true }))
  await screen.findByRole("alert")
  expect(screen.queryByRole("link", { name: "Shared issue" })).toBeNull()
})
it("uses filtered projections and clears data on a rejected refresh", async () => {
  let refused = false
  fetchMock.mockImplementation(async (path: string) => path.includes("restricted-issue-projects") ? response([]) : refused ? response({}, 404) : response([row]))
  show()
  await screen.findByRole("link", { name: "Shared issue" })
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/w/restricted-issues?limit=100&offset=0")
  refused = true
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
  await screen.findByText("Issues unavailable")
  expect(screen.queryByText(/this is not about your access/)).toBeNull()
  expect(screen.getByText(/Your workspace administrator decides/)).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Shared issue" })).toBeNull()
})
it("does not offer details without project read access", async () => {
  fetchMock.mockImplementation(async (path: string) => response(path.includes("restricted-issue-projects") ? [] : [{ ...row, can_read: false }]))
  show()
  await screen.findByRole("heading", { name: "Shared issue" })
  expect(screen.queryByRole("link", { name: "Shared issue" })).toBeNull()
  expect(screen.queryByText("New issue")).toBeNull()
})
it("reads the requested issue and its dedicated shared thread without mutation controls", async () => {
  fetchMock.mockImplementation(async (path: string) => response(path.endsWith("/comments") ? [{ id: "c", body: "Explicit staff reply", author: "Workspace member", created_at: "2026-10-14T00:00:00Z" }] : { ...row, description: "Current project brief" }))
  show("CLIENT-1")
  await screen.findByText("Current project brief")
  await screen.findByText("Explicit staff reply")
  expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(["/api/v1/workspaces/w/restricted-issues/CLIENT-1", "/api/v1/workspaces/w/restricted-issues/issue-1/comments"])
  expect(screen.queryByRole("textbox")).toBeNull()
  expect(screen.queryByRole("button", { name: /save|delete|start|post|move/i })).toBeNull()
})
it("keeps a rejected creation draft and posts only to its selected project", async () => {
  let fail = true
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") return fail ? response({ error: "Project intake changed" }, 409) : response({ ...row, title: "New request" }, 201)
    return response(path.includes("restricted-issue-projects") ? [project] : [])
  })
  show()
  fireEvent.click(await screen.findByText("New issue"))
  fireEvent.change(screen.getByLabelText("Project", { exact: true }), { target: { value: "project-1" } })
  fireEvent.change(screen.getByLabelText("Title", { exact: true }), { target: { value: "New request" } })
  fireEvent.change(screen.getByLabelText("Description", { exact: true }), { target: { value: "Preserved client draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Create issue" }))
  await screen.findByText("Project intake changed")
  expect(screen.getByLabelText("Title", { exact: true })).toHaveValue("New request")
  expect(screen.getByLabelText("Description", { exact: true })).toHaveValue("Preserved client draft")
  const writes = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(writes).toHaveLength(1)
  expect(JSON.parse(String(writes[0][1].body))).toEqual({ project_id: "project-1", title: "New request", description: "Preserved client draft" })
  fail = false
  fireEvent.click(screen.getByRole("button", { name: "Create issue" }))
  await waitFor(() => expect(screen.getByLabelText("Title", { exact: true })).toHaveValue(""))
})
it("explains missing intake without offering a request the server will reject", async () => {
  fetchMock.mockImplementation(async (path: string) => response(path.includes("restricted-issue-projects") ? [{ ...project, intake_ready: false }] : []))
  show()
  fireEvent.click(await screen.findByText("New issue"))
  fireEvent.change(screen.getByLabelText("Project", { exact: true }), { target: { value: "project-1" } })
  fireEvent.change(screen.getByLabelText("Title", { exact: true }), { target: { value: "Request" } })
  expect(screen.getByText(/configure this project’s issue intake crew/)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Create issue" })).toBeDisabled()
})
it("gates contribution controls and keeps a refused shared comment draft", async () => {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => init?.method === "POST" ? response({}, 404) : response(path.endsWith("/comments") ? [] : { ...row, can_contribute: true }))
  show("CLIENT-1")
  const input = await screen.findByLabelText("Comment shared with this project")
  fireEvent.change(input, { target: { value: "Preserve this question" } })
  fireEvent.click(screen.getByRole("button", { name: "Post comment" }))
  await screen.findByText(/Your project access changed/)
  expect(input).toHaveValue("Preserve this question")
  expect(screen.getByRole("button", { name: "Move to Backlog" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: /delete|start|run/i })).toBeNull()
})

it("keeps a Guest draft with its original audience after a project move", async () => {
  let current={...row,can_contribute:true}
  fetchMock.mockImplementation(async (path:string,init?:RequestInit)=>response(path.endsWith("/comments")?[]:current))
  const {client}=show("CLIENT-1")
  const input=await screen.findByLabelText("Comment shared with this project")
  fireEvent.change(input,{target:{value:"Client one draft"}})
  current={...current,project_id:"p2",project_name:"Second client"}
  await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issues"]})})
  await waitFor(()=>expect(screen.getByRole("button",{name:"Post comment"})).toBeDisabled())
  expect(input).toHaveValue("Client one draft")
  expect(screen.getByRole("alert")).toHaveTextContent("project changed")
})

it("preserves a comment draft through a transient issue refresh failure", async () => {
  let failed=false
  fetchMock.mockImplementation(async (path:string)=>path.endsWith("/comments")?response([]):failed?response({},503):response({...row,can_contribute:true}))
  const {client}=show("CLIENT-1")
  const input=await screen.findByLabelText("Comment shared with this project")
  fireEvent.change(input,{target:{value:"Keep this draft after polling"}})
  failed=true
  await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issues"]})})
  await screen.findByText(/current view and drafts are kept/)
  expect(input).toHaveValue("Keep this draft after polling")
  expect(screen.getByRole("button",{name:"Post comment"})).toBeDisabled()
  failed=false
  await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issues"]})})
  await waitFor(()=>expect(screen.getByRole("button",{name:"Post comment"})).toBeEnabled())
  expect(input).toHaveValue("Keep this draft after polling")
})

it("hides a shared detail and thread when background refresh refuses access", async () => {
  let refused=false
  fetchMock.mockImplementation(async(path:string)=>refused?response({},403):response(path.endsWith("/comments")?[{id:"c",body:"OLD_THREAD_CANARY",author:"You",created_at:"2026-10-15T00:00:00Z"}]:{...row,can_contribute:true}))
  const {client}=show("CLIENT-1")
  await screen.findByText("OLD_THREAD_CANARY")
  refused=true
  await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issues"]})})
  await waitFor(()=>expect(screen.queryByText("OLD_THREAD_CANARY")).toBeNull())
  expect(screen.queryByLabelText("Comment shared with this project")).toBeNull()
})

it("retries an uncertain submission with the same operation key and resets after success", async () => {
  let fail=true
  fetchMock.mockImplementation(async(path:string,init?:RequestInit)=>{
    if(init?.method==="POST") { if(fail) throw new Error("Connection interrupted"); return response({...row,title:"New request"},201) }
    return response(path.includes("restricted-issue-projects")?[project]:[])
  })
  show()
  fireEvent.click(await screen.findByText("New issue"))
  fireEvent.change(screen.getByLabelText("Project",{exact:true}),{target:{value:"project-1"}})
  fireEvent.change(screen.getByLabelText("Title",{exact:true}),{target:{value:"Same request"}})
  fireEvent.click(screen.getByRole("button",{name:"Create issue"}))
  await screen.findByText("Connection interrupted")
  fail=false
  fireEvent.click(screen.getByRole("button",{name:"Create issue"}))
  await waitFor(()=>expect(screen.getByLabelText("Title",{exact:true})).toHaveValue(""))
  fireEvent.change(screen.getByLabelText("Title",{exact:true}),{target:{value:"Same request"}})
  fireEvent.click(screen.getByRole("button",{name:"Create issue"}))
  await waitFor(()=>expect(screen.getByLabelText("Title",{exact:true})).toHaveValue(""))
  const writes=fetchMock.mock.calls.filter(([,init])=>init?.method==="POST")
  const keys=writes.map(([,init])=>new Headers(init.headers).get("Idempotency-Key"))
  expect(keys).toHaveLength(3)
  expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/)
  expect(keys[1]).toBe(keys[0])
  expect(keys[2]).not.toBe(keys[0])
})
