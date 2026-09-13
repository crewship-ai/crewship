/**
 * From folder <name> — what a Page inherits from its folder (#2533).
 *
 * Pinned: the card is drawn only for a filed Page; which of two things it
 * says is the server's answer to the ACL read (200: the list with names and
 * the way to Sharing; 403: the marker, the refusal, and the reader's own
 * paths); and nothing on it is a control.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, cleanup, waitFor, fireEvent } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => undefined }))

import { FolderAccessCard } from "@/components/features/pages/editor/section-access-folder"
import type { WirePageDetail } from "@/hooks/use-page-grants"

const FILED: WirePageDetail = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  owner: "crew/lookout",
  folder: { slug: "ops", name: "Ops", icon: "rocket", color: "amber" },
  panels: [],
}

const FOLDERS = {
  folders: [{ id: "f1", slug: "ops", name: "Ops", icon: "rocket", color: "amber", owner: "crew/lookout", owner_crew_name: "Lookout", page_count: 1, acl_version: 5, shared: "crew" }],
}

function json(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => body,
    text: async () => (body === null ? "" : JSON.stringify(body)),
  } as unknown as Response
}

function mount(page: WirePageDetail, routes: { acl?: Response; me?: Response } = {}) {
  const calls: string[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      calls.push(`${(init?.method ?? "GET").toUpperCase()} ${url}`)
      if (/\/page-folders\/ops\/acl\?/.test(url)) return routes.acl ?? json(403, { error: "Only a manager of Lookout or a workspace admin can see who Ops is shared with." })
      if (/\/access\/me\?/.test(url)) return routes.me ?? json(200, { paths: ["crew:lookout", "folder:ops"] })
      if (url.startsWith("/api/v1/page-folders?")) return json(200, FOLDERS)
      return json(404, { error: `unrouted ${url}` })
    }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <FolderAccessCard workspaceId="ws-1" slug={page.slug!} page={page} />
    </QueryClientProvider>,
  )
  return { calls }
}

beforeEach(cleanup)
afterEach(() => vi.unstubAllGlobals())

describe("From folder", () => {
  it("is not drawn for a Page in no folder, and reads nothing", () => {
    const { calls } = mount({ ...FILED, folder: null })
    expect(document.querySelector('[data-slot="page-folder-access"]')).toBeNull()
    expect(calls).toEqual([])
  })

  it("names the folder, lists who it is shared with, and opens Sharing — for a manager", async () => {
    mount(FILED, {
      acl: json(200, {
        acl_version: 5,
        acl: [
          { subject_type: "crew", subject_id: "c-support", label: "Support", can_read: true, can_write: false },
          { subject_type: "user", subject_id: "u-bob", label: "bob@example.com", can_read: true, can_write: true },
        ],
      }),
    })
    const card = await waitFor(() => document.querySelector('[data-slot="page-folder-access"]') as HTMLElement)
    expect(card.textContent).toContain("From folder")
    expect(card.textContent).toContain("Ops")
    await waitFor(() => expect(card.textContent).toContain("Visible to crew Support; bob@example.com can edit."))
    expect(card.querySelectorAll('[data-slot="folder-acl-readonly"] > div')).toHaveLength(2)
    expect(card.textContent).toContain("Can view")
    expect(card.textContent).toContain("Can edit")
    // Read-only: no radio, no switch, no remove.
    expect(screen.queryByRole("radio")).toBeNull()
    expect(screen.queryByRole("switch")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Open folder sharing…" }))
    const dialog = await screen.findByRole("dialog")
    await waitFor(() => expect(dialog.textContent).toContain("Sharing"))
    await waitFor(() => expect(screen.getByRole("radiogroup", { name: "Permission for Support" })).toBeTruthy())
  })

  it("says the marker, the product's refusal and the reader's own paths — for everyone else", async () => {
    mount(FILED)
    const card = await waitFor(() => document.querySelector('[data-slot="page-folder-access"]') as HTMLElement)
    await waitFor(() => expect(card.querySelector('[data-slot="folder-sharing-marker"]')?.textContent).toBe("Shared with a crew"))
    expect(card.textContent).toContain("Only a manager of Lookout or a workspace admin can see who Ops is shared with.")
    await waitFor(() =>
      expect(card.querySelector('[data-slot="own-paths"]')?.textContent).toBe("You reach it through your crew lookout and the folder ops."),
    )
    expect(screen.queryByRole("button", { name: "Open folder sharing…" })).toBeNull()
  })
})
