import { describe, it, expect } from "vitest"
import { channelMatches, filterDeliveries } from "@/components/features/integrations/delivery-search"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"

const d = (id: string, extra: Partial<NotificationDelivery>): NotificationDelivery => ({
  id, workspace_id: "ws", channel_id: "ch-slack", user_id: "u", category: "run.failed", dedup_key: id,
  source_kind: "run", source_id: "r", title: "Run failed", status: "sent", created_at: "2026-09-04T09:00:00Z", ...extra,
} as NotificationDelivery)

describe("filterDeliveries", () => {
  const rows = [d("1", {}), d("2", { category: "approval.requested", status: "failed", error: "webhook 500" }), d("3", { channel_id: "ch-mail" })]
  const name = (id: string) => (id === "ch-slack" ? "#crewship-ops" : id === "ch-mail" ? "ops@acme.io" : undefined)

  it("returns everything for an empty query", () => {
    expect(filterDeliveries(rows, "  ", name)).toBe(rows)
  })
  it("matches category, status, error text and the channel's name", () => {
    expect(filterDeliveries(rows, "approval", name).map((r) => r.id)).toEqual(["2"])
    expect(filterDeliveries(rows, "FAILED", name).map((r) => r.id)).toEqual(["1", "2", "3"])
    expect(filterDeliveries(rows, "webhook 500", name).map((r) => r.id)).toEqual(["2"])
    expect(filterDeliveries(rows, "acme", name).map((r) => r.id)).toEqual(["3"])
  })
})

describe("channelMatches", () => {
  it("matches a connection by name, provider or kind", () => {
    expect(channelMatches({ name: "#crewship-ops", provider: "slack", type: "shoutrrr" }, "slack")).toBe(true)
    expect(channelMatches({ name: "ops@acme.io", provider: null, type: "email" }, "MAIL")).toBe(true)
    expect(channelMatches({ name: "hook", provider: null, type: "webhook" }, "discord")).toBe(false)
  })
})
