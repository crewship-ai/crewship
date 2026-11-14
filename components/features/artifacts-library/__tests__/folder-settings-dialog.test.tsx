import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const flags = vi.hoisted(() => ({ mock: true, fetch: vi.fn((): Promise<Response> => { throw new Error("no HTTP in the mock") }) }))
vi.mock("@/lib/feature-flags", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/feature-flags")>()), artifactsSharingMock: () => flags.mock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "me" } }, status: "authenticated" }) }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "u2", name: "Ann Novak" }, { id: "u3", name: "Bo Lind" }], error: false }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: flags.fetch, broadcastSessionExpired: vi.fn() }))

import { FolderSettingsDialog } from "../sharing/folder-settings-dialog"
import { mockSharingApi, resetSharingMock } from "../sharing/sharing-api"

const FOLDER = { slug: "design", name: "Design" }
// vitest.setup stubs localStorage, so the mock lives in memory only; reset it per test.
beforeEach(() => { resetSharingMock(); localStorage.clear(); flags.mock = true })
afterEach(cleanup)

const renderDialog = (onOpenChange = vi.fn()) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ui = (open: boolean) => <QueryClientProvider client={client}><FolderSettingsDialog workspaceId="ws1" folder={FOLDER} open={open} onOpenChange={onOpenChange} /></QueryClientProvider>
  const view = render(ui(true))
  return { reopen: () => { view.rerender(ui(false)); view.rerender(ui(true)) }, unmount: view.unmount }
}
const change = (label: string, value: string) => fireEvent.change(screen.getByRole("combobox", { name: label }), { target: { value } })

it("saves audience and entries, and a fresh read shows them again", async () => {
  const onOpenChange = vi.fn()
  const { reopen } = renderDialog(onOpenChange)
  await screen.findByRole("radio", { name: /Shared like the folder/ })
  expect((screen.getByRole("radio", { name: /Shared like the folder/ }) as HTMLInputElement).checked).toBe(true)
  expect(screen.getByText("0 entries · new artifacts shared like the folder")).toBeTruthy()

  fireEvent.click(screen.getByRole("radio", { name: /Private to the creator/ }))
  change("Add person or everyone", "user:u2"); change("Level to add", "comment")
  fireEvent.click(screen.getByRole("button", { name: "Add" }))
  change("Add person or everyone", "workspace:")
  fireEvent.click(screen.getByRole("button", { name: "Add" }))
  change("Level for Everyone in this workspace", "write")
  change("Add person or everyone", "user:u3")
  fireEvent.click(screen.getByRole("button", { name: "Add" }))
  fireEvent.click(screen.getByRole("button", { name: "Remove Bo Lind" }))
  expect(screen.getByText("2 entries · new artifacts private")).toBeTruthy()

  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  expect(await mockSharingApi("ws1").folderSettings("design")).toMatchObject({
    defaultAudience: "private",
    entries: [{ subjectType: "user", subjectId: "u2", label: "Ann Novak", level: "comment" }, { subjectType: "workspace", subjectId: "", level: "write" }],
  })

  // Closing and opening reads again; then a fresh page with an empty query cache does too.
  reopen()
  await screen.findByRole("list", { name: "Folder access" })
  cleanup()
  renderDialog()
  const list = await screen.findByRole("list", { name: "Folder access" })
  expect(within(list).getAllByRole("listitem").map(li => li.textContent)).toEqual([expect.stringContaining("Ann Novak"), expect.stringContaining("Everyone in this workspace")])
  expect((screen.getByRole("combobox", { name: "Level for Ann Novak" }) as HTMLSelectElement).value).toBe("comment")
  expect((screen.getByRole("combobox", { name: "Level for Everyone in this workspace" }) as HTMLSelectElement).value).toBe("write")
  expect((screen.getByRole("radio", { name: /Private to the creator/ }) as HTMLInputElement).checked).toBe(true)
})

it("Cancel leaves the saved settings alone", async () => {
  const onOpenChange = vi.fn()
  renderDialog(onOpenChange)
  await screen.findByRole("radio", { name: /Private to the creator/ })
  fireEvent.click(screen.getByRole("radio", { name: /Private to the creator/ }))
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(onOpenChange).toHaveBeenCalledWith(false)
  expect(await mockSharingApi("ws1").folderSettings("design")).toMatchObject({ defaultAudience: "folder", entries: [] })
})

