import { describe, expect, it } from "vitest"

import {
  canSeeJournalViews,
  journalToActivity,
  journalViewOf,
  memberActivityUrl,
  needsLookup,
} from "@/lib/journal-redirect"

// F-02: every old /journal link keeps landing, and who may see what does not
// change. One row per old route / param.

const sp = (q: string) => new URLSearchParams(q)
const admin = (q: string) => journalToActivity(sp(q), { admin: true })
const member = (q: string, resolve = {}) => journalToActivity(sp(q), { admin: false, resolve })

describe("who sees the Journal views", () => {
  it.each([
    ["OWNER", true],
    ["ADMIN", true],
    ["MANAGER", false],
    ["MEMBER", false],
    ["VIEWER", false],
    [null, false],
    [undefined, false],
    ["", false],
  ])("%s → %s", (role, ok) => {
    expect(canSeeJournalViews(role)).toBe(ok)
  })
})

describe("OWNER / ADMIN: the same view in Activity, params verbatim", () => {
  it.each([
    ["", "/activity?section=log"],
    ["tab=timeline", "/activity?section=log"],
    ["tab=wat", "/activity?section=log"],
    ["time=15m", "/activity?section=log&time=15m"],
    ["time=custom&from=1700000000000&to=1700003600000", "/activity?section=log&time=custom&from=1700000000000&to=1700003600000"],
    ["crew_id=c1&agent_id=a1", "/activity?section=log&crew_id=c1&agent_id=a1"],
    ["crew=engineering&agent=robin", "/activity?section=log&crew=engineering&agent=robin"],
    ["trace_id=run_1", "/activity?section=log&trace_id=run_1"],
    ["tab=timeline&trace_id=run_1", "/activity?section=log&trace_id=run_1"],
    ["mission_id=ENG-4", "/activity?section=log&mission_id=ENG-4"],
    ["severity=error&mute=container,network", "/activity?section=log&severity=error&mute=container%2Cnetwork"],
    ["q=agent%3Aviktor+boom", "/activity?section=log&q=agent%3Aviktor+boom"],
    ["q=hook.fired", "/activity?section=log&q=hook.fired"],
    ["agent_id=a1&entry_type=escalation", "/activity?section=log&agent_id=a1&entry_type=escalation"],
    ["tab=runs", "/activity?section=runs"],
    [
      "tab=runs&run_window=7d&run_status=FAILED&run_trigger=CRON&run_page=3",
      "/activity?section=runs&run_window=7d&run_status=FAILED&run_trigger=CRON&run_page=3",
    ],
    ["tab=spend", "/activity?mode=spend"],
  ])("/journal?%s → %s", (q, target) => {
    expect(admin(q)).toBe(target)
  })
})

