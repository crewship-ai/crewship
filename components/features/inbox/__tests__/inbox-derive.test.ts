import { describe, it, expect, vi, afterEach } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"

import {
  absolute, bucketOf, canRole, categoryOf, decisionMetaFor, durationLabel, expiresIn, jumpFor,
  payloadNumber, payloadString, remainingLabel, resolverOf, since, subjectOf, withinPeriod,
} from "../inbox-derive"

// The derivations decide what every row is called, where it files, who may act
// on it and how urgent it looks. They are pure, so they are cheap to pin — and
// each one below is a rule the surface would otherwise get wrong silently.

function item(over: Partial<InboxItem> & Pick<InboxItem, "kind">): InboxItem {
  return {
    id: "i", workspace_id: "w", source_id: "s", title: "t",
    state: "unread", priority: "medium", blocking: false,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    ...over,
  } as InboxItem
}

afterEach(() => vi.useRealTimers())

describe("payload readers", () => {
  it("returns a string only when it is one", () => {
    const it0 = item({ kind: "message", payload: { a: "x", b: 3, c: null } })
    expect(payloadString(it0, "a")).toBe("x")
    expect(payloadString(it0, "b")).toBe("")
    expect(payloadString(it0, "missing")).toBe("")
  })

  it("returns a number only when it is one", () => {
    const it0 = item({ kind: "message", payload: { a: "3", b: 3 } })
    expect(payloadNumber(it0, "a")).toBeNull()
    expect(payloadNumber(it0, "b")).toBe(3)
  })
})

describe("canRole mirrors the server", () => {
  it('treats "create" as MANAGER and up', () => {
    expect(canRole("MANAGER", "create")).toBe(true)
    expect(canRole("MEMBER", "create")).toBe(false)
  })

  it('treats "manage" as OWNER/ADMIN only — the gap that 403s a manager', () => {
    expect(canRole("ADMIN", "manage")).toBe(true)
    expect(canRole("MANAGER", "manage")).toBe(false)
  })

  it("says no when the role has not resolved yet", () => {
    expect(canRole(null, "create")).toBe(false)
  })
})

describe("bucketOf", () => {
  it("files anything blocking as a decision, whatever its kind", () => {
    expect(bucketOf(item({ kind: "message", blocking: true }))).toBe("decisions")
  })

  it("files waitpoints and escalations as decisions", () => {
    expect(bucketOf(item({ kind: "waitpoint" }))).toBe("decisions")
    expect(bucketOf(item({ kind: "escalation" }))).toBe("decisions")
  })

  it("separates a chat reply from an issue nudge", () => {
    expect(bucketOf(item({ kind: "message", payload: { chat_url: "/chat/x" } }))).toBe("replies")
    expect(bucketOf(item({ kind: "message", payload: { issue_identifier: "ENG-6" } }))).toBe("review")
  })

  it("gives routine progress its own lane, which is why subkind is written", () => {
    expect(bucketOf(item({ kind: "message", payload: { subkind: "routine_update" } }))).toBe("routines")
    expect(bucketOf(item({ kind: "schedule_missed" }))).toBe("routines")
    expect(bucketOf(item({ kind: "schedule_circuit_breaker_tripped" }))).toBe("routines")
  })

  it("falls back to other", () => {
    expect(bucketOf(item({ kind: "memory_consolidation" }))).toBe("other")
  })
})

describe("subjectOf — who the row is ABOUT", () => {
  it("prefers the agent in the payload over the system that sent it", () => {
    const s = subjectOf(item({ kind: "escalation", sender_type: "system", sender_name: "Keeper", payload: { agent_name: "casey" } }))
    expect(s).toMatchObject({ kind: "agent", label: "casey" })
  })

  it("falls back to agent_slug", () => {
    expect(subjectOf(item({ kind: "message", payload: { agent_slug: "atlas" } })).label).toBe("atlas")
  })

  it("keeps an agent sender's own avatar seed", () => {
    const s = subjectOf(item({ kind: "message", sender_type: "agent", sender_name: "casey", avatar_seed: "seed-1" }))
    expect(s).toMatchObject({ kind: "agent", seed: "seed-1" })
  })

  it("marks pipelines, crews and the system by kind", () => {
    expect(subjectOf(item({ kind: "message", sender_type: "pipeline", sender_name: "nightly" })).kind).toBe("routine")
    expect(subjectOf(item({ kind: "message", sender_type: "crew", sender_name: "Ops" })).kind).toBe("crew")
    expect(subjectOf(item({ kind: "message", sender_type: "system", sender_name: "Keeper" })).kind).toBe("system")
  })
})

describe("resolverOf — the human, not the machine", () => {
  it("is a user, so it draws as a circle rather than an agent tile", () => {
    expect(resolverOf(item({ kind: "message", resolved_by_user_id: "pavel" }))).toEqual({
      kind: "user", id: "pavel", label: "pavel",
    })
  })

  it("is null when nobody decided — an expiry has no actor", () => {
    expect(resolverOf(item({ kind: "waitpoint" }))).toBeNull()
  })
})