it("on the server, offers only what #4444 serves: Read or Edit, no default audience, saved entry by entry", async () => {
  flags.mock = false
  const acl = { folder: "design", acl_version: 2, acl: [{ subject_type: "user", subject_id: "u2", label: "ann@example.test", can_read: true, can_write: false }] }
  flags.fetch.mockImplementation(async () => new Response(JSON.stringify(acl), { status: 200 }))
  const onOpenChange = vi.fn()
  renderDialog(onOpenChange)
  await screen.findByRole("list", { name: "Folder access" })
  expect(screen.queryByRole("radio")).toBeNull()
  expect([...(screen.getByRole("combobox", { name: "Level for ann@example.test" }) as HTMLSelectElement).options].map(o => o.textContent)).toEqual(["Read", "Edit"])
  expect(screen.getByText(/Give Comment on each artifact/)).toBeTruthy()
  expect(screen.getByText(/Each change is saved on its own/)).toBeTruthy()
  change("Level for ann@example.test", "write")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  const writes = flags.fetch.mock.calls.map(call => call as unknown as [string, RequestInit | undefined]).filter(([, init]) => init?.method === "PUT")
  expect(writes.map(([url, init]) => [new URL(url, "http://h").pathname, JSON.parse(String(init!.body))])).toEqual([
    ["/api/v1/page-folders/design/acl", { subject_type: "user", subject_id: "u2", can_write: true }],
  ])
})

it("returns focus to the control that opened it once the library unmounts the dialog", async () => {
  const opener = document.createElement("button")
  opener.textContent = "Folder settings"
  document.body.append(opener)
  opener.focus()
  const { unmount } = renderDialog()
  expect(document.activeElement).not.toBe(opener)
  unmount()
  await waitFor(() => expect(document.activeElement).toBe(opener))
  opener.remove()
})

// #5317 PR5 (Harbor 2 v92): one row for the agents of the folder's crew, at
// most Viewer (Can suggest is hidden until a folder can give a proposal,
// #5317 step 5.2), saved against the version that was read.
it("on the server, sets the crew's agents to Viewer against the version it read", async () => {
  flags.mock = false
  const acl = { folder: "design", acl_version: 4, acl: [], agent_access: "none", agent_access_available: true }
  flags.fetch.mockImplementation(async () => new Response(JSON.stringify(acl), { status: 200 }))
  const onOpenChange = vi.fn()
  renderDialog(onOpenChange)
  const select = await screen.findByRole("combobox", { name: "Agents of this folder's crew" }) as HTMLSelectElement
  expect([...select.options].map(o => o.textContent)).toEqual(["No access", "Viewer"])
  expect(screen.getByText(/never edit through a folder/)).toBeTruthy()
  change("Agents of this folder's crew", "read")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  const writes = flags.fetch.mock.calls.map(call => call as unknown as [string, RequestInit | undefined]).filter(([, init]) => init?.method === "PUT")
  expect(writes.map(([url, init]) => [new URL(url, "http://h").pathname, JSON.parse(String(init!.body))])).toEqual([
    ["/api/v1/page-folders/design/agent-access", { level: "read", acl_version: 4 }],
  ])
})

it("on the server, shows a propose level set elsewhere as Viewer access without offering it", async () => {
  flags.mock = false
  const acl = { folder: "design", acl_version: 4, acl: [], agent_access: "propose", agent_access_available: true }
  flags.fetch.mockImplementation(async () => new Response(JSON.stringify(acl), { status: 200 }))
  renderDialog()
  const select = await screen.findByRole("combobox", { name: "Agents of this folder's crew" }) as HTMLSelectElement
  expect(select.value).toBe("propose")
  expect([...select.options].map(o => [o.textContent, o.disabled])).toEqual([["No access", false], ["Viewer", false], ["Viewer (set as Can suggest)", true]])
})

