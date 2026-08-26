import { describe, it, expect } from "vitest"

import {
  buildConversationRows,
  filterConversationRows,
  type ConversationRow,
} from "../conversations-sidebar"
import type { ChatTreeAgent, ChatTreeThread } from "../../chat-tree-sidebar"

const NOW = Date.parse("2026-08-26T12:00:00Z")

function agent(partial: Partial<ChatTreeAgent> & { id: string }): ChatTreeAgent {
  return {
    name: partial.name ?? partial.id,
    slug: partial.slug ?? partial.id,
    status: partial.status ?? "idle",
    ...partial,
  }
}

function thread(partial: Partial<ChatTreeThread> & { id: string }): ChatTreeThread {
  return {
    title: partial.title ?? null,
    status: partial.status ?? "open",
    message_count: partial.message_count ?? 2,
    started_at: partial.started_at ?? "2026-08-20T10:00:00Z",
    ...partial,
  }
}

describe("buildConversationRows", () => {
  it("merges every agent's threads into one list, newest first", () => {
    const agents = [agent({ id: "a1", name: "Morgan" }), agent({ id: "a2", name: "Riley" })]
    const rows = buildConversationRows(agents, {
      a1: [thread({ id: "t-old", last_activity_at: "2026-08-24T10:00:00Z" })],
      a2: [
        thread({ id: "t-new", last_activity_at: "2026-08-26T11:00:00Z" }),
        thread({ id: "t-mid", last_activity_at: "2026-08-25T10:00:00Z" }),
      ],
    })
    expect(rows.map((r) => r.thread.id)).toEqual(["t-new", "t-mid", "t-old"])
    // Attribution rides with the row — that is what lets the sidebar drop the
    // per-agent grouping and still say whose conversation this is.
    expect(rows[0].agent.name).toBe("Riley")
  })

  it("falls back to started_at when a thread has no last activity", () => {
    const rows = buildConversationRows([agent({ id: "a1" })], {
      a1: [thread({ id: "t1", started_at: "2026-08-22T09:00:00Z", last_activity_at: null })],
    })
    expect(rows[0].at).toBe(Date.parse("2026-08-22T09:00:00Z"))
  })

  it("sorts an unparseable thread last instead of to the top", () => {
    // A NaN sort key floats to whichever end the comparator happens to put it.
    // A thread whose timestamps we cannot read is the LEAST likely to be the
    // one the reader wants first.
    const rows = buildConversationRows([agent({ id: "a1" })], {
      a1: [
        thread({ id: "broken", started_at: "not-a-date", last_activity_at: "also-not" }),
        thread({ id: "good", last_activity_at: "2026-08-25T10:00:00Z" }),
      ],
    })
    expect(rows.map((r) => r.thread.id)).toEqual(["good", "broken"])
  })

  it("returns an empty list for an agent with no threads", () => {
    expect(buildConversationRows([agent({ id: "a1" })], {})).toEqual([])
  })
})

describe("filterConversationRows", () => {
  const agents = [agent({ id: "a1", name: "Morgan" }), agent({ id: "a2", name: "Riley" })]
  const rows: ConversationRow[] = buildConversationRows(agents, {
    a1: [
      thread({
        id: "t-live",
        title: "Rutina nad seznam.cz",
        last_activity_at: "2026-08-26T11:40:00Z",
        unread_count: 2,
      }),
    ],
    a2: [
      thread({
        id: "t-stale",
        title: "Push the payload",
        last_activity_at: "2026-08-20T10:00:00Z",
      }),
    ],
  })

  it("keeps everything under the All facet", () => {
    expect(filterConversationRows(rows, "all", "", NOW)).toHaveLength(2)
  })

  it("keeps only threads with unread messages under Unread", () => {
    const out = filterConversationRows(rows, "unread", "", NOW)
    expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
  })

  it("keeps only threads that moved within the hour under Live", () => {
    const out = filterConversationRows(rows, "live", "", NOW)
    expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
  })

  it("matches on the thread title", () => {
    const out = filterConversationRows(rows, "all", "seznam", NOW)
    expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
  })

  it("matches on the agent's name, because that is how people look for threads", () => {
    const out = filterConversationRows(rows, "all", "riley", NOW)
    expect(out.map((r) => r.thread.id)).toEqual(["t-stale"])
  })

  it("applies the query to the same fields whichever facet is active", () => {
    // Live + a query only the stale thread matches returns nothing — the facet
    // IS the filter — but the field set the query reads is identical either
    // way, so a result can never disappear because of where it was searched.
    expect(filterConversationRows(rows, "live", "push", NOW)).toHaveLength(0)
    expect(filterConversationRows(rows, "all", "push", NOW)).toHaveLength(1)
  })

  it("treats a whitespace-only query as no query", () => {
    expect(filterConversationRows(rows, "all", "   ", NOW)).toHaveLength(2)
  })
})
