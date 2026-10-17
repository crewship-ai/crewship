import { describe, expect, it } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"
import type { JournalEntry } from "@/lib/types/journal"
import {
  EMPTY_FILTERS,
  bundleRepeats,
  changeFromEntry,
  changeSentence,
  feedCsv,
  feedUrlParams,
  foldStories,
  groupFeed,
  inRange,
  mergeFeed,
  parseFeedUrl,
  rangeBounds,
  rangeDates,
  rangeIncluding,
  runHistory,
  stepRange,
  viewCounts,
  workFromChain,
  workPasses,
  type FeedUrlState,
  type StoryItem,
  type WorkItem,
} from "@/lib/activity-feed"

// Thu 15 Oct 2026, 08:16 local time.
const NOW = new Date(2026, 9, 15, 8, 16).getTime()
const at = (day: number, h: number, m = 0) => new Date(2026, 9, day, h, m).toISOString()

const chain = (origin: string, over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin,
  started_by_kind: "schedule",
  started_by: "nightly",
  runs: 1,
  max_chain_depth: 0,
  failed_runs: 0,
  failed: false,
  first_activity: at(15, 7),
  last_activity: at(15, 7, 1),
  duration_ms: 60_000,
  issue_count: 0,
  agent_count: 0,
  routine_slug: "digest",
  ...over,
})

const entry = (id: string, over: Partial<JournalEntry> = {}): JournalEntry => ({
  id,
  workspace_id: "ws",
  ts: at(15, 7, 30),
  entry_type: "mission.status_change",
  severity: "info",
  actor_type: "user",
  summary: id,
  ...over,
})

const work = (c: ChainSummary) => workFromChain(c, { routineName: (s) => (s === "digest" ? "Daily digest" : undefined) })