it("on the server, hides the agents row where it would not take effect", async () => {
  flags.mock = false
  const acl = { folder: "design", acl_version: 4, acl: [], agent_access: "none", agent_access_available: false }
  flags.fetch.mockImplementation(async () => new Response(JSON.stringify(acl), { status: 200 }))
  renderDialog()
  await screen.findByRole("button", { name: "Save" })
  expect(screen.queryByRole("combobox", { name: "Agents of this folder's crew" })).toBeNull()
})

it("on the server, a retry after a failed entry save does not resend the agents level on the old version", async () => {
  flags.mock = false
  let version = 4
  let failEntry = true
  flags.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://h").pathname
    if (init?.method === "PUT" && path.endsWith("/agent-access")) {
      const body = JSON.parse(String(init.body))
      if (body.acl_version !== version) return new Response(JSON.stringify({ error: "conflict: acl_version" }), { status: 409 })
      version++
      return new Response(JSON.stringify({ folder: "design", agent_access: body.level, acl_version: version, agent_access_available: true }), { status: 200 })
    }
    if (init?.method === "PUT") {
      if (failEntry) { failEntry = false; return new Response(JSON.stringify({ error: "try again" }), { status: 500 }) }
      version++
      return new Response("{}", { status: 200 })
    }
    return new Response(JSON.stringify({ folder: "design", acl_version: version, acl: [], agent_access: "none", agent_access_available: true }), { status: 200 })
  })
  const onOpenChange = vi.fn()
  renderDialog(onOpenChange)
  await screen.findByRole("combobox", { name: "Agents of this folder's crew" })
  change("Agents of this folder's crew", "read")
  change("Add person or everyone", "user:u2"); change("Level to add", "read")
  fireEvent.click(screen.getByRole("button", { name: "Add" }))
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByText(/try again/)
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  const agentPuts = flags.fetch.mock.calls.map(call => call as unknown as [string, RequestInit | undefined])
    .filter(([url, init]) => init?.method === "PUT" && url.includes("/agent-access"))
  expect(agentPuts).toHaveLength(1)
})

// #5343: a partial save moves acl_version through this form's own entry writes.
// Changing the agents level afterwards must save against the current version
// when only this form moved it, and refuse when someone else changed the row.
function serverWithPartialEntrySave(opts: { otherChangesAgentsTo?: "read" | "none" | "propose" } = {}) {
  let version = 4
  let agentLevel = "none"
  let entryPuts = 0
  flags.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://h").pathname
    if (init?.method === "PUT" && path.endsWith("/agent-access")) {
      const body = JSON.parse(String(init.body))
      if (body.acl_version !== version) return new Response(JSON.stringify({ error: "folder changed since acl_version; read its permissions again" }), { status: 409 })
      version++
      agentLevel = body.level
      return new Response(JSON.stringify({ folder: "design", agent_access: body.level, acl_version: version, agent_access_available: true }), { status: 200 })
    }
    if (init?.method === "PUT") {
      entryPuts++
      // The first entry lands, the second fails: a partial save.
      if (entryPuts === 2) {
        if (opts.otherChangesAgentsTo) { agentLevel = opts.otherChangesAgentsTo; version++ }
        return new Response(JSON.stringify({ error: "try again" }), { status: 500 })
      }
      version++
      return new Response("{}", { status: 200 })
    }
    return new Response(JSON.stringify({ folder: "design", acl_version: version, acl: [], agent_access: agentLevel, agent_access_available: true }), { status: 200 })
  })
  return { agentPuts: () => flags.fetch.mock.calls.map(call => call as unknown as [string, RequestInit | undefined])
    .filter(([url, init]) => init?.method === "PUT" && url.includes("/agent-access")).map(([, init]) => JSON.parse(String(init!.body))) }
}

async function partialSaveThenChangeAgents(next: string) {
  const onOpenChange = vi.fn()
  renderDialog(onOpenChange)
  await screen.findByRole("combobox", { name: "Agents of this folder's crew" })
  change("Agents of this folder's crew", "read")
  change("Add person or everyone", "user:u2"); change("Level to add", "read")
  fireEvent.click(screen.getByRole("button", { name: "Add" }))
  change("Add person or everyone", "user:u3"); change("Level to add", "read")
  fireEvent.click(screen.getByRole("button", { name: "Add" }))
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByText(/try again/)
  change("Agents of this folder's crew", next)
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  return onOpenChange
}

