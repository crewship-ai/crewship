import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, cleanup } from "@testing-library/react"

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { CapabilityGrid } from "../capability-grid"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

// The Member column is `position: sticky`, so it needs an opaque background
// to cover cells scrolling underneath it. It used bg-background while the
// grid sits on bg-card — two different surfaces butting together, which drew
// a visible box around the member column that stopped dead at the Role
// column. The row's hover tint could not show through it either, so hovering
// highlighted every column except that one and made the seam worse.
//
// These pin the surface match. They are CSS assertions, which normally earn
// their keep poorly — but this one is a rendering defect that no behavioural
// test can see, and it was reported from a screenshot twice.

const members = [
  { id: "m1", role: "OWNER", user: { id: "u1", email: "a@x.io", full_name: "Ada", avatar_url: null } },
  { id: "m2", role: "MEMBER", user: { id: "u2", email: "b@x.io", full_name: null, avatar_url: null } },
]

function renderGrid() {
  apiFetch.mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ members: members.map((m) => ({ user_id: m.user.id, role: m.role, capabilities: [] })) }),
  })
  // The grid loads capabilities through react-query; retries off so a
  // stubbed failure surfaces immediately instead of after backoff.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <CapabilityGrid members={members} workspaceId="ws1" currentUserId="u1" />
    </QueryClientProvider>,
  )
}

describe("CapabilityGrid — the pinned Member column", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("sits on the same surface as the rest of the grid", async () => {
    const { container } = renderGrid()
    await waitFor(() => expect(screen.getByText("Ada")).toBeTruthy())

    const sticky = Array.from(container.querySelectorAll<HTMLElement>(".sticky"))
    expect(sticky.length).toBeGreaterThan(0)
    for (const cell of sticky) {
      // bg-background is the page surface; the grid is on bg-card. Mixing
      // them is what drew the box.
      expect(cell.className).not.toMatch(/\bbg-background\b/)
      expect(cell.className).toMatch(/\bbg-card\b/)
    }
  })

  it("follows the row's hover tint instead of staying flat", async () => {
    const { container } = renderGrid()
    await waitFor(() => expect(screen.getByText("Ada")).toBeTruthy())

    const stickyCells = Array.from(container.querySelectorAll<HTMLElement>("td.sticky"))
    expect(stickyCells.length).toBeGreaterThan(0)
    for (const cell of stickyCells) {
      // An opaque sticky cell blocks the <tr> hover, so it has to opt in.
      expect(cell.className).toMatch(/group-hover:/)
    }
  })
})

describe("CapabilityGrid — unchecked cells", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("uses a translucent fill rather than the page colour", async () => {
    const { container } = renderGrid()
    await waitFor(() => expect(screen.getByText("Ada")).toBeTruthy())

    const boxes = Array.from(container.querySelectorAll<HTMLElement>('[role="switch"]'))
    expect(boxes.length).toBeGreaterThan(0)
    // Same wrong-surface bug as the pinned column: bg-background is the page
    // behind the card, so empty cells rendered as dark squares. The shared
    // Checkbox uses a translucent fill for exactly this reason.
    for (const b of boxes) {
      expect(b.className).not.toMatch(/\bbg-background\b/)
    }
  })
})