describe("work and change rows merge by time, repeated runs bundle per routine per day", () => {
  it("maps a chain to a work row in the Routines vocabulary", () => {
    const w = work(chain("r1", { waiting_runs: 1, agents: [{ id: "a1", name: "Scout", assignments: 1 }] }))
    expect(w).toMatchObject({ type: "work", id: "r1", status: "waiting", active: true, title: "Daily digest", source: "schedule", origin: "schedule" })
    expect(w.agents).toEqual([{ id: "a1", name: "Scout" }])
    expect(work(chain("r2", { failed: true })).status).toBe("failed")
    expect(work(chain("r3", { cancelled_runs: 1 })).status).toBe("stopped")
    expect(work(chain("r4")).status).toBe("completed")
  })

  it("maps only change entry types, with before → after when the payload says", () => {
    const c = changeFromEntry(entry("e1", { mission_id: "m1", payload: { from: "In progress", to: "In review" } }), {
      issueLabel: (id) => (id === "m1" ? "CRW-148 Rotate certificates" : undefined),
    })
    expect(c).toMatchObject({ type: "change", kind: "issue", object: "CRW-148 Rotate certificates", action: "Status changed", who: "A person", diff: "In progress → In review" })
    expect(changeFromEntry(entry("e2", { entry_type: "run.completed" }))).toBeNull()
    expect(changeFromEntry(entry("e3", { entry_type: "credential.revealed", actor_type: "agent", agent_id: "a1" }), { agentName: () => "Scout" })).toMatchObject({ kind: "access", who: "Scout" })
  })

  it("names audited objects in words, never the raw event name", () => {
    const audit = (id: string, entry_type: string, payload: Record<string, unknown>, summary: string) =>
      changeFromEntry(entry(id, { entry_type, payload, summary }))!
    expect(
      audit("a1", "audit.entity_deleted", { action: "hook.delete", entity_type: "hook", entity_id: "hk_4734b7ad8729a2e2", metadata: { event: "post_agent_stop" } }, "hook.delete hook hk_4734b7ad8"),
    ).toMatchObject({ kind: "crew", object: "Hook hk_4734b7ad8", action: "Deleted" })
    expect(
      audit("a2", "audit.entity_created", { action: "agent_template.create", entity_type: "agent_template", entity_id: "tpl_1", metadata: { name: "Nightly reviewer" } }, "agent_template.create agent_template tpl_1"),
    ).toMatchObject({ object: "Agent template Nightly reviewer", action: "Created" })
    expect(
      audit("a3", "audit.entity_updated", { action: "crew.update", entity_type: "crew", entity_id: "crw_9", name: "Platform" }, "crew.update crew crw_9"),
    ).toMatchObject({ object: "Crew Platform", action: "Updated" })
    // An older entry with only the summary still reads as words.
    expect(audit("a4", "audit.entity_restored", {}, "webhook.restore webhook wh_0123456789abcdef")).toMatchObject({ object: "Webhook wh_012345678", action: "Restored" })
    // A summary that is already a sentence is kept as it is.
    expect(audit("a5", "audit.entity_updated", {}, "Renamed the Platform crew")).toMatchObject({ object: "Renamed the Platform crew" })
    // The Peek's sentence: the verb and the object, not the event name.
    expect(changeSentence(audit("a6", "audit.entity_deleted", { entity_type: "hook", entity_id: "hk_4734b7ad8729a2e2" }, "hook.delete hook hk_4734b7ad8"))).toBe("Deleted hook hk_4734b7ad8")
    expect(changeSentence(audit("a7", "audit.entity_updated", {}, "Renamed the Platform crew"))).toBe("Renamed the Platform crew")
  })

  it("merges newest first", () => {
    const w1 = work(chain("r1", { first_activity: at(15, 7) }))
    const w2 = work(chain("r2", { first_activity: at(14, 9) }))
    const c1 = changeFromEntry(entry("e1", { ts: at(15, 6) }))!
    expect(mergeFeed([w2, w1], [c1]).map((x) => x.id)).toEqual(["r1", "e1", "r2"])
  })

  it("bundles three or more completed runs of one routine on one day, and nothing that needs a person", () => {
    const runs = [7, 6, 5, 4].map((h) => work(chain(`d${h}`, { first_activity: at(14, h) })))
    const failed = work(chain("df", { first_activity: at(14, 3), failed: true }))
    const today = [work(chain("t1", { first_activity: at(15, 7) })), work(chain("t2", { first_activity: at(15, 6) }))]
    const out = bundleRepeats([...today, ...runs, failed], NOW)
    expect(out.map((x) => x.id)).toEqual(["t1", "t2", "bundle:digest:d7", "df"])
    const b = out[2] as WorkItem
    expect(b.runs?.map((r) => r.id)).toEqual(["d7", "d6", "d5", "d4"])
    expect(b.durationMs).toBe(60_000)
  })

  it("does not bundle two runs, or runs on different days", () => {
    const xs = [work(chain("a", { first_activity: at(14, 7) })), work(chain("b", { first_activity: at(14, 6) })), work(chain("c", { first_activity: at(13, 6) }))]
    expect(bundleRepeats(xs, NOW).map((x) => x.id)).toEqual(["a", "b", "c"])
  })

  it("groups by time with Active now first, then by day", () => {
    const running = work(chain("live", { running_runs: 1, first_activity: at(13, 7) }))
    const xs = mergeFeed([running, work(chain("t", { first_activity: at(15, 7) })), work(chain("y", { first_activity: at(14, 7) }))], [])
    expect(groupFeed(xs, "time", NOW).map((g) => [g.label, g.items.map((i) => i.id)])).toEqual([
      ["Active now", ["live"]],
      ["Today", ["t"]],
      ["Yesterday", ["y"]],
    ])
  })

  it("groups by what, object and who", () => {
    const w = work(chain("r1", { agents: [{ id: "a1", name: "Scout", assignments: 1 }] }))
    const c = changeFromEntry(entry("e1", { mission_id: "m1" }), { issueLabel: () => "CRW-1" })!
    expect(groupFeed([w, c], "what", NOW).map((g) => g.label)).toEqual(["Runs and agent work", "Issues"])
    expect(groupFeed([w, c], "object", NOW, { routineName: () => "Daily digest" }).map((g) => g.label)).toEqual(["Daily digest", "CRW-1"])
    expect(groupFeed([w, c], "who", NOW).map((g) => g.label)).toEqual(["Scout", "A person"])
  })

  it("counts views over the other facets", () => {
    const xs = [work(chain("a", { running_runs: 1 })), work(chain("b", { waiting_runs: 1 })), work(chain("c", { failed: true })), work(chain("d", { routine_slug: "other" }))]
    const changes = [changeFromEntry(entry("e1"))!]
    expect(viewCounts(xs, changes, EMPTY_FILTERS)).toEqual({ all: 5, need: 1, live: 1, failed: 1 })
    expect(viewCounts(xs, changes, { ...EMPTY_FILTERS, routine: "digest" }).all).toBe(3)
    expect(workPasses(xs[0], { ...EMPTY_FILTERS, q: "daily" }, { routineName: () => "Daily digest" })).toBe(true)
  })

  it("exports the rows as CSV with bundles unfolded", () => {
    const runs = [7, 6, 5].map((h) => work(chain(`d${h}`, { first_activity: at(14, h) })))
    const csv = feedCsv(bundleRepeats(runs, NOW))
    expect(csv.trim().split("\n")).toHaveLength(4)
    expect(csv).toContain("d5")
  })
})