describe("time", () => {
  it("steps up from minutes so a day-long timeout is not shown as 1428m", () => {
    expect(remainingLabel(11)).toBe("11m")
    expect(remainingLabel(90)).toBe("2h")
    expect(remainingLabel(1428)).toBe("24h")
    expect(remainingLabel(4 * 24 * 60)).toBe("4d")
  })

  it("reads timeout_at, and only when it is there", () => {
    vi.useFakeTimers().setSystemTime(new Date("2026-07-30T12:00:00Z"))
    expect(expiresIn(item({ kind: "waitpoint", payload: { timeout_at: "2026-07-30T12:30:00Z" } }))).toBe(30)
    expect(expiresIn(item({ kind: "waitpoint" }))).toBeNull()
  })

  it("goes negative once it has expired, so the UI can say so", () => {
    vi.useFakeTimers().setSystemTime(new Date("2026-07-30T13:00:00Z"))
    expect(expiresIn(item({ kind: "waitpoint", payload: { timeout_at: "2026-07-30T12:30:00Z" } }))).toBe(-30)
  })

  it("formats relative and absolute times, and survives a missing one", () => {
    vi.useFakeTimers().setSystemTime(new Date("2026-07-30T12:00:00Z"))
    expect(since(new Date("2026-07-30T11:59:40Z").toISOString())).toBe("just now")
    expect(since(new Date("2026-07-30T11:30:00Z").toISOString())).toBe("30m ago")
    expect(since(new Date("2026-07-30T09:00:00Z").toISOString())).toBe("3h ago")
    expect(since(undefined)).toBe("—")
    expect(absolute(undefined)).toBe("—")
    expect(durationLabel(null)).toBe("—")
    expect(durationLabel(30)).toBe("30m")
    expect(durationLabel(120)).toBe("2h")
  })
})

describe("withinPeriod", () => {
  const day = 24 * 60 * 60 * 1000
  const at = (daysAgo: number) => new Date(Date.now() - daysAgo * day).toISOString()

  it("dates the DECISION, not the arrival", () => {
    // Raised long ago, closed yesterday: the archive answers "what did we
    // decide lately", so this is inside a 7-day window.
    const old = item({ kind: "escalation", created_at: at(120), resolved_at: at(1) })
    expect(withinPeriod(old, "7")).toBe(true)
    expect(withinPeriod(item({ kind: "escalation", resolved_at: at(90) }), "30")).toBe(false)
  })

  it("lets everything through on all time", () => {
    expect(withinPeriod(item({ kind: "escalation", resolved_at: at(999) }), "all")).toBe(true)
  })

  it("fails open on a window it cannot read, rather than hiding rows", () => {
    // A hidden row is worse than an unfiltered one: the reader cannot tell the
    // difference between "nothing matched" and "the filter broke".
    expect(withinPeriod(item({ kind: "escalation", resolved_at: at(999) }), "nonsense")).toBe(true)
    expect(withinPeriod(item({ kind: "escalation", resolved_at: at(999) }), "0")).toBe(true)
  })

  it("fails open on an unparsable timestamp", () => {
    expect(withinPeriod(item({ kind: "escalation", resolved_at: "not-a-date" }), "7")).toBe(true)
  })

  it("falls back to arrival when nothing closed it", () => {
    expect(withinPeriod(item({ kind: "waitpoint", created_at: at(2) }), "7")).toBe(true)
    expect(withinPeriod(item({ kind: "waitpoint", created_at: at(40) }), "7")).toBe(false)
  })
})

describe("categoryOf mirrors internal/notify/categories.go", () => {
  it("maps every kind the backend writes", () => {
    expect(categoryOf(item({ kind: "waitpoint" }))).toBe("agents.approval")
    expect(categoryOf(item({ kind: "escalation" }))).toBe("agents.escalation")
    expect(categoryOf(item({ kind: "failed_run" }))).toBe("routines.failed")
    expect(categoryOf(item({ kind: "message" }))).toBe("chat.replies")
    expect(categoryOf(item({ kind: "memory_consolidation" }))).toBe("memory")
    expect(categoryOf(item({ kind: "schedule_missed" }))).toBe("routines.missed")
    expect(categoryOf(item({ kind: "schedule_circuit_breaker_tripped" }))).toBe("routines.missed")
  })

  it("shows the raw kind rather than nothing for a kind added later", () => {
    expect(categoryOf(item({ kind: "brand_new" as InboxItem["kind"] }))).toBe("brand_new")
  })
})

