import { describe, it, expect, vi, afterEach } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"

import {
  absolute, bucketOf, canRole, categoryOf, decisionMetaFor, durationLabel, expiresIn, jumpFor,
  payloadNumber, payloadString, remainingLabel, resolverOf, since, subjectOf,
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

  it("flags the keeper access request as having no resolve endpoint", () => {
    expect(decisionMetaFor(item({ kind: "escalation", payload: { request_type: "access" } }))?.missingEndpoint)
      .toMatch(/no resolve endpoint/)
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
})
