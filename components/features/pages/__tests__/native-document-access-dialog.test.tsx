import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { NativeDocumentAccessDialog } from "../native-document-access-dialog"
import { pageAccessKeys, normalizePageGrants } from "@/hooks/use-page-grants"
import { pagesKeys } from "@/hooks/use-pages"
import { effectiveAccessKey } from "../editor/section-access-effective"
import { pageFoldersKeys } from "@/hooks/use-page-folders"

const identity = vi.hoisted(() => ({ id: "human" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: identity.id } }, status: "authenticated" }) }))

const page = { id: "native-page", slug: "native", name: "Document", owner: "user/human", has_collaboration: true, panels: [] }
const grant = { subject_type: "user", subject: "secret@example.com", subject_id: "secret", level: "write", live: true }
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
function mount(qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })) {
  const result = render(<QueryClientProvider client={qc}><NativeDocumentAccessDialog workspaceId="ws" slug="native" onClose={vi.fn()} /></QueryClientProvider>)
  return { ...result, qc }
}
beforeEach(() => { cleanup(); identity.id = "human" })
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })

it("offers document read/write sharing without the panel producer control", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).includes("/grants") ? json({ grants: [grant] }) : String(input).includes("/access") ? json({ subjects: [] }) : json(page)))
  mount()
  await screen.findByText("secret@example.com")
  expect(screen.getByText("Owner: human")).toBeInTheDocument()
  const level = screen.getByLabelText("Level")
  expect(level).toHaveTextContent("read")
  expect(level).toHaveTextContent("write")
  expect(level).not.toHaveTextContent("produce")
  expect(screen.queryByLabelText("Panels")).not.toBeInTheDocument()
  expect(screen.getByText(/Writing and sharing require separate rights/)).toBeInTheDocument()
})

it("does not reveal a cached administrator roster while a new grants read is pending or refused", async () => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  qc.setQueryData(pageAccessKeys.grants("ws", "native"), normalizePageGrants({ grants: [grant] }))
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).includes("/grants") ? pending : json(page)))
  mount(qc)
  await screen.findByText("Checking sharing permissions…")
  expect(screen.queryByText("secret@example.com")).not.toBeInTheDocument()
  expect(screen.queryByText("Effective roster")).not.toBeInTheDocument()
  await act(async () => { finish(json({ error: "Only the owner may inspect grants." }, 403)); await pending })
  await screen.findByText("Only the owner may inspect grants.")
  expect(screen.queryByText("secret@example.com")).not.toBeInTheDocument()
  expect(screen.queryByLabelText("Subject")).not.toBeInTheDocument()
})

it("rechecks a cached Page before exposing its owner or access cards", async () => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  qc.setQueryData(pagesKeys.detail("ws", "native", "human"), { ...page, owner: "user/old-owner" })
  const fetcher = vi.fn(async () => pending)
  vi.stubGlobal("fetch", fetcher)
  mount(qc)
  await waitFor(() => expect(fetcher).toHaveBeenCalled())
  expect(screen.queryByText(/old-owner/)).not.toBeInTheDocument()
  expect(screen.queryByText("Effective roster")).not.toBeInTheDocument()
  await act(async () => { finish(json({ error: "Unavailable" }, 404)); await pending })
  await screen.findByText("Document access is unavailable.")
  expect(screen.queryByText(/old-owner/)).not.toBeInTheDocument()
})


it("does not reuse another viewer's effective-access or folder roster after a fresh Page grant succeeds", async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  qc.setQueryData(effectiveAccessKey("ws", "native"), { subjects: [{ kind: "user", id: "old", label: "effective-secret", paths: ["owner"] }], nextCursor: null })
  qc.setQueryData(pageFoldersKeys.acl("ws", "ops"), { entries: [{ subjectType: "user", subjectId: "old", label: "folder-secret", canWrite: true }] })
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/grants")) return json({ grants: [] })
    if (url.includes("/page-folders/ops/acl")) return pending
    if (url.includes("/access/me")) return json({ paths: [] })
    if (url.includes("/access")) return json({ subjects: [] })
    if (url.includes("/page-folders?")) return json({ folders: [] })
    return json({ ...page, folder: { slug: "ops", name: "Ops" } })
  }))
  mount(qc)
  await screen.findByText("Reading the folder’s permissions…")
  expect(screen.queryByText("folder-secret")).not.toBeInTheDocument()
  expect(screen.queryByText(/effective-secret/)).not.toBeInTheDocument()
  await act(async () => { finish(json({ error: "Folder manager required." }, 403)); await pending })
  await screen.findByText("Folder manager required.")
  expect(screen.queryByRole("button", { name: "Open folder sharing…" })).not.toBeInTheDocument()
  expect(screen.queryByText("folder-secret")).not.toBeInTheDocument()
})

