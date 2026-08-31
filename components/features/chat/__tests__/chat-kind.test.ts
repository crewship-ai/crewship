import { describe, it, expect } from "vitest"

// =============================================================================
// A routine step is not a conversation.
//
// Four writers put rows in `chats` — a person opening a thread, a routine
// minting one chat PER STEP, an issue starting work, an agent delegating — and
// the column merged all four into one activity-ordered list. On a workspace
// that runs routines that is not clutter, it is eviction: the per-agent page is
// ten rows, one five-step run writes five of them, and after two runs the
// thread somebody wrote yesterday is off the end of the query.
//
// These are the rules that keep the four apart, and the two properties that
// make the split safe to rely on: the partition is TOTAL (nothing can fall
// between the buckets and vanish) and it never reads a TITLE (which a rename
// can change out from under it).
// =============================================================================

import { CONCEPT_ICON } from "@/lib/concept-icons"

import {
  CHAT_SCOPES,
  KIND_META,
  classifyThread,
  groupByRecency,
  recencyBuckets,
  routineGroupOf,
  scopeForKind,
  scopeKindParam,
  type ChatKind,
} from "../chat-kind"

type Row = Parameters<typeof classifyThread>[0]
const row = (p: Partial<Row>): Row => ({ mode: "CHAT", origin: null, kind: null, ...p })

describe("classifyThread — mode first, then origin, then everything else is direct", () => {
  it("files an issue's chat by its mode, whatever its origin says", () => {
    // An issue runs its work in a MISSION-mode chat. Origin is not the
    // deciding column for one, and letting it be would file an issue a cron
    // kicked off under Routines.
    expect(classifyThread(row({ mode: "MISSION", origin: "CRON" }))).toBe("issue")
  })

  it("treats routine, cron and webhook as the same thing to a reader", () => {
    for (const origin of ["ROUTINE", "CRON", "WEBHOOK"]) {
      expect(classifyThread(row({ origin }))).toBe("routine")
    }
  })

  it("files agent-to-agent delegation as its own kind", () => {
    expect(classifyThread(row({ origin: "AGENT" }))).toBe("agent")
  })

  it("calls a person's chat direct whether or not the origin was recorded", () => {
    // NULL is the pre-migration state of every chat on an upgrading instance.
    // Guessing anything else for it would hide real conversations.
    expect(classifyThread(row({ origin: null }))).toBe("direct")
    expect(classifyThread(row({ origin: "UI" }))).toBe("direct")
    expect(classifyThread(row({ origin: "CLI" }))).toBe("direct")
  })

  it("is TOTAL — an origin nobody has thought of yet stays visible", () => {
    // The load-bearing default, and the reason `direct` is written as the
    // negation of the others rather than as an allowlist. A future origin
    // value, a hand-written row, a restore from an older schema: all of them
    // classify as something, and the something they classify as is the list
    // the reader is actually looking at. A row that belongs to no bucket is a
    // row nobody can find.
    expect(classifyThread(row({ origin: "SOMETHING_NEW" }))).toBe("direct")
    expect(classifyThread(row({ mode: "TASK", origin: undefined }))).toBe("direct")
  })

  it("prefers the server's verdict, so the list cannot disagree with the filter", () => {
    // `kind` is what `?kind=` used to build the page. Re-deriving it locally
    // would put a second opinion in the loop about a row the server already
    // judged — and the two opinions are exactly what would drift.
    expect(classifyThread(row({ kind: "routine", mode: "CHAT", origin: "UI" }))).toBe("routine")
  })

  it("ignores a kind the client does not know, rather than trusting it blind", () => {
    expect(classifyThread(row({ kind: "nonsense", origin: "AGENT" }))).toBe("agent")
  })

  it("never reads the title", () => {
    // A title is user-editable (`PATCH .../chats/{id}`), so a rule over it
    // reclassifies a row the moment somebody tidies its name — and misfiles a
    // human conversation the moment somebody calls one "Pipeline notes".
    const looksLikeARoutine = { ...row({ origin: "UI" }), title: "Pipeline notes · step two" }
    expect(classifyThread(looksLikeARoutine as Row)).toBe("direct")
  })
})

describe("scopes — three tabs over four kinds", () => {
  it("asks the server for every kind the scope claims to show", () => {
    expect(scopeKindParam("direct")).toBe("direct")
    expect(scopeKindParam("issue")).toBe("issue")
    // Delegation rides with routines: from the reader's side both are work
    // that happened without them typing, and a fourth equal-width tab in a
    // 280px column truncates its own label.
    expect(scopeKindParam("routine")).toBe("routine,agent")
  })

  it("leaves no kind unreachable", () => {
    const covered = new Set<ChatKind>(CHAT_SCOPES.flatMap((s) => s.kinds))
    expect([...covered].sort()).toEqual(["agent", "direct", "issue", "routine"])
  })

  it("answers which scope holds a kind, for every kind", () => {
    // The inverse lookup is what lets a deep link BRING the column to a
    // conversation instead of only letting somebody navigate to it by hand.
    // If it ever answered null for a real kind, arriving from an inbox item
    // would leave the reader looking at a list that does not contain what
    // they opened.
    expect(scopeForKind("direct")).toBe("direct")
    expect(scopeForKind("routine")).toBe("routine")
    expect(scopeForKind("agent")).toBe("routine")
    expect(scopeForKind("issue")).toBe("issue")
  })

  it("moves the column nowhere for a kind it does not recognise", () => {
    // Guessing a bucket is worse than leaving the column where the reader put
    // it — a wrong move looks exactly like a right one.
    expect(scopeForKind("something_new")).toBeNull()
    expect(scopeForKind("")).toBeNull()
  })
})