describe("decisionMetaFor — which role the server demands", () => {
  it("needs MANAGER+ for a waitpoint", () => {
    expect(decisionMetaFor(item({ kind: "waitpoint" }))).toMatchObject({ requires: "create", tone: "warn" })
  })

  it("needs OWNER/ADMIN for a skill proposal, which is addressed to MANAGER", () => {
    expect(decisionMetaFor(item({ kind: "escalation", payload: { kind: "skill_proposal" } }))).toMatchObject({
      requires: "manage", heading: "Proposed skill",
    })
  })

  it("needs MANAGER+ for a routine proposal", () => {
    expect(decisionMetaFor(item({ kind: "escalation", payload: { kind: "routine_proposal" } }))).toMatchObject({
      requires: "create", heading: "Proposed routine",
    })
  })

  // Was: "flags the keeper access request as having no resolve endpoint". It has
  // one now — POST /admin/keeper/requests/{id}/resolve — so the card offers
  // Approve/Deny instead of admitting the server cannot take the decision. The
  // endpoint is derived from the request id because that is the row being ruled
  // on; without one there is nothing to resolve and the field stays undefined.
  it("points a keeper access request at its resolve endpoint", () => {
    const meta = decisionMetaFor(item({
      kind: "escalation",
      payload: { request_type: "access", request_id: "kr_1" },
    }))
    expect(meta?.resolveEndpoint).toBe("/api/v1/admin/keeper/requests/kr_1/resolve")
    expect(meta?.missingEndpoint).toBeUndefined()
  })

  // A credential decision is roleManage on the server, and the card has to say
  // the same thing: telling a MANAGER the ruling is theirs, when the server will
  // refuse them, is what justified addressing the item to an audience wider than
  // the people who can act on it.
  it("requires manage for a credential access request", () => {
    expect(decisionMetaFor(item({ kind: "escalation", payload: { request_type: "access" } }))?.requires)
      .toBe("manage")
  })

  it("frames the three kinds the old UI could not draw", () => {
    expect(decisionMetaFor(item({ kind: "schedule_circuit_breaker_tripped" }))?.heading).toBe("Routine is disabled")
    expect(decisionMetaFor(item({ kind: "schedule_missed" }))?.heading).toBe("Missed occurrences")
    expect(decisionMetaFor(item({ kind: "memory_consolidation" }))).toMatchObject({ requires: "manage", tone: "default" })
  })

  it("returns nothing to frame for a plain message or failed run", () => {
    expect(decisionMetaFor(item({ kind: "message" }))).toBeNull()
    expect(decisionMetaFor(item({ kind: "failed_run" }))).toBeNull()
  })
})

describe("jumpFor", () => {
  it("prefers the chat deep link, then the issue, then the run", () => {
    expect(jumpFor(item({ kind: "message", payload: { chat_url: "/chat/x", issue_identifier: "ENG-6" } }))?.label)
      .toBe("Open chat")
    expect(jumpFor(item({ kind: "message", payload: { issue_identifier: "ENG-6" } }))?.label).toBe("Open ENG-6")
    expect(jumpFor(item({ kind: "waitpoint", payload: { pipeline_run_id: "r1" } }))?.label).toBe("Open run")
    expect(jumpFor(item({ kind: "message" }))).toBeNull()
  })

  // The label alone was all this ever returned, so the detail pane rendered a
  // button that NAMED a destination and went nowhere. A jump target without an
  // href is not a jump target.
  it("carries the href for each destination it names", () => {
    expect(jumpFor(item({ kind: "message", payload: { chat_url: "/chat/x" } }))?.href).toBe("/chat/x")
    expect(jumpFor(item({ kind: "message", payload: { issue_identifier: "ENG-6" } }))?.href).toBe("/issues/ENG-6")
    expect(jumpFor(item({ kind: "waitpoint", payload: { pipeline_run_id: "r1" } }))?.href)
      .toBe("/activity?run=r1")
  })

  it("encodes identifiers that would otherwise break the path or query", () => {
    expect(jumpFor(item({ kind: "message", payload: { issue_identifier: "ENG/6 7" } }))?.href)
      .toBe("/issues/ENG%2F6%207")
    expect(jumpFor(item({ kind: "waitpoint", payload: { pipeline_run_id: "r 1&x=2" } }))?.href)
      .toBe("/activity?run=r%201%26x%3D2")
  })

  // Same rule kind-actions applies to chat_url: one leading slash, so a payload
  // can't turn an in-app jump into an off-origin navigation.
  it("refuses an off-origin chat_url instead of linking to it", () => {
    expect(jumpFor(item({ kind: "message", payload: { chat_url: "//evil.example/x" } }))).toBeNull()
    expect(jumpFor(item({ kind: "message", payload: { chat_url: "/\\evil.example/x" } }))).toBeNull()
    expect(jumpFor(item({ kind: "message", payload: { chat_url: "https://evil.example" } }))).toBeNull()
  })

  it("falls through to the run when chat_url is unsafe but a run exists", () => {
    const j = jumpFor(item({ kind: "waitpoint", payload: { chat_url: "https://evil.example", pipeline_run_id: "r1" } }))
    expect(j?.href).toBe("/activity?run=r1")
  })
})