it("refreshes the effective projection immediately after a successful revoke, without waiting for polling", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] })
  let revoked = false, effectiveReads = 0
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === "DELETE") { revoked = true; return json({ grants: [] }) }
    if (url.includes("/grants")) return json({ grants: revoked ? [] : [grant] })
    if (url.includes("/access")) {
      effectiveReads++
      return json({ subjects: revoked ? [] : [{ subject_type: "user", subject_id: "secret", label: "effective-person", paths: ["grant:write"] }] })
    }
    return json(page)
  }))
  mount()
  await screen.findByText(/effective-person/)
  fireEvent.click(screen.getByLabelText("Revoke write from user/secret@example.com"))
  fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Revoke" }))
  await waitFor(() => expect(revoked).toBe(true))
  await waitFor(() => expect(effectiveReads).toBeGreaterThan(1))
  await waitFor(() => expect(screen.queryByText(/effective-person/)).not.toBeInTheDocument())
})

it("discards the previous human's access view when the authenticated human changes", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/grants")) return identity.id === "human" ? json({ grants: [grant] }) : json({ error: "Owner required." }, 403)
    if (url.includes("/access")) return json({ subjects: [] })
    return json(page)
  }))
  const view = mount()
  await screen.findByText("secret@example.com")
  identity.id = "reader"
  view.rerender(<QueryClientProvider client={view.qc}><NativeDocumentAccessDialog workspaceId="ws" slug="native" onClose={vi.fn()} /></QueryClientProvider>)
  expect(screen.queryByText("secret@example.com")).not.toBeInTheDocument()
  await screen.findByText("Owner required.")
  expect(screen.queryByText("secret@example.com")).not.toBeInTheDocument()
})

it("returns keyboard focus to the sharing opener when the dialog closes", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).includes("/grants") ? json({ grants: [] }) : String(input).includes("/access") ? json({ subjects: [] }) : json(page)))
  function Surface() {
    const [open, setOpen] = React.useState(false)
    const [trigger, setTrigger] = React.useState<HTMLButtonElement | null>(null)
    return <><button ref={setTrigger} onClick={() => setOpen(true)}>Open access</button>{open && <NativeDocumentAccessDialog workspaceId="ws" slug="native" onClose={() => setOpen(false)} returnFocus={trigger} />}</>
  }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={qc}><Surface /></QueryClientProvider>)
  const opener = screen.getByRole("button", { name: "Open access" })
  fireEvent.click(opener)
  await screen.findByRole("dialog")
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  await waitFor(() => expect(document.activeElement).toBe(opener))
})


for (const status of [403, 500]) it(`removes an already displayed folder roster and nested sharing when its next read returns ${status}`, async () => {
  let refused = false
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/grants")) return json({ grants: [] })
    if (url.includes("/page-folders/ops/acl")) return refused
      ? json({ error: "Folder ACL is unavailable." }, status)
      : json({ acl: [{ subject_type: "user", subject_id: "member", label: "folder-person", can_read: true, can_write: true }], acl_version: 1 })
    if (url.includes("/page-folders?")) return json({ folders: [{ id: "ops-id", slug: "ops", name: "Ops", owner: "crew/lookout", shared: "crews", acl_version: 1, page_count: 1 }] })
    if (url.includes("/access/me")) return json({ paths: [] })
    if (url.includes("/access")) return json({ subjects: [] })
    return json({ ...page, folder: { slug: "ops", name: "Ops" } })
  }))
  mount()
  await screen.findByText("folder-person")
  fireEvent.click(screen.getByRole("button", { name: "Open folder sharing…" }))
  await waitFor(() => expect(screen.getAllByRole("dialog").length).toBeGreaterThan(1))
  refused = true
  await screen.findByText("Folder ACL is unavailable.", {}, { timeout: 3000 })
  expect(screen.queryByText("folder-person")).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Open folder sharing…" })).not.toBeInTheDocument()
  expect(screen.getAllByRole("dialog")).toHaveLength(1)
})