describe("icons come from the one map, never from memory", () => {
  // lib/concept-icons.ts exists because "the agent overview ended up showing
  // Routines as a Workflow, Skills as Sparkles and Tools as a Wrench, so the
  // same four concepts wore different faces depending on which screen you were
  // looking at". Its own instruction is to check the map before inventing an
  // icon — and this column shipped without doing that, showing Repeat2 for
  // Routines and Target for Issues while the nav rail two inches to the left
  // showed ScrollText and CircleDot.
  //
  // Asserting identity with CONCEPT_ICON rather than naming the glyphs: if the
  // product ever restyles Issues, the rail and this column change together and
  // this test stays true. Naming `CircleDot` here would make it the third
  // opinion instead of a guard against there being one.

  it("uses the rail's icon for every scope", () => {
    const byId = Object.fromEntries(CHAT_SCOPES.map((s) => [s.id, s.icon]))
    expect(byId.direct).toBe(CONCEPT_ICON.sessions)
    expect(byId.routine).toBe(CONCEPT_ICON.routines)
    expect(byId.issue).toBe(CONCEPT_ICON.issues)
  })

  it("uses the rail's icon for every kind badge", () => {
    expect(KIND_META.direct.icon).toBe(CONCEPT_ICON.sessions)
    expect(KIND_META.routine.icon).toBe(CONCEPT_ICON.routines)
    expect(KIND_META.issue.icon).toBe(CONCEPT_ICON.issues)
    // "Messages from other agents" is what the map already calls a delegation.
    expect(KIND_META.agent.icon).toBe(CONCEPT_ICON.peers)
  })

  it("names the same concept with the same glyph in both places", () => {
    // The scope strip and the row badge are two renderings of one partition.
    // They drifting apart would be the original defect at a smaller scale.
    for (const scope of CHAT_SCOPES) {
      expect(scope.icon).toBe(KIND_META[scope.kinds[0]].icon)
    }
  })
})

describe("groupByRecency", () => {
  const now = new Date("2026-08-31T09:00:00Z").getTime()
  const at = (r: { at: number }) => r.at
  const day = 86_400_000

  it("says nothing at all about a short list", () => {
    // Three headers over four rows is not structure, it is three lines of
    // chrome explaining a list you can already see.
    const rows = [{ at: now }, { at: now - day }]
    expect(groupByRecency(rows, at, now)).toEqual([{ label: null, rows }])
  })

  it("splits on local midnight, not on 'twenty-four hours ago'", () => {
    // A thread from 23:50 last night is not "today" because it is nine hours
    // old. A reader who says "yesterday" means the calendar day.
    const midnight = new Date(now)
    midnight.setHours(0, 0, 0, 0)
    const rows = [
      { at: now },
      { at: midnight.getTime() + 60_000 },
      { at: midnight.getTime() - 60_000 },
      { at: midnight.getTime() - day - 60_000 },
      { at: midnight.getTime() - 20 * day },
      { at: midnight.getTime() - 40 * day },
    ]
    const out = groupByRecency(rows, at, now)
    expect(out.map((g) => g.label)).toEqual(["Today", "Yesterday", "Earlier this week", "Earlier"])
    expect(out[0].rows).toHaveLength(2)
    expect(out[1].rows).toHaveLength(1)
  })

  it("loses nothing, including a row whose timestamp did not parse", () => {
    // `parseSessionTimestamp` answers 0 for an unparseable stamp. The last
    // bucket's bound is -Infinity precisely so that row lands in "Earlier"
    // instead of being dropped from a list somebody is using to find it.
    const rows = [{ at: now }, { at: now - day }, { at: 0 }, { at: 0 }, { at: 0 }, { at: 0 }]
    const out = groupByRecency(rows, at, now)
    expect(out.flatMap((g) => g.rows)).toHaveLength(rows.length)
  })

  it("emits no empty group", () => {
    const rows = Array.from({ length: 8 }, () => ({ at: now }))
    expect(groupByRecency(rows, at, now).map((g) => g.label)).toEqual(["Today"])
  })

  it("orders its bounds newest-first, which is what makes one pass enough", () => {
    const bounds = recencyBuckets(now).map((b) => b.from)
    expect([...bounds].sort((a, b) => b - a)).toEqual(bounds)
  })
})

describe("routineGroupOf", () => {
  it("splits on the LAST separator, so a routine may have one in its name", () => {
    // "Deploy · nightly" is a legal routine name; splitting on the first
    // separator would file each of its steps under a different heading.
    expect(routineGroupOf("Deploy · nightly · publish")).toEqual({
      group: "Deploy · nightly",
      step: "publish",
    })
  })

  it("keeps the legacy id-shaped title in one piece as its own group", () => {
    expect(routineGroupOf("Pipeline pln_abc · step summarize")).toEqual({
      group: "Pipeline pln_abc",
      step: "step summarize",
    })
  })

  it("degrades to a group of one when the title has no separator", () => {
    // This heuristic decides how rows are STACKED, never whether they show.
    // The degenerate case is the flat list it replaced.
    expect(routineGroupOf("Just a name")).toEqual({ group: "Just a name", step: null })
    expect(routineGroupOf(null)).toEqual({ group: "Untitled", step: null })
  })
})
