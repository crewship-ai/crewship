import { describe, it, expect, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"
import { EntryAvatar } from "../inbox-entry-identity"
import { inboxEntry } from "../inbox-v2-derive"
import type { InboxLookup } from "../inbox-v2-types"

afterEach(cleanup)

const lookup: InboxLookup = { crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }
const item = (kind: string) => ({ id: "i", workspace_id: "w", kind, source_id: "s", title: "t", state: "unread", priority: "high", blocking: false, sender_type: "system", sender_name: "", created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", payload: {} }) as unknown as InboxItem

// One colour per row: the kind pill carries the severity, so the schedule
// tile beside it is the neutral brand tile for missed and paused alike —
// a paused row used to be red twice, in the tile and in the pill.
describe("schedule alert tile", () => {
  it.each(["schedule_missed", "schedule_circuit_breaker_tripped"])("%s uses the neutral icon tile", (kind) => {
    const { container } = render(<EntryAvatar entry={inboxEntry(item(kind))} lookup={lookup} />)
    const tile = container.querySelector("span")!
    expect(tile.className).toContain("icon-tile")
    expect(tile.className).not.toMatch(/destructive/)
  })
})
