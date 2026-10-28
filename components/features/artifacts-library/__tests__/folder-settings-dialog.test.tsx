import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

vi.mock("@/lib/feature-flags", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/feature-flags")>()), artifactsSharingMock: () => true }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "u2", name: "Ann Novak" }, { id: "u3", name: "Bo Lind" }], error: false }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(() => { throw new Error("no HTTP in the mock") }), broadcastSessionExpired: vi.fn() }))

import { FolderSettingsDialog } from "../sharing/folder-settings-dialog"
import { mockSharingApi, resetSharingMock } from "../sharing/sharing-api"

const FOLDER = { slug: "design", name: "Design" }
// vitest.setup stubs localStorage, so the mock lives in memory only; reset it per test.
beforeEach(() => { resetSharingMock(); localStorage.clear() })
afterEach(cleanup)

const renderDialog = (onOpenChange = vi.fn()) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ui = (open: boolean) => <QueryClientProvider client={client}><FolderSettingsDialog workspaceId="ws1" folder={FOLDER} open={open} onOpenChange={onOpenChange} /></QueryClientProvider>
  const view = render(ui(true))
  return { reopen: () => { view.rerender(ui(false)); view.rerender(ui(true)) } }
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
