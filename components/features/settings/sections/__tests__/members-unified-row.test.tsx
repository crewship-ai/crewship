import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { MembersSection } from "../members-section"

/**
 * #1517 — Settings → Members used to render the roster and a per-member
 * capability grid as two separate lists of the same people. They are one
 * list now: a row per person that expands to that person's role and grants.
 *
 * What is worth pinning here is the behaviour the merge introduced or moved,
 * not the layout:
 *
 *   - expanding a row shows THAT person's capabilities (the whole point of
 *     keying the disclosure to the identity),
 *   - the collapsed row still says something about capabilities, so the list
 *     is scannable without eight expansions,
 *   - a capability toggle and a role change both still save, and both still
 *     surface a server refusal. `apiFetch` resolves on 4xx/5xx rather than
 *     throwing, so a bare `await` reads as success — these tests fail if
 *     anyone drops the `res.ok` check.
 */

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

// vi.mock is hoisted above the module body, so the stub has to be too.
const toast = vi.hoisted(() => ({
  success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn(),
}))
vi.mock("sonner", () => ({ toast }))

vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))

// Radix Select drives open/close through pointer-capture APIs happy-dom does
// not implement; polyfill them so the role dropdown can open.
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  // @ts-expect-error happy-dom lacks these pointer-capture stubs
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  // @ts-expect-error polyfill
  Element.prototype.setPointerCapture = vi.fn()
  // @ts-expect-error polyfill
  Element.prototype.releasePointerCapture = vi.fn()
})

function openSelect(trigger: HTMLElement) {
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
  fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

const members = [
  {
    id: "m-owner",
    role: "OWNER",
    created_at: new Date().toISOString(),
    user: { id: "u-owner", email: "olive@x.io", full_name: "Olive Owner", avatar_url: null },
  },
  {
    id: "m-mel",
    role: "MEMBER",
    created_at: new Date().toISOString(),
    user: { id: "u-mel", email: "mel@x.io", full_name: "Mel Member", avatar_url: null },
  },
  {
    id: "m-vic",
    role: "VIEWER",
    created_at: new Date().toISOString(),
    user: { id: "u-vic", email: "vic@x.io", full_name: "Vic Viewer", avatar_url: null },
  },
]

/** Mel has issue.create; Vic has nothing beyond the implied chat. */
const bulkBody = {
  members: [
    { user_id: "u-mel", role: "MEMBER", capabilities: ["chat", "issue.create"] },
    { user_id: "u-vic", role: "VIEWER", capabilities: ["chat"] },
  ],
}

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MembersSection
        members={members}
        workspaceId="ws1"
        currentUserId="u-caller"
        callerRole="ADMIN"
        onRefresh={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

function expand(name: RegExp) {
  fireEvent.click(screen.getByRole("button", { name }))
}

beforeEach(() => {
  cleanup()
  apiFetch.mockReset()
  toast.error.mockReset()
  toast.success.mockReset()
  // Default: the bulk capabilities read succeeds. Individual tests append
  // one-shot responses for the PATCH they are about.
  apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => bulkBody })
})

describe("MembersSection — expanding a row", () => {
  it("shows nobody's capabilities until a row is expanded", async () => {
    renderSection()
    // The pip summary confirms the data has loaded; the toggles must not
    // have rendered with it.
    await screen.findByLabelText(/Mel Member: chat, issue.create/i)
    expect(screen.queryAllByRole("switch")).toHaveLength(0)
  })

  it("shows exactly one person's capabilities — the expanded one", async () => {
    renderSection()
    await screen.findByLabelText(/Mel Member: chat, issue.create/i)

    expand(/expand permissions for Mel Member/i)

    // Eight capabilities for Mel and nobody else. The old grid rendered
    // 8 × every member at once; the row keys the disclosure to the identity.
    expect(screen.getAllByRole("switch")).toHaveLength(8)
    expect(screen.getByRole("switch", { name: /Revoke issue.create from Mel Member/i })).toBeTruthy()
    expect(screen.queryByRole("switch", { name: /Vic Viewer/i })).toBeNull()
  })

  it("reflects that person's own grants, not the previous row's", async () => {
    renderSection()
    await screen.findByLabelText(/Vic Viewer: chat/i)

    expand(/expand permissions for Vic Viewer/i)
    // Vic has only the implied chat, so issue.create reads as a grant offer.
    expect(screen.getByRole("switch", { name: /Grant issue.create to Vic Viewer/i })).toBeTruthy()
  })

  it("keeps chat and the whole OWNER row un-toggleable", async () => {
    renderSection()
    await screen.findByLabelText(/Mel Member: chat, issue.create/i)

    expand(/expand permissions for Mel Member/i)
    expect(screen.getByRole("switch", { name: /Chat is always granted/i })).toBeDisabled()

    expand(/expand permissions for Olive Owner/i)
    expect(
      screen.getByRole("switch", { name: /OWNER capabilities are immutable: issue.create/i }),
    ).toBeDisabled()
  })

  it("summarises capabilities in the collapsed row so the list stays scannable", async () => {
    renderSection()
    // A pip per capability, in a fixed column, marked granted or not — this
    // is what survives of the table's one real strength (comparing a single
    // capability across people) without the table.
    const pips = await screen.findByLabelText(/Mel Member: chat, issue.create/i)
    const granted = pips.querySelectorAll('[data-granted="true"]')
    expect(pips.querySelectorAll("[data-capability]")).toHaveLength(8)
    expect(granted).toHaveLength(2)
    expect(
      Array.from(granted).map((n) => n.getAttribute("data-capability")),
    ).toEqual(["chat", "issue.create"])
  })
})