describe("everyone else: the Activity list, narrowed by what the link said", () => {
  it.each([
    ["", "/activity"],
    ["tab=timeline", "/activity"],
    ["tab=spend", "/activity"],
    ["tab=spend&time=7d", "/activity"],
    ["time=5m", "/activity?range=1h"],
    ["time=15m", "/activity?range=1h"],
    ["time=1h", "/activity?range=1h"],
    ["time=24h", "/activity?range=24h"],
    ["time=7d", "/activity"],
    ["time=30d", "/activity?range=30d"],
    ["time=all", "/activity?range=30d"],
    [
      "time=custom&from=1700000000000&to=1700003600000",
      `/activity?range=custom&from=${encodeURIComponent(new Date(1700000000000).toISOString())}&to=${encodeURIComponent(new Date(1700003600000).toISOString())}`,
    ],
    ["time=custom", "/activity"],
    ["crew_id=c1", "/activity?crew=c1"],
    ["agent_id=a1", "/activity?agent=a1"],
    ["crew=c1&agent=a1", "/activity?crew=c1&agent=a1"],
    ["mission_id=m1", "/activity?mission=m1"],
    ["trace_id=run_1", "/activity?run=run_1"],
    ["q=payment+declined", "/activity?q=payment+declined"],
    ["q=agent%3Aa1+boom", "/activity?agent=a1&q=boom"],
    ["q=crew%3Ac1", "/activity?crew=c1"],
    ["severity=error&mute=container", "/activity"],
    ["agent_id=a1&entry_type=escalation", "/activity?agent=a1"],
    ["tab=runs", "/activity?what=work&range=24h"],
    ["tab=runs&run_window=7d", "/activity?what=work&range=7d"],
    ["tab=runs&run_window=30d", "/activity?what=work&range=30d"],
    ["tab=runs&run_status=RUNNING", "/activity?what=work&range=24h&view=live"],
    ["tab=runs&run_status=FAILED", "/activity?what=work&range=24h&view=failed"],
    ["tab=runs&run_status=TIMEOUT", "/activity?what=work&range=24h&view=failed"],
    ["tab=runs&run_status=COMPLETED", "/activity?what=work&range=24h&status=completed"],
    ["tab=runs&run_status=CANCELLED", "/activity?what=work&range=24h&status=stopped"],
    ["tab=runs&run_trigger=USER", "/activity?what=work&range=24h&source=person"],
    ["tab=runs&run_trigger=WEBHOOK", "/activity?what=work&range=24h&source=webhook"],
    ["tab=runs&run_trigger=CRON", "/activity?what=work&range=24h&source=schedule"],
    ["tab=runs&run_trigger=AGENT", "/activity?what=work&range=24h&source=chat"],
    ["tab=runs&run_trigger=SYSTEM", "/activity?what=work&range=24h"],
    ["tab=runs&run_page=4", "/activity?what=work&range=24h"],
  ])("/journal?%s → %s", (q, target) => {
    expect(member(q)).toBe(target)
  })

  it("never names an admin-only view", () => {
    for (const q of ["tab=spend", "tab=runs", "", "section=log", "mode=spend"]) {
      const target = member(q)
      expect(target).not.toMatch(/mode=spend|section=log|section=runs/)
    }
  })

  it("resolves slugs and issue identifiers to the ids the list filters on", () => {
    const resolve = {
      crew: (s: string) => (s === "engineering" ? "crew-1" : undefined),
      agent: (s: string) => (s === "robin" ? "agent-7" : undefined),
      issue: (s: string) => (s === "ENG-4" ? "mission-4" : undefined),
    }
    expect(member("crew=engineering&agent=robin&mission_id=ENG-4", resolve)).toBe(
      "/activity?crew=crew-1&agent=agent-7&mission=mission-4",
    )
    // Unknown slugs travel as typed rather than being dropped.
    expect(member("agent_id=ghost", resolve)).toBe("/activity?agent=ghost")
  })
})

describe("admin-only Activity URLs opened by someone else", () => {
  it.each([
    ["section=log&trace_id=run_1&severity=error", "/activity?run=run_1"],
    ["section=log&crew_id=c1&time=30d", "/activity?range=30d&crew=c1"],
    ["section=runs&run_status=FAILED", "/activity?what=work&range=24h&view=failed"],
    ["mode=spend", "/activity"],
  ])("/activity?%s → %s", (q, target) => {
    const params = sp(q)
    const view = journalViewOf(params, "/activity")
    expect(view).not.toBeNull()
    expect(memberActivityUrl(params, view!)).toBe(target)
  })

  it("does not treat the list or the other ledgers as Journal views", () => {
    for (const q of ["", "section=work", "section=deliveries", "view=failed", "mode=work"]) {
      expect(journalViewOf(sp(q), "/activity")).toBeNull()
    }
  })
})

describe("/runs (retired route)", () => {
  it("is the Journal's Runs tab for both roles", () => {
    expect(journalToActivity(sp("section=runs"), { admin: true, pathname: "/activity" })).toBe("/activity?section=runs")
    expect(journalToActivity(sp("section=runs"), { admin: false, pathname: "/activity" })).toBe("/activity?what=work&range=24h")
  })
})

describe("needsLookup", () => {
  it.each([
    ["", false],
    ["tab=runs&run_status=FAILED", false],
    ["crew_id=c1", true],
    ["agent=robin", true],
    ["mission_id=ENG-4", true],
    ["q=agent%3Arobin", true],
    ["q=boom", false],
  ])("%s → %s", (q, want) => {
    expect(needsLookup(sp(q))).toBe(want)
  })
})
