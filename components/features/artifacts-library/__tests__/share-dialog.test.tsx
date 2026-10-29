import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { LibraryItem } from "../library-items"

const flags = vi.hoisted(() => ({ mock: true, fetch: vi.fn() }))
vi.mock("@/lib/feature-flags", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/feature-flags")>()), artifactsSharingMock: () => flags.mock }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: flags.fetch }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "me" } }, status: "authenticated" }) }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "me", name: "Me Myself" }, { id: "u2", name: "Ada Lovelace" }, { id: "u3", name: "Alan Turing" }], error: false }) }))

import { mockSharingApi, resetSharingMock } from "../sharing/sharing-api"
import { ShareDialog } from "../sharing/share-dialog"

const item = (over: Partial<LibraryItem> = {}): LibraryItem => ({
  slug: "brief", name: "Partner brief", kind: "word", family: "document", sharedVersion: 1, native: true, projectBacked: false,
  empty: false, private: false, ownerLabel: "You", folder: null, updatedAt: null, ...over,
})

beforeEach(() => { resetSharingMock(); localStorage.clear(); flags.mock = true; flags.fetch.mockReset() })
afterEach(cleanup)

const renderDialog = (it: LibraryItem = item()) => render(<QueryClientProvider client={new QueryClient()}>
  <ShareDialog workspaceId="ws1" item={it} open onOpenChange={() => {}} />
</QueryClientProvider>)

const list = () => screen.getByRole("region", { name: "People with access" })
const row = (label: string) => within(list()).getByText(label).closest("li") as HTMLElement

it("titles the item and lists the owner and workspace read for a non-private item", async () => {
  renderDialog()
  expect(screen.getByRole("heading", { name: "Share “Partner brief”" })).toBeTruthy()
  expect(screen.getByRole("status").textContent).toMatch(/Loading/)
  await within(list()).findByText("Everyone in this workspace")
  const owner = row("You")
  expect(within(owner).getByText("Owner")).toBeTruthy()
  expect(within(owner).getByText("Edit")).toBeTruthy()
  expect(within(owner).queryByRole("button", { name: /Remove/ })).toBeNull()
  const everyone = row("Everyone in this workspace")
  expect(within(everyone).getByText("Shared directly")).toBeTruthy()
  expect((within(everyone).getByRole("combobox") as HTMLSelectElement).value).toBe("read")
  expect(screen.getByText("Only people in this workspace. No public links.")).toBeTruthy()
})

it("a private item lists only its owner", async () => {
  renderDialog(item({ private: true }))
  await within(list()).findByText("You")
  expect(within(list()).getAllByRole("listitem")).toHaveLength(1)
})

it("adds a person with Comment, then changes the level and removes them", async () => {
  renderDialog()
  await within(list()).findByText("Everyone in this workspace")
  fireEvent.change(screen.getByRole("combobox", { name: "Share with" }), { target: { value: "user:u2" } })
  fireEvent.change(screen.getByRole("combobox", { name: "Level" }), { target: { value: "comment" } })
  expect(screen.getByText(/Comment: Opens the shared version and leaves comments/)).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Share" }))
  expect(await screen.findByRole("button", { name: "Sharing…" })).toBeTruthy()
  await within(list()).findByText("Ada Lovelace")
  const select = () => within(row("Ada Lovelace")).getByRole("combobox", { name: "Level for Ada Lovelace" }) as HTMLSelectElement
  expect(select().value).toBe("comment")
  expect((screen.getByRole("combobox", { name: "Share with" }) as HTMLSelectElement).value).toBe("")

  fireEvent.change(select(), { target: { value: "write" } })
  await waitFor(() => expect(row("Ada Lovelace").getAttribute("aria-busy")).toBeNull())
  await waitFor(() => expect(select().value).toBe("write"))
  const stored = await act(() => mockSharingApi("ws1").access(item()))
  expect(stored).toContainEqual(expect.objectContaining({ subjectId: "u2", level: "write", via: "grant" }))

  fireEvent.click(screen.getByRole("button", { name: "Remove Ada Lovelace" }))
  await waitFor(() => expect(within(list()).queryByText("Ada Lovelace")).toBeNull())
  expect(within(list()).getByText("Everyone in this workspace")).toBeTruthy()
})

it("shows folder rows read-only with where to change them", async () => {
  const folder = { slug: "fin", name: "Finance", icon: null, color: null }
  await mockSharingApi("ws1").saveFolderSettings("fin", { defaultAudience: "folder", entries: [{ subjectType: "user", subjectId: "u3", label: "Alan Turing", level: "write", via: "folder" }] })
  renderDialog(item({ folder }))
  await within(list()).findByText("Alan Turing")
  const folderRow = row("Alan Turing")
  expect(within(folderRow).getByText("Via folder Finance · Change in folder settings")).toBeTruthy()
  expect(within(folderRow).getByText("Edit")).toBeTruthy()
  expect(within(folderRow).queryByRole("combobox")).toBeNull()
  expect(within(folderRow).queryByRole("button")).toBeNull()
})

const perms = (read: boolean, comment = false, edit = false) => ({ read, comment, edit, produce: false })
const OWNER = { subject_type: "user", subject_id: "me", label: "You", paths: ["owner"], effective_permissions: perms(true, true, true), sources: [{ path: "owner", inherited: false, permissions: perms(true, true, true) }] }
/** GET …/access and …/grants as #4444 serves them; writes answer with the grants envelope. */
const serve = (grants: unknown[], write?: (method: string, url: string) => Response | undefined) => flags.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
  const method = init?.method ?? "GET"
  const answer = method === "GET" ? undefined : write?.(method, url)
  if (answer) return answer
  return new Response(JSON.stringify(url.includes("/grants") ? { page: "brief", grants } : { page: "brief", page_id: "p1", pages_version: 3, subjects: [OWNER] }), { status: 200 })
})

