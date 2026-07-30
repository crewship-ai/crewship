// Admin → Notifications drew every provider with the same generic speech
// bubble, while Integrations — one click away, listing the same providers —
// draws each one's actual brand mark. Same objects, two appearances, and the
// admin one carried no information at all: eleven identical icons.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { NotificationsTab } from "../notifications-tab"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const PROVIDERS = {
  providers: [
    { provider: "discord", scheme: "discord", enabled: true },
    { provider: "slack", scheme: "slack", enabled: false },
  ],
}

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => PROVIDERS })
})

describe("Admin → Notifications", () => {
  it("draws each provider's own mark, the same one Integrations uses", async () => {
    const { container } = render(<NotificationsTab workspaceId="ws-1" />)
    await screen.findByRole("switch", { name: /discord/i })

    // ProviderMark renders a per-brand glyph; the old generic icon was one
    // shared lucide component repeated for every row.
    const marks = container.querySelectorAll("[data-provider-mark]")
    expect(marks.length).toBeGreaterThanOrEqual(2)
    expect(Array.from(marks).map((m) => m.getAttribute("data-provider-mark"))).toEqual(
      expect.arrayContaining(["discord", "slack"]),
    )
  })

  it("says that switching a provider off stops delivery, not just new channels", async () => {
    render(<NotificationsTab workspaceId="ws-1" />)
    await screen.findByRole("switch", { name: /discord/i })
    // The old copy promised only "rejected at channel-create time", which is
    // why an operator who switched Discord off kept receiving Discord posts.
    expect(screen.getAllByText(/stops delivery|nothing more leaves/i).length).toBeGreaterThan(0)
  })
})
