import { describe, expect, it } from "vitest"

import {
  applyFilters,
  channelKind,
  deriveStatus,
  EMPTY_FILTERS,
  filtersActive,
  isForeignPersonal,
  rowMatches,
  type ConnectionRow,
} from "../connection-model"
import type { NotificationChannel } from "@/hooks/use-notification-channels"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"

function channel(over: Partial<NotificationChannel> = {}): NotificationChannel {
  return {
    id: "nch_1",
    workspace_id: "ws1",
    type: "shoutrrr",
    provider: "discord",
    events: ["failed"],
    enabled: true,
    ...over,
  }
}

function delivery(over: Partial<NotificationDelivery> = {}): NotificationDelivery {
  return {
    id: "nd_1",
    workspace_id: "ws1",
    channel_id: "nch_1",
    user_id: "u1",
    category: "routines.failed",
    dedup_key: "k",
    source_kind: "journal",
    source_id: "j1",
    title: "t",
    status: "sent",
    attempts: 1,
    created_at: "2026-07-27T10:00:00Z",
    updated_at: "2026-07-27T10:00:00Z",
    ...over,
  }
}

function row(over: Partial<ConnectionRow> = {}): ConnectionRow {
  return {
    id: "r1",
    kind: "chat",
    name: "Engineering alerts",
    detail: "discord · #eng-alerts",
    provider: "discord",
    providerLabel: "Discord",
    scope: "workspace",
    enabled: true,
    categories: ["routines.failed"],
    status: "delivering",
    sent24h: 4,
    lastDelivery: null,
    source: "channel",
    readOnly: false,
    ...over,
  }
}

describe("channelKind", () => {
  const cat = (p: string) =>
    ({ discord: "chat", ntfy: "push", opsgenie: "incident" })[p]

  it("reads the kind of a chat/push destination from the provider's category", () => {
    expect(channelKind(channel({ provider: "discord" }), cat)).toBe("chat")
    expect(channelKind(channel({ provider: "ntfy" }), cat)).toBe("push")
    expect(channelKind(channel({ provider: "opsgenie" }), cat)).toBe("incident")
  })

  it("keeps email and webhook out of the provider lookup entirely", () => {
    // They are built-in transports, not catalog providers — asking the
    // catalog about them would always miss.
    expect(channelKind(channel({ type: "email", provider: undefined }), cat)).toBe("email")
    expect(channelKind(channel({ type: "webhook", provider: undefined }), cat)).toBe("webhook")
  })

  it("degrades an unknown provider to chat instead of dropping the row", () => {
    // An older server (no `category` on the wire) or a provider removed from
    // the catalog must still show the connection that exists.
    expect(channelKind(channel({ provider: "some-new-thing" }), cat)).toBe("chat")
    expect(channelKind(channel({ provider: "discord" }), () => undefined)).toBe("chat")
  })
})

describe("deriveStatus", () => {
  it("reports unknown, not healthy, when the caller may not read the log", () => {
    // A MEMBER cannot see deliveries. Claiming "delivering" would be inventing
    // an answer we do not have.
    expect(deriveStatus(true, null)).toBe("unknown")
  })

  it("calls a disabled channel disabled regardless of its history", () => {
    expect(deriveStatus(false, [delivery({ status: "sent" })])).toBe("disabled")
    expect(deriveStatus(false, null)).toBe("disabled")
  })

  it("reports never used when nothing has been attempted", () => {
    expect(deriveStatus(true, [])).toBe("never_used")
  })

  it("lets the most recent failure outrank an older success", () => {
    const rows = [
      delivery({ id: "old", status: "sent", created_at: "2026-07-27T09:00:00Z" }),
      delivery({ id: "new", status: "failed", created_at: "2026-07-27T10:00:00Z" }),
    ]
    expect(deriveStatus(true, rows)).toBe("failing")
    // Order of the input must not change the verdict.
    expect(deriveStatus(true, [...rows].reverse())).toBe("failing")
  })

  it("reports delivering when the newest attempt succeeded", () => {
    expect(
      deriveStatus(true, [
        delivery({ status: "failed", created_at: "2026-07-27T09:00:00Z" }),
        delivery({ status: "sent", created_at: "2026-07-27T10:00:00Z" }),
      ]),
    ).toBe("delivering")
  })

  it("does not call a channel delivering when everything was dropped", () => {
    // dropped_pref / dropped_rate mean nothing left the building. Counting
    // them as delivery is how a muted channel looks healthy.
    expect(
      deriveStatus(true, [
        delivery({ status: "dropped_pref" }),
        delivery({ status: "dropped_rate" }),
      ]),
    ).toBe("never_used")
  })
})

describe("isForeignPersonal", () => {
  it("never marks a workspace channel as somebody else's", () => {
    expect(isForeignPersonal(channel({ scope: "workspace", owner_user_id: "u2" }), "u1")).toBe(false)
  })

  it("marks another member's personal channel", () => {
    expect(isForeignPersonal(channel({ scope: "user", owner_user_id: "u2" }), "u1")).toBe(true)
  })

  it("leaves my own personal channel editable", () => {
    expect(isForeignPersonal(channel({ scope: "user", owner_user_id: "u1" }), "u1")).toBe(false)
  })

  it("does not guess while the session is still loading", () => {
    // Guessing "foreign" here would flicker my own rows to read-only on every
    // page load; guessing "mine" would offer Delete on someone else's channel.
    // Neither is acceptable, so an unknown viewer decides nothing.
    expect(isForeignPersonal(channel({ scope: "user", owner_user_id: "u2" }), null)).toBe(false)
    expect(isForeignPersonal(channel({ scope: "user", owner_user_id: "u2" }), "")).toBe(false)
  })

  it("treats a channel with no recorded owner as not foreign", () => {
    expect(isForeignPersonal(channel({ scope: "user", owner_user_id: undefined }), "u1")).toBe(false)
  })
})

describe("filters", () => {
  it("counts only the facets that constrain anything", () => {
    expect(filtersActive(EMPTY_FILTERS)).toBe(0)
    expect(filtersActive({ ...EMPTY_FILTERS, kind: "chat" })).toBe(1)
    expect(filtersActive({ ...EMPTY_FILTERS, kind: "chat", provider: "slack" })).toBe(2)
  })

  it("intersects facets rather than unioning them", () => {
    const rows = [
      row({ id: "a", kind: "chat", provider: "discord", scope: "workspace" }),
      row({ id: "b", kind: "chat", provider: "slack", scope: "personal" }),
      row({ id: "c", kind: "push", provider: "ntfy", scope: "workspace" }),
    ]
    const got = applyFilters(rows, { ...EMPTY_FILTERS, kind: "chat", scope: "workspace" })
    expect(got.map((r) => r.id)).toEqual(["a"])
  })

  it("returns everything when no facet is set", () => {
    const rows = [row({ id: "a" }), row({ id: "b" })]
    expect(applyFilters(rows, EMPTY_FILTERS)).toHaveLength(2)
  })
})

describe("rowMatches", () => {
  it("matches on the name, the detail line, the provider and the categories", () => {
    const r = row()
    expect(rowMatches(r, "engineering")).toBe(true)
    expect(rowMatches(r, "#eng-alerts")).toBe(true)
    expect(rowMatches(r, "Discord")).toBe(true)
    expect(rowMatches(r, "routines.failed")).toBe(true)
    expect(rowMatches(r, "telegram")).toBe(false)
  })

  it("treats an empty or whitespace query as no constraint", () => {
    expect(rowMatches(row(), "")).toBe(true)
    expect(rowMatches(row(), "   ")).toBe(true)
  })
})