it("shows the server's message when sharing fails", async () => {
  flags.mock = false
  serve([], method => method === "PUT" ? new Response(JSON.stringify({ error: "Only the owner can share this artifact." }), { status: 403 }) : undefined)
  renderDialog()
  await within(list()).findByText("You")
  fireEvent.change(screen.getByRole("combobox", { name: "Share with" }), { target: { value: "workspace:" } })
  fireEvent.click(screen.getByRole("button", { name: "Share" }))
  expect((await screen.findByRole("alert")).textContent).toBe("Only the owner can share this artifact.")
  expect(JSON.parse(flags.fetch.mock.calls.find(([, init]) => init?.method === "PUT")![1].body)).toEqual({ subject_type: "workspace", level: "read" })
})

it("offers no expiry, guest or link sharing", async () => {
  renderDialog()
  await within(list()).findByText("Everyone in this workspace")
  const dialog = screen.getByRole("dialog")
  expect(within(dialog).queryByText(/expir/i)).toBeNull()
  expect(within(dialog).queryByLabelText(/expir/i)).toBeNull()
  expect(within(dialog).queryAllByRole("textbox").filter(box => /guest|link|email/i.test(`${box.getAttribute("aria-label")} ${box.getAttribute("placeholder")}`))).toEqual([])
  expect(within(dialog).queryAllByRole("textbox")).toEqual([])
  const subjects = [...(screen.getByRole("combobox", { name: "Share with" }) as HTMLSelectElement).options].map(o => o.textContent)
  expect(subjects).toEqual(["Add a person…", "Everyone in this workspace", "Ada Lovelace", "Alan Turing"])
  expect([...(screen.getByRole("combobox", { name: "Level" }) as HTMLSelectElement).options].map(o => o.textContent)).toEqual(["Edit", "Comment", "Read"])
})

it("on the server, a downgrade from Edit to Read removes Edit, by query", async () => {
  flags.mock = false
  serve([{ subject_type: "user", subject: "ada@example.test", subject_id: "u2", level: "write", live: true }])
  renderDialog()
  await within(list()).findByText("ada@example.test")
  fireEvent.change(within(row("ada@example.test")).getByRole("combobox"), { target: { value: "read" } })
  await waitFor(() => expect(flags.fetch.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(true))
  const writes = flags.fetch.mock.calls.filter(([, init]) => init?.method && init.method !== "GET")
  expect(writes.map(([url, init]) => [init.method, init.body ? JSON.parse(init.body) : new URL(url, "http://h").searchParams.toString()])).toEqual([
    ["PUT", { subject_type: "user", subject: "u2", level: "read" }],
    ["DELETE", "workspace_id=ws1&subject_type=user&subject=u2&level=write"],
  ])
})

it("never offers the signed-in person as someone to share with, and names their own row You", async () => {
  renderDialog()
  await within(list()).findByText("Everyone in this workspace")
  const options = Array.from((screen.getByLabelText("Share with", { exact: true }) as HTMLSelectElement).options).map(o => o.textContent)
  expect(options).toContain("Ada Lovelace")
  expect(options).not.toContain("Me Myself")
})
