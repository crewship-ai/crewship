import { describe, it, expect, vi } from "vitest"
import { render, screen, within } from "@testing-library/react"

// useRouter/usePathname are globally mocked in vitest.setup.ts.
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
// No remote data — every entity fetch resolves empty so only the static
// Quick Actions / Navigation / Settings groups render.
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn().mockResolvedValue({ ok: true, json: async () => [] }),
}))

import { CommandPalette } from "../command-palette"

function openPalette() {
  return render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
}

describe("CommandPalette — feature coverage", () => {
  it.each(["Inbox", "Approvals", "Activity", "Routines", "Integrations"])(
    "exposes the %s feature in navigation",
    (label) => {
      openPalette()
      expect(screen.getByText(label)).toBeInTheDocument()
    },
  )

  it.each(["Profile", "Members", "Privacy & Memory", "Audit Log"])(
    "exposes the %s settings deep-link",
    (label) => {
      openPalette()
      const group = screen.getByRole("group", { name: /settings/i })
      expect(within(group).getByText(label)).toBeInTheDocument()
    },
  )

  it("drops Marketplace, which has no page and 404s", () => {
    openPalette()
    expect(screen.queryByText("Marketplace")).not.toBeInTheDocument()
  })
})
