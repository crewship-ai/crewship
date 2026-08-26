import { describe, it, expect } from "vitest"

import {
  applyReadOverrides,
  buildConversationRows,
  filterConversationRows,
  liveThreadIds,
  type ConversationRow,
} from "../conversations-sidebar"
import type { ChatTreeAgent, ChatTreeThread } from "../../chat-tree-sidebar"

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

describe("liveThreadIds", () => {
  it("marks a RUNNING agent's freshest thread and nothing else", () => {
    const rows = buildConversationRows(
      [agent({ id: "a1", name: "Morgan", status: "RUNNING" })],
      {
        a1: [
          thread({ id: "t-new", last_activity_at: "2026-08-26T11:50:00Z" }),
          thread({ id: "t-old", last_activity_at: "2026-08-24T10:00:00Z" }),
        ],
      },
    )
    // One run, one live row. Lighting up every thread an agent owns would
    // claim four things are happening when one is.
    expect([...liveThreadIds(rows)]).toEqual(["t-new"])
  })

  it("marks nothing for an idle agent, however recently it replied", () => {
    const rows = buildConversationRows([agent({ id: "a1", status: "IDLE" })], {
      a1: [thread({ id: "t1", last_activity_at: "2026-08-26T11:59:00Z" })],
    })
    // The old rule was "moved in the last hour", which called a finished
    // conversation live. A reply forty minutes ago is over.
    expect(liveThreadIds(rows).size).toBe(0)
  })

  it("marks one thread per running agent", () => {
    const rows = buildConversationRows(
      [
        agent({ id: "a1", status: "RUNNING" }),
        agent({ id: "a2", status: "RUNNING" }),
        agent({ id: "a3", status: "IDLE" }),
      ],
      {
        a1: [thread({ id: "t1", last_activity_at: "2026-08-26T11:00:00Z" })],
        a2: [thread({ id: "t2", last_activity_at: "2026-08-26T10:00:00Z" })],
        a3: [thread({ id: "t3", last_activity_at: "2026-08-26T11:30:00Z" })],
      },
    )
    expect([...liveThreadIds(rows)].sort()).toEqual(["t1", "t2"])
  })
})

describe("filterConversationRows", () => {
  const agents = [
    agent({ id: "a1", name: "Morgan", status: "RUNNING" }),
    agent({ id: "a2", name: "Riley", status: "IDLE" }),
  ]
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
  const live = liveThreadIds(rows)

  it("keeps everything under the All facet", () => {
    expect(filterConversationRows(rows, "all", "", live)).toHaveLength(2)
  })

  it("keeps only threads with unread messages under Unread", () => {
    const out = filterConversationRows(rows, "unread", "", live)
    expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
  })

  it("keeps only threads whose agent is working under Live", () => {
    const out = filterConversationRows(rows, "live", "", live)
    expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
  })

  it("matches on the thread title", () => {
    const out = filterConversationRows(rows, "all", "seznam", live)
    expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
  })

  it("matches on the agent's name, because that is how people look for threads", () => {
    const out = filterConversationRows(rows, "all", "riley", live)
    expect(out.map((r) => r.thread.id)).toEqual(["t-stale"])
  })

  it("applies the query to the same fields whichever facet is active", () => {
    expect(filterConversationRows(rows, "live", "push", live)).toHaveLength(0)
    expect(filterConversationRows(rows, "all", "push", live)).toHaveLength(1)
  })

  it("treats a whitespace-only query as no query", () => {
    expect(filterConversationRows(rows, "all", "   ", live)).toHaveLength(2)
  })

  describe("the open conversation is never filtered away", () => {
    it("keeps a read thread visible under Unread", () => {
      // The disorienting one: clicking an unread row marks it read, and
      // without the pin the row you just clicked vanishes from under the
      // cursor. It stays, now without its badge.
      const out = filterConversationRows(rows, "unread", "", live, "t-stale")
      expect(out.map((r) => r.thread.id).sort()).toEqual(["t-live", "t-stale"])
    })

    it("keeps a finished thread visible under Live", () => {
      const out = filterConversationRows(rows, "live", "", live, "t-stale")
      expect(out.map((r) => r.thread.id)).toContain("t-stale")
    })

    it("does not pin a thread that is not open", () => {
      const out = filterConversationRows(rows, "unread", "", live, "t-live")
      expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
    })

    it("still answers the search honestly", () => {
      // A query is the reader asking for a specific thing. Pinning against
      // the FACET is help; pinning against the QUERY would be answering a
      // question with something they did not ask about.
      const out = filterConversationRows(rows, "all", "seznam", live, "t-stale")
      expect(out.map((r) => r.thread.id)).toEqual(["t-live"])
    })
  })
})

describe("applyReadOverrides", () => {
  const READ_AT = Date.parse("2026-08-26T12:00:00Z")

  const listWith = (t: Partial<ChatTreeThread> & { id: string }) => ({
    a1: [thread(t)],
  })

  it("zeroes the thread you are looking at, whatever the fetch said", () => {
    // The list GET is routinely served before the mark-read PUT commits, so
    // the open thread's count is stale by one reply almost every time.
    const out = applyReadOverrides(listWith({ id: "t1", unread_count: 3 }), {}, "t1")
    expect(out.a1[0].unread_count).toBe(0)
  })

  it("zeroes a thread read since its last activity", () => {
    const out = applyReadOverrides(
      listWith({ id: "t1", unread_count: 2, last_activity_at: "2026-08-26T11:00:00Z" }),
      { t1: READ_AT },
      null,
    )
    expect(out.a1[0].unread_count).toBe(0)
  })

  it("lets a thread go unread again once the agent replies in it", () => {
    // The regression: a boolean "this is read" survived the agent answering.
    // Open A, switch to B, let A get a reply — A IS unread, the server says
    // so, and the old override kept forcing zero. Switching between agents
    // was where this showed up.
    const out = applyReadOverrides(
      listWith({ id: "t1", unread_count: 2, last_activity_at: "2026-08-26T12:30:00Z" }),
      { t1: READ_AT },
      null,
    )
    expect(out.a1[0].unread_count).toBe(2)
  })

  it("leaves a thread it has never seen alone", () => {
    const out = applyReadOverrides(listWith({ id: "t1", unread_count: 4 }), {}, null)
    expect(out.a1[0].unread_count).toBe(4)
  })

  it("never invents unread the server does not have", () => {
    const out = applyReadOverrides(listWith({ id: "t1", unread_count: 0 }), { t1: 0 }, "t1")
    expect(out.a1[0].unread_count).toBe(0)
  })

  it("falls back to started_at when there is no last activity", () => {
    const out = applyReadOverrides(
      listWith({
        id: "t1",
        unread_count: 1,
        started_at: "2026-08-26T12:30:00Z",
        last_activity_at: null,
      }),
      { t1: READ_AT },
      null,
    )
    expect(out.a1[0].unread_count).toBe(1)
  })

  it("carries every agent through, not just the ones it changed", () => {
    const out = applyReadOverrides(
      {
        a1: [thread({ id: "t1", unread_count: 1 })],
        a2: [thread({ id: "t2", unread_count: 2 })],
      },
      { t1: READ_AT },
      null,
    )
    expect(Object.keys(out).sort()).toEqual(["a1", "a2"])
    expect(out.a2[0].unread_count).toBe(2)
  })
})