describe("change stories: several updates to one object on one day are one row", () => {
  const issueCtx = { issueLabel: (id: string) => (id === "m1" ? "CRW-1 Fix flaky login" : id === "m2" ? "CRW-2 Docs" : undefined) }
  const status = (id: string, h: number, from: string, to: string, over: Partial<JournalEntry> = {}) =>
    changeFromEntry(entry(id, { ts: at(14, h), mission_id: "m1", payload: { from, to }, ...over }), issueCtx)!
  const three = () => [status("e3", 11, "In progress", "In review"), status("e2", 10, "Todo", "In progress"), status("e1", 9, "Backlog", "Todo")]
  const crw1 = [{ id: "m1", identifier: "CRW-1", title: "Fix flaky login" }]

  it("folds three updates to one issue on one day into a story, in the order they happened", () => {
    const out = foldStories(three(), NOW)
    expect(out).toHaveLength(1)
    const st = out[0] as StoryItem
    expect(st.type).toBe("story")
    expect(st.title).toBe("CRW-1 Fix flaky login")
    expect(st.objectKind).toBe("issue")
    expect(st.missionId).toBe("m1")
    // Newest first like the list; the row's time is the last update.
    expect(st.items.map((x) => x.id)).toEqual(["e3", "e2", "e1"])
    expect(st.ts).toBe(Date.parse(at(14, 11)))
    expect(st.first).toBe(Date.parse(at(14, 9)))
    expect(st.flow).toBe("Todo → In progress → In review")
    expect(st.diff).toBe("In progress → In review")
    expect(st.who).toEqual(["A person"])
  })

  it("keeps two updates, or updates on different days, as rows of their own", () => {
    expect(foldStories(three().slice(0, 2), NOW).map((x) => x.type)).toEqual(["change", "change"])
    const spread = [status("a", 11, "A", "B"), status("b", 10, "B", "C"), status("c", 9, "C", "D", { ts: at(13, 9) })]
    expect(foldStories(spread, NOW).map((x) => x.id)).toEqual(["a", "b", "c"])
    const twoIssues = [status("a", 11, "A", "B"), status("b", 10, "B", "C", { mission_id: "m2" }), status("c", 9, "C", "D")]
    expect(foldStories(twoIssues, NOW).map((x) => x.id)).toEqual(["a", "b", "c"])
  })

  it("names the first three steps and ends with … when there are more", () => {
    const four = [status("e4", 12, "In review", "Done"), ...three()]
    expect((foldStories(four, NOW)[0] as StoryItem).flow).toBe("Todo → In progress → In review → …")
  })

  it("lets completed agent work on the issue join its story; nothing that needs a person, no routine runs", () => {
    const agentWork = work(chain("w1", { routine_slug: undefined, started_by_kind: "issue", first_activity: at(14, 10, 30), agents: [{ id: "a1", name: "Scout", assignments: 1 }], issues: crw1 }))
    const failedWork = work(chain("w2", { routine_slug: undefined, failed: true, first_activity: at(14, 10, 40), issues: crw1 }))
    const routineRun = work(chain("w3", { first_activity: at(14, 10, 50), issues: crw1 }))
    const out = foldStories(mergeFeed([agentWork, failedWork, routineRun], three()), NOW)
    expect(out.map((x) => x.type)).toEqual(["work", "work", "story"])
    expect(out.slice(0, 2).map((x) => x.id)).toEqual(["w3", "w2"])
    const st = out[2] as StoryItem
    expect(st.items.map((x) => x.id)).toEqual(["e3", "w1", "e2", "e1"])
    expect(st.flow).toBe("Todo → In progress → Scout worked on it → …")
    expect(st.who).toEqual(["A person", "Scout"])
    expect(st.agents).toEqual([{ id: "a1", name: "Scout" }])
  })

  it("never folds what is still going on", () => {
    const live = work(chain("w1", { routine_slug: undefined, running_runs: 1, first_activity: at(14, 10, 30), issues: crw1 }))
    const out = foldStories(mergeFeed([live], three()), NOW)
    expect(out[0].id).toBe("w1")
    expect(out[1].type).toBe("story")
  })

  it("folds updates to one page the same way", () => {
    const page = (id: string, h: number) =>
      changeFromEntry(entry(id, { ts: at(14, h), entry_type: "page.spec.changed", payload: { page_id: "pg1", page_title: "Weekly report" } }))!
    const st = foldStories([page("p3", 11), page("p2", 10), page("p1", 9)], NOW)[0] as StoryItem
    expect(st.type).toBe("story")
    expect(st.objectKind).toBe("artifact")
    expect(st.title).toBe("Weekly report")
    expect(st.flow).toBe("Changed → Changed → Changed")
  })

  it("leaves other kinds of change alone", () => {
    const cred = (id: string, h: number) =>
      changeFromEntry(entry(id, { ts: at(14, h), entry_type: "credential.revealed", payload: { credential_name: "GitHub" } }))!
    expect(foldStories([cred("c3", 11), cred("c2", 10), cred("c1", 9)], NOW).map((x) => x.type)).toEqual(["change", "change", "change"])
  })

  it("exports a story as its updates, one line each", () => {
    const csv = feedCsv(foldStories(three(), NOW))
    expect(csv.trim().split("\n")).toHaveLength(4)
    expect(csv).toContain("e1")
  })
})

