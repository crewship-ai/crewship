import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-channel-agents", () => ({ useChannelAgents: () => ({ agents: [], loading: false }) }))

import { ChannelPage, toggleCategory } from "../channel-page"
import type { ConnectionRow } from "../connection-model"

const row: ConnectionRow = {
  id: "c1",
  kind: "chat",
  name: "#ops-alerts",
  detail: "slack",
  provider: "slack",
  providerLabel: "Slack",
  scope: "workspace",
  enabled: true,
  categories: ["routines.failed"],
  status: "delivering",
  sent24h: 1,
  lastDelivery: null,
  source: "channel",
  readOnly: false,
  channel: { id: "c1", workspace_id: "ws", type: "shoutrrr", provider: "slack", events: [], enabled: true, scope: "workspace", categories: ["routines.failed"] },
}

function renderPage(pane: "overview" | "events" | "settings", over: Partial<Parameters<typeof ChannelPage>[0]> = {}) {
  const props = {
    workspaceId: "ws",
    row,
    pane,
    onPane: vi.fn(),
    deliveries: [],
    providerOff: false,
    canEdit: true,
    onBack: vi.fn(),
    onPatch: vi.fn(async () => {}),
    onTest: vi.fn(async () => {}),
    onRemove: vi.fn(async () => {}),
    ...over,
  }
  render(<ChannelPage {...props} />)
  return props
}

afterEach(cleanup)

describe("ChannelPage", () => {
  it("events toggles PATCH categories", async () => {
    const p = renderPage("events")
    fireEvent.click(screen.getByRole("switch", { name: "Send routines.completed here" }))
    await waitFor(() => expect(p.onPatch).toHaveBeenCalledWith("c1", { categories: ["routines.completed", "routines.failed"] }))
  })

  it("lowest priority PATCHes min_priority", async () => {
    const p = renderPage("events")
    fireEvent.click(screen.getByRole("button", { name: "High" }))
    await waitFor(() => expect(p.onPatch).toHaveBeenCalledWith("c1", { min_priority: "high" }))
  })

  it("paused PATCHes enabled", async () => {
    const p = renderPage("settings")
    fireEvent.click(screen.getByRole("switch", { name: "Paused" }))
    await waitFor(() => expect(p.onPatch).toHaveBeenCalledWith("c1", { enabled: false }))
  })

  it("remove asks first and stays open while the server refuses", async () => {
    const onRemove = vi.fn(async () => {
      throw new Error("nope")
    })
    renderPage("settings", { onRemove })
    fireEvent.click(screen.getByRole("button", { name: "Remove…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("Agents and routines stop sending here")
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove channel" }))
    await waitFor(() => expect(onRemove).toHaveBeenCalled())
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
  })

  it("is read-only without edit rights", () => {
    renderPage("settings", { canEdit: false })
    expect(screen.getByRole("switch", { name: "Paused" })).toBeDisabled()
    expect(screen.queryByRole("button", { name: "Remove…" })).not.toBeInTheDocument()
  })

  it("says when the service is off for the server", () => {
    renderPage("overview", { providerOff: true })
    expect(screen.getByText("Turned off for this server")).toBeInTheDocument()
    expect(screen.getByText(/Admin › Notifications/)).toBeInTheDocument()
  })

  it("never offers Send again on a delivery", () => {
    renderPage("overview", { row: { ...row, status: "failing" }, deliveries: [{ id: "d", workspace_id: "ws", channel_id: "c1", user_id: "u", category: "routines.failed", dedup_key: "", source_kind: "", source_id: "", title: "x", status: "failed", attempts: 3, created_at: new Date().toISOString(), updated_at: "" }] })
    expect(screen.getByText("1 delivery failed")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Send again/ })).not.toBeInTheDocument()
  })
})

describe("toggleCategory", () => {
  it("narrows 'every category' to all but one, and widens back to []", () => {
    const off = toggleCategory([], "memory", false)
    expect(off).not.toContain("memory")
    expect(toggleCategory(off, "memory", true)).toEqual([])
  })
})
