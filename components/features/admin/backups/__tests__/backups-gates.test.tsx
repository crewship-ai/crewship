import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// Role gates of Admin › Backups. The page is the instance administrators':
// AdminRouteGate mounts nothing for anyone else, so no backup API is called
// from here (the server enforces the same on every route). Within it, a key
// or a destination a plan uses cannot be removed: the button is off and says
// why, and a server 409 is the fallback. The table below is the contract.
//
//   role                     | Back up now | New plan | scope | Remove key/destination in use
//   instance administrator   | yes         | yes      | yes   | disabled, with the reason
//   anyone else              | none: the page is not mounted at all

const h = vi.hoisted(() => ({ admin: true as boolean | null, apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.admin }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-dess", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }), usePathname: () => "/admin/backups" }))
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: { href: string; children: React.ReactNode }) => <a href={href} {...p}>{children}</a> }))

import AdminBackupsPage from "../../../../../app/(dashboard)/admin/backups/page"
import { RecipientsList } from "../backups-keys"
import { StorageBody } from "../backups-storage"
import { destinationsFixture, settingsFixture } from "../__fixtures__/backups"
import type { BackupRecipient, OffsiteDestination } from "../backups-model"
import type { SectionCtx } from "../backups-scope"

const ctx: SectionCtx = {
  scope: "instance", selected: new Set(), workspaces: [], currentWorkspaceId: null, demo: false, go: vi.fn(), focusRun: null, focusPath: null, backUpNow: vi.fn(), newPlanSignal: 0,
}
const key = (over: Partial<BackupRecipient>): BackupRecipient => ({ id: "k1", name: "ops-2027", public_key: "age1q8xk2v9d8f3l5n0p4r6t8w0y2a4c6e8g0i2k4m3k", holder: "Pavel", created_at: "2026-01-01T00:00:00Z", ...over })
const dest = (over: Partial<OffsiteDestination> = {}): OffsiteDestination => ({
  id: "d1", name: "R2 eu-backups", kind: "s3", endpoint: "https://x", region: "auto", bucket: "b", prefix: "p", access_key_id: "a", path_style: false,
  allow_private_network: false, last_test_at: null, last_test_error: null, created_at: "2026-01-01T00:00:00Z", copies: 3, copy_bytes: 1e9, last_verified_at: null, used_by: [], ...over,
})

beforeEach(() => {
  h.admin = true
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(new Response("{}", { status: 404 }))
  window.history.replaceState(null, "", "/admin/backups?demo=1")
  Object.values(h.toast).forEach((f) => f.mockReset())
})
afterEach(cleanup)

describe("who gets the page", () => {
  it("an instance administrator gets Back up now, New plan and the scope control", async () => {
    render(<AdminBackupsPage />)
    expect(await screen.findByRole("button", { name: /^Showing:/ })).toBeInTheDocument()
    expect(screen.getAllByRole("button", { name: "Back up now" }).length).toBeGreaterThan(0)
    expect(screen.getAllByRole("button", { name: "New plan" }).length).toBeGreaterThan(0)
  })

  it("anyone else is told so on the page, with no control and no backup API call", () => {
    h.admin = false
    render(<AdminBackupsPage />)
    expect(screen.getByText(/can.t open the Admin console/i)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull()
    expect(screen.queryByRole("button", { name: /^Showing:/ })).toBeNull()
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("mounts nothing while the role is unknown", () => {
    h.admin = null
    render(<AdminBackupsPage />)
    expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull()
    expect(h.apiFetch).not.toHaveBeenCalled()
  })
})

describe("a key a plan uses cannot be removed", () => {
  it("is disabled and says which plan, in words, not only in a tooltip", () => {
    render(<RecipientsList list={[key({ used_by: ["Nightly"] }), key({ id: "k2", name: "vault-2026" })]} ctx={ctx} />)
    const inUse = screen.getByText("ops-2027", { exact: false }).closest("[data-slot=settings-row]") as HTMLElement
    expect(within(inUse).getByRole("button", { name: "Remove" })).toBeDisabled()
    expect(inUse.querySelector("[data-slot=remove-reason]")).toHaveTextContent("In use by Nightly")
    const free = screen.getByText("vault-2026", { exact: false }).closest("[data-slot=settings-row]") as HTMLElement
    expect(within(free).getByRole("button", { name: "Remove" })).toBeEnabled()
  })

  it("the last key cannot go, because every backup is encrypted", () => {
    render(<RecipientsList list={[key({})]} ctx={ctx} />)
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled()
    expect(document.querySelector("[data-slot=remove-reason]")).toHaveTextContent("every backup is encrypted")
  })

  it("a server 409 is the fallback: the dialog stays and the reason is shown", async () => {
    h.apiFetch.mockImplementation(async (_u: string, init?: RequestInit) =>
      init?.method === "DELETE" ? new Response(JSON.stringify({ error: "key is used by plan Nightly" }), { status: 409 }) : new Response("{}", { status: 404 }))
    render(<RecipientsList list={[key({}), key({ id: "k2", name: "vault-2026" })]} ctx={ctx} />)
    fireEvent.click(within(screen.getByText("ops-2027", { exact: false }).closest("[data-slot=settings-row]") as HTMLElement).getByRole("button", { name: "Remove" }))
    fireEvent.click(within(await screen.findByTestId("confirm-dialog")).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
    expect(String(h.toast.error.mock.calls[0][0])).toContain("key is used by plan Nightly")
    expect(screen.getByTestId("confirm-dialog")).toBeInTheDocument()
  })
})

describe("a destination a plan uses cannot be removed", () => {
  const body = (d: OffsiteDestination) => render(<StorageBody settings={settingsFixture()} destinations={[d]} space={null} ctx={ctx} />)

  it("is disabled and says which plan", () => {
    body(dest({ used_by: ["Weekly off-site"] }))
    const row = screen.getByText("R2 eu-backups", { exact: false }).closest("[data-slot=settings-row]") as HTMLElement
    expect(within(row).getByRole("button", { name: "Remove" })).toBeDisabled()
    expect(row).toHaveTextContent("used by Weekly off-site")
  })

  it("is removable when no plan copies there", () => {
    body(dest({}))
    const row = screen.getByText("R2 eu-backups", { exact: false }).closest("[data-slot=settings-row]") as HTMLElement
    expect(within(row).getByRole("button", { name: "Remove" })).toBeEnabled()
    void destinationsFixture
  })
})
