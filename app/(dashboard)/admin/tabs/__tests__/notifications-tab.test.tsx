// Admin → Notifications drew every provider with the same generic speech
// bubble, while Integrations — one click away, listing the same providers —
// draws each one's actual brand mark. Same objects, two appearances, and the
// admin one carried no information at all: eleven identical icons.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

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
})

// Grouped the way people choose a provider, with this workspace's usage, so
// switching one off says what it will silence.
describe("Admin → Notifications, grouped and counted", () => {
  const FULL = {
    categories: [{ key: "chat", label: "Chat", hint: "Team rooms" }, { key: "incident", label: "Incident", hint: "On-call" }],
    providers: [
      { provider: "discord", scheme: "discord", enabled: true, label: "Discord", category: "chat" },
      { provider: "slack", scheme: "slack", enabled: true, label: "Slack", category: "chat" },
      { provider: "opsgenie", scheme: "opsgenie", enabled: false, label: "Opsgenie", category: "incident" },
    ],
  }
  const CHANNELS = { channels: [{ id: "c1", type: "shoutrrr", provider: "slack" }, { id: "c2", type: "shoutrrr", provider: "slack", scope: "user" }, { id: "c3", type: "webhook" }] }
  beforeEach(() => {
    h.apiFetch.mockImplementation(async (u: string) => ({
      ok: true, status: 200,
      json: async () => (String(u).includes("notification-channels") ? CHANNELS : String(u).includes("security-posture") ? { email_configured: false } : FULL),
    }))
  })

  it("groups providers by category and shows how many channels use each", async () => {
    render(<NotificationsTab workspaceId="ws-1" />)
    expect(await screen.findByRole("region", { name: "Chat" })).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Incident" })).toBeInTheDocument()
    expect(await screen.findByText("2 channels")).toBeInTheDocument()
    expect(document.querySelector("[data-slot=settings-summary]")).toHaveTextContent("2 of 3 providers allowed")
    expect(screen.getByRole("link", { name: /Channels live in Integrations/ })).toHaveAttribute("href", expect.stringContaining("/integrations"))
  })

  it("asks in the row before silencing a provider channels depend on, and says delivery stops", async () => {
    render(<NotificationsTab workspaceId="ws-1" />)
    await screen.findByText("2 channels")
    fireEvent.click(screen.getByRole("switch", { name: /disable slack/i }))
    // The old copy promised only "rejected at channel-create time", which is
    // why an operator who switched Discord off kept receiving Discord posts.
    expect(screen.getByRole("alert")).toHaveTextContent(/2 channels stop delivering/)
    expect(h.apiFetch.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === "PATCH")).toBe(false)

    fireEvent.click(screen.getByRole("button", { name: "Switch off" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(
      "/api/v1/notification-providers/slack?workspace_id=ws-1",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ enabled: false }) }),
    ))
  })

  it("switches an unused provider off without asking", async () => {
    render(<NotificationsTab workspaceId="ws-1" />)
    await screen.findByText("2 channels")
    fireEvent.click(screen.getByRole("switch", { name: /disable discord/i }))
    expect(screen.queryByRole("alert")).toBeNull()
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(
      "/api/v1/notification-providers/discord?workspace_id=ws-1",
      expect.objectContaining({ method: "PATCH" }),
    ))
  })
})

describe("Admin → Notifications, long categories", () => {
  it("shows six providers and folds the rest behind one row", async () => {
    const many = Array.from({ length: 9 }, (_, i) => ({ provider: `p${i}`, scheme: `p${i}`, enabled: true, label: `Provider ${i}`, category: "chat" }))
    h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ categories: [{ key: "chat", label: "Chat", hint: "" }], providers: many }) })
    render(<NotificationsTab workspaceId="ws-1" />)
    await screen.findByText("Provider 0")
    expect(screen.queryByText("Provider 6")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Show 3 more" }))
    expect(screen.getByText("Provider 8")).toBeInTheDocument()
  })
})