describe("time range", () => {
  it("computes the presets", () => {
    expect(rangeBounds({ key: "7d" }, NOW).since).toBe(NOW - 7 * 86_400_000)
    expect(rangeBounds({ key: "today" }, NOW).since).toBe(new Date(2026, 9, 15).getTime())
    expect(rangeBounds({ key: "yday" }, NOW)).toEqual({ since: new Date(2026, 9, 14).getTime(), until: new Date(2026, 9, 15).getTime() })
    // Thursday → Monday 12 Oct.
    expect(rangeBounds({ key: "week" }, NOW).since).toBe(new Date(2026, 9, 12).getTime())
    expect(inRange(new Date(2026, 9, 14, 23).getTime(), { key: "yday" }, NOW)).toBe(true)
    expect(inRange(new Date(2026, 9, 15, 1).getTime(), { key: "yday" }, NOW)).toBe(false)
  })

  it("labels the dates the way the trigger shows them", () => {
    expect(rangeDates({ key: "7d" }, NOW)).toBe("8 – 15 Oct")
    expect(rangeDates({ key: "30d" }, NOW)).toBe(`${new Date(2026, 8, 15).toLocaleDateString("en-GB", { day: "numeric", month: "short" })} – 15 Oct`)
    expect(rangeDates({ key: "today" }, NOW)).toBe("15 Oct")
    expect(rangeDates({ key: "yday" }, NOW)).toBe("14 Oct")
    expect(rangeDates({ key: "1h" }, NOW)).toBe("07:16 – now")
  })

  it("widens the range just enough to include a linked run, never narrower than before", () => {
    const r7 = { key: "7d" as const }
    // Already inside: the same range comes back.
    expect(rangeIncluding(r7, new Date(2026, 9, 14).getTime(), NOW)).toBe(r7)
    // 10 days ago is outside 7 days: the next preset that holds it.
    expect(rangeIncluding(r7, new Date(2026, 9, 5).getTime(), NOW)).toEqual({ key: "30d" })
    expect(rangeIncluding({ key: "1h" }, NOW - 3 * 3_600_000, NOW)).toEqual({ key: "24h" })
    // Yesterday → a run this morning: 7 days holds both, Today would drop yesterday.
    expect(rangeIncluding({ key: "yday" }, new Date(2026, 9, 15, 7).getTime(), NOW)).toEqual({ key: "7d" })
    // Older than 30 days: a custom range from that day to now.
    expect(rangeIncluding(r7, new Date(2026, 8, 1, 13).getTime(), NOW)).toEqual({
      key: "custom",
      from: new Date(2026, 8, 1).getTime(),
      to: NOW,
    })
  })

  it("steps shorter and longer and stops at the ends", () => {
    expect(stepRange("7d", -1)).toBe("today")
    expect(stepRange("7d", 1)).toBe("30d")
    expect(stepRange("30d", 1)).toBeNull()
    expect(stepRange("1h", -1)).toBeNull()
    // A preset outside the steps steps from 7 days.
    expect(stepRange("week", 1)).toBe("30d")
  })
})