describe("MembersSection — saving a capability", () => {
  it("PATCHes the grant for the expanded member", async () => {
    renderSection()
    await screen.findByLabelText(/Mel Member: chat, issue.create/i)
    expand(/expand permissions for Mel Member/i)

    apiFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ user_id: "u-mel", role: "MEMBER", capabilities: ["chat", "issue.create", "memory.write"] }),
    })
    fireEvent.click(screen.getByRole("switch", { name: /Grant memory.write to Mel Member/i }))

    await waitFor(() => {
      const patch = apiFetch.mock.calls.find(([, init]) => init?.method === "PATCH")
      expect(patch).toBeTruthy()
      expect(patch![0]).toContain("/members/u-mel/capabilities")
      expect(JSON.parse(patch![1].body)).toEqual({ grant: ["memory.write"] })
    })
    // The server's canonical set lands back in the row.
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: /Revoke memory.write from Mel Member/i })).toBeTruthy(),
    )
  })

  it("surfaces a server refusal instead of reporting success", async () => {
    // apiFetch RESOLVES on 403 — it does not throw. Without the res.ok check
    // the optimistic flip would stand and the admin would believe the grant
    // landed.
    renderSection()
    await screen.findByLabelText(/Mel Member: chat, issue.create/i)
    expand(/expand permissions for Mel Member/i)

    apiFetch.mockResolvedValueOnce({
      ok: false,
      status: 403,
      text: async () => "forbidden: caller lost admin",
      json: async () => ({}),
    })
    fireEvent.click(screen.getByRole("switch", { name: /Grant memory.write to Mel Member/i }))

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(String(toast.error.mock.calls[0][0])).toMatch(/permission denied/i)
    // ...and the optimistic flip is rolled back, so the row does not lie.
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: /Grant memory.write to Mel Member/i })).toBeTruthy(),
    )
    // The raw server text never reaches the UI.
    expect(String(toast.error.mock.calls[0][0])).not.toMatch(/caller lost admin/)
  })
})

describe("MembersSection — saving a role", () => {
  async function changeMelTo(role: string) {
    renderSection()
    await screen.findByLabelText(/Mel Member: chat, issue.create/i)

    openSelect(screen.getByRole("combobox", { name: /Change role for Mel Member/i }))
    fireEvent.click(await screen.findByRole("option", { name: role }))
    // Role change is approval-first: a confirm dialog gates the PATCH.
    fireEvent.click(await screen.findByRole("button", { name: /^Change role$/i }))
  }

  it("PATCHes the membership and reports success", async () => {
    apiFetch.mockResolvedValueOnce({ ok: true, status: 200, json: async () => bulkBody })
    apiFetch.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({}) })

    await changeMelTo("MANAGER")

    await waitFor(() => {
      const patch = apiFetch.mock.calls.find(([, init]) => init?.method === "PATCH")
      expect(patch).toBeTruthy()
      expect(patch![0]).toContain("/members/m-mel")
      expect(JSON.parse(patch![1].body)).toEqual({ role: "MANAGER" })
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Role changed to MANAGER"))
  })

  it("surfaces the server's problem detail instead of reporting success", async () => {
    apiFetch.mockResolvedValueOnce({ ok: true, status: 200, json: async () => bulkBody })
    apiFetch.mockResolvedValueOnce({
      ok: false,
      status: 403,
      json: async () => ({ detail: "cannot grant a role at or above your own" }),
    })

    await changeMelTo("MANAGER")

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("cannot grant a role at or above your own"),
    )
    expect(toast.success).not.toHaveBeenCalled()
  })
})