it("on the server, changing the agents level after a partial save saves against the current version (#5343)", async () => {
  flags.mock = false
  const server = serverWithPartialEntrySave()
  const onOpenChange = await partialSaveThenChangeAgents("none")
  await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  // The first try fences on the version this form saved (5) and is refused; the retry uses the current one.
  expect(server.agentPuts()).toEqual([{ level: "read", acl_version: 4 }, { level: "none", acl_version: 5 }, { level: "none", acl_version: 6 }])
  expect(screen.queryByText(/changed since/)).toBeNull()
})

it("on the server, an agents level changed by someone else is not overwritten after a partial save (#5343)", async () => {
  flags.mock = false
  const server = serverWithPartialEntrySave({ otherChangesAgentsTo: "propose" })
  const onOpenChange = await partialSaveThenChangeAgents("none")
  await screen.findByText(/changed by someone else and is now Can suggest/)
  expect(onOpenChange).not.toHaveBeenCalledWith(false)
  expect(server.agentPuts().filter(b => b.level === "none")).toHaveLength(1) // the refused attempt only
  expect((screen.getByRole("combobox", { name: "Agents of this folder's crew" }) as HTMLSelectElement).value).toBe("propose")
})

function agentPutServer(agentPut: (body: { level: string; acl_version: number }, n: number) => Response, get: () => Record<string, unknown>) {
  let n = 0
  flags.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://h").pathname
    if (init?.method === "PUT" && path.endsWith("/agent-access")) return agentPut(JSON.parse(String(init.body)), ++n)
    if (init?.method === "PUT") return new Response("{}", { status: 200 })
    return new Response(JSON.stringify({ folder: "design", acl: [], ...get() }), { status: 200 })
  })
  const calls = () => flags.fetch.mock.calls.map(call => call as unknown as [string, RequestInit | undefined])
  return {
    agentPuts: () => calls().filter(([url, init]) => init?.method === "PUT" && url.includes("/agent-access")).length,
    reads: () => calls().filter(([url, init]) => (init?.method ?? "GET") === "GET" && new URL(url, "http://h").pathname.endsWith("/acl")).length,
  }
}

async function saveAgentsAs(level: string) {
  const onOpenChange = vi.fn()
  renderDialog(onOpenChange)
  await screen.findByRole("combobox", { name: "Agents of this folder's crew" })
  change("Agents of this folder's crew", level)
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  return onOpenChange
}

it("on the server, a 409 because the flag is off shows the server's reason, not a concurrent change (#5343)", async () => {
  flags.mock = false
  let available = true
  agentPutServer(() => { available = false; return new Response(JSON.stringify({ error: "agent access modes are off in this workspace", code: "artifact_agent_access_modes_off" }), { status: 409 }) },
    () => ({ acl_version: 4, agent_access: "none", agent_access_available: available }))
  await saveAgentsAs("read")
  await screen.findByText(/agent access modes are off in this workspace/)
  expect(screen.queryByText(/changed by someone else/)).toBeNull()
})

it("on the server, an error other than 409 passes through without a re-read or retry (#5343)", async () => {
  flags.mock = false
  const server = agentPutServer(() => new Response(JSON.stringify({ error: "server is unhappy" }), { status: 500 }),
    () => ({ acl_version: 4, agent_access: "none", agent_access_available: true }))
  await saveAgentsAs("read")
  await screen.findByText(/server is unhappy/)
  const readsAfterOpen = server.reads()
  expect(server.agentPuts()).toBe(1)
  await new Promise(r => setTimeout(r, 50))
  expect(server.reads()).toBe(readsAfterOpen)
})

it("on the server, retries at most once: a 409 on the retry is shown (#5343)", async () => {
  flags.mock = false
  let version = 4
  const server = agentPutServer(() => { version++; return new Response(JSON.stringify({ error: "folder changed since acl_version; read its permissions again" }), { status: 409 }) },
    () => ({ acl_version: version, agent_access: "none", agent_access_available: true }))
  const onOpenChange = await saveAgentsAs("read")
  await screen.findByText(/folder changed since acl_version/)
  expect(server.agentPuts()).toBe(2)
  expect(onOpenChange).not.toHaveBeenCalledWith(false)
})