describe("old links land on the list", () => {
  const read = (qs: string) => parseFeedUrl(new URLSearchParams(qs))

  it.each([
    ["status=active", "running"],
    ["status=waiting", "waiting"],
    ["status=failed", "failed"],
    ["status=done", "completed"],
    ["view=need", "waiting"],
    ["view=live", "running"],
  ])("%s → %s", (qs, status) => {
    expect(read(qs).filters.status).toBe(status)
  })

  it("status=done maps to the Completed facet and writes back as status=completed", () => {
    const s = read("status=done")
    expect(feedUrlParams(s).toString()).toBe("status=completed")
  })

  it.each([
    ["lens=issues", "object"],
    ["lens=agents", "who"],
    ["lens=routines", "object"],
    ["lens=workflows", "time"],
  ])("%s → group by %s", (qs, group) => {
    expect(read(qs).group).toBe(group)
  })

  it("pipeline + run → routine object filter over 30 days with the run in the Peek", () => {
    const s = read("pipeline=triage&run=r1")
    expect(s.filters.routine).toBe("triage")
    expect(s.range.key).toBe("30d")
    expect(s.peek).toBe("r1")
    expect(feedUrlParams(s).toString()).toBe("pipeline=triage&run=r1")
  })

  it("mission → issue object filter", () => {
    expect(read("mission=m1").filters.issue).toBe("m1")
  })

  it("round-trips the canonical params", () => {
    const s: FeedUrlState = {
      filters: { ...EMPTY_FILTERS, status: "failed", agent: "a1", source: "webhook", q: "pr" },
      group: "who",
      bundle: false,
      range: { key: "24h" },
    }
    const back = parseFeedUrl(feedUrlParams(s))
    expect(back).toEqual({ ...s, peek: undefined })
  })

  it("writes nothing for the default list", () => {
    expect(feedUrlParams(read("")).toString()).toBe("")
  })
})

describe("earlier runs", () => {
  it("keeps the last 20 oldest first, with the success rate and the median of completed runs", () => {
    const recs = Array.from({ length: 22 }, (_, i) => ({
      id: `r${String(i).padStart(2, "0")}`,
      status: i % 5 === 0 ? "failed" : "completed",
      started_at: new Date(2026, 9, 1, i).toISOString(),
      duration_ms: (i + 1) * 1000,
    }))
    const h = runHistory(recs, "r21")
    expect(h.bars).toHaveLength(20)
    expect(h.bars[0].id).toBe("r02")
    expect(h.bars.at(-1)).toMatchObject({ id: "r21", current: true })
    // r02..r21: failed at 5, 10, 15, 20 → 16 of 20 completed.
    expect(h.successPct).toBe(80)
    // Completed durations 3,4,5,7,8,9,10,12,… s → median of the 8th and 9th (12 s, 13 s).
    expect(h.medianMs).toBe(12_500)
    expect(runHistory([], null)).toEqual({ bars: [], successPct: null, medianMs: null })
  })
})
