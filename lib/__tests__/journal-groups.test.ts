import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync } from "node:fs"
import { join, resolve } from "node:path"

import { activitySource, sourceEntryTypes } from "@/lib/activity-stream"
import { ENTRY_TYPES_BY_GROUP, entryTypesForGroups } from "@/lib/journal-groups"
import {
  GROUP_COLOR,
  GROUP_LABEL,
  GROUP_ORDER,
  GROUP_TO_BUNDLE,
  SEVERITY_COLOR,
  TYPE_PILL_LABEL,
  TYPE_TO_GROUP,
  groupOf,
  pillLabelOf,
  type EntryGroup,
} from "@/lib/journal-style"
import { JOURNAL_ENTRY_TYPES } from "@/lib/types/journal"

/* ------------------------------------------------------------------ *
 *  The Go corpus
 *
 *  A fully automatic Go→TS check IS practical here, so this reads the
 *  Go source rather than diffing against a checked-in generated list: a
 *  generated list is only as fresh as the last person who remembered to
 *  regenerate it, and the drift this test exists to catch is exactly the
 *  drift nobody remembered.
 *
 *  Reading internal/journal/types.go alone is not enough — that is what
 *  lib/__tests__/activity-stream.test.ts does, and it is why twelve types
 *  emitted from file-local constants (page.published, page.owner_transferred,
 *  onboarding.proposal_applied, …) stayed invisible to every frontend map.
 *  journal.EntryType is a plain string type with no central registry, so a
 *  call site is free to mint its own constant, and several do.
 *
 *  Four patterns cover every emit site in the tree:
 *    1. `EntryFoo EntryType = "foo.bar"`  — the shared vocabulary
 *    2. `journal.EntryType("foo.bar")`    — a file-local constant
 *    3. `Type: "foo.bar"` inside any `…Entry{…}` composite literal — a bare
 *       literal at the emit site. The needle is `Entry{` rather than
 *       `journal.Entry{` on purpose: the orchestrator emits through its own
 *       `orchestrator.JournalEntry{}` (converted at
 *       internal/server/journal_adapter.go) and those literals must count too.
 *    4. `emitJournal(ctx, "foo.bar", …)`  — the sidecar's own emit helper
 *       (internal/sidecar/journal_emit.go), whose callers all pass a literal.
 *
 *  Every type reachable through 3 and 4 today is also a types.go constant, so
 *  the corpus does not currently depend on them — but an orchestrator- or
 *  sidecar-only type would otherwise be dropped silently, and a ratchet that
 *  goes green while missing the drift it exists to catch is worse than none.
 * ------------------------------------------------------------------ */

const REPO_ROOT = process.cwd()

function goFiles(dir: string, out: string[] = []): string[] {
  let entries
  try {
    entries = readdirSync(dir, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    if (e.name === "node_modules" || e.name === ".git" || e.name === "testdata") continue
    const p = join(dir, e.name)
    if (e.isDirectory()) goFiles(p, out)
    // Tests invent types freely; only production emit sites count.
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(p)
  }
  return out
}

/**
 * `journal.Entry{`, the journal package's own unqualified `Entry{`, and the
 * orchestrator's `JournalEntry{`. The leading boundary is load-bearing:
 * without it this also matches `mcpCredEntry{`, whose `Type: "OAUTH2"` is
 * not a journal entry type at all.
 */
const ENTRY_LITERAL = /(?<![A-Za-z0-9_])(?:journal\.)?(?:Journal)?Entry\{/g

/** `Type: "x.y"` occurrences inside each brace-balanced composite literal. */
function typesInCompositeLiterals(src: string, sink: Set<string>) {
  for (const match of src.matchAll(ENTRY_LITERAL)) {
    const i = match.index
    let depth = 0
    let j = i + match[0].length - 1
    for (; j < src.length; j++) {
      if (src[j] === "{") depth++
      else if (src[j] === "}") {
        depth--
        if (depth === 0) {
          j++
          break
        }
      }
    }
    for (const m of src.slice(i, j).matchAll(/\bType:\s*"([^"]+)"/g)) sink.add(m[1])
  }
}

/** Every entry type one Go source file declares or emits. */
function scanned(src: string): string[] {
  const found = new Set<string>()
  for (const m of src.matchAll(/EntryType\s*(?:=\s*|\(\s*)"([^"]+)"/g)) found.add(m[1])
  typesInCompositeLiterals(src, found)
  for (const m of src.matchAll(/\bemitJournal\(\s*[\w.]+\s*,\s*"([^"]+)"/g)) found.add(m[1])
  return [...found]
}

const GO_ENTRY_TYPES: string[] = (() => {
  const found = new Set<string>()
  for (const dir of ["internal", "cmd", "ee"]) {
    for (const file of goFiles(resolve(REPO_ROOT, dir))) {
      for (const t of scanned(readFileSync(file, "utf8"))) found.add(t)
    }
  }
  return [...found].sort()
})()

describe("the Go corpus scan", () => {
  // Ratchet: a moved file or a broken regex must fail loudly rather than
  // making every coverage assertion below vacuously true.
  it("finds the backend entry types at all", () => {
    expect(GO_ENTRY_TYPES.length).toBeGreaterThan(130)
  })

  it("picks up types declared outside internal/journal/types.go", () => {
    // One of each declaration shape, so a regression in the scan is a
    // failure here and not a silent narrowing of the corpus.
    expect(GO_ENTRY_TYPES).toContain("page.owner_transferred") // file-local const
    expect(GO_ENTRY_TYPES).toContain("keeper.rule_auto_tuned") // inline conversion
    expect(GO_ENTRY_TYPES).toContain("memory.priority_changed") // types.go, promoted
  })

  it("reads the orchestrator's and the sidecar's own emit shapes", () => {
    // Both currently emit only types.go constants, so these would still
    // pass with the scan narrowed back to `journal.Entry{` — the point is
    // that the shapes are covered before a type appears that needs them.
    const orchestrator = readFileSync(
      resolve(REPO_ROOT, "internal/orchestrator/orchestrator_run.go"),
      "utf8",
    )
    expect(orchestrator).toMatch(/JournalEntry\{/)
    expect(scanned(orchestrator)).toContain("chat.user_message")

    const sidecar = readFileSync(resolve(REPO_ROOT, "internal/sidecar/memory_write.go"), "utf8")
    expect(sidecar).toMatch(/emitJournal\(/)
    expect(scanned(sidecar).some((t) => t.startsWith("memory."))).toBe(true)
  })

  it("yields only dotted lower-case type names", () => {
    expect(GO_ENTRY_TYPES.filter((t) => !/^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$/.test(t))).toEqual([])
  })
})

/* ------------------------------------------------------------------ *
 *  Coverage — the drift ratchet
 * ------------------------------------------------------------------ */

describe("entry-type coverage", () => {
  it("gives every backend entry type a group other than 'other'", () => {
    // "other" is not excludable server-side (entryTypesForGroups returns []
    // for it), so an unmapped type cannot be filtered out of a busy
    // workspace's 5,000-entry window at all.
    expect(GO_ENTRY_TYPES.filter((t) => groupOf(t) === "other")).toEqual([])
  })

  it("lists every backend entry type in ENTRY_TYPES_BY_GROUP", () => {
    const listed = new Set(Object.values(ENTRY_TYPES_BY_GROUP).flat())
    expect(GO_ENTRY_TYPES.filter((t) => !listed.has(t))).toEqual([])
  })

  it("mirrors every backend entry type in JOURNAL_ENTRY_TYPES", () => {
    const front = new Set<string>(JOURNAL_ENTRY_TYPES as readonly string[])
    expect(GO_ENTRY_TYPES.filter((t) => !front.has(t))).toEqual([])
  })

  it("maps no type the backend cannot emit", () => {
    const go = new Set(GO_ENTRY_TYPES)
    expect(Object.keys(TYPE_TO_GROUP).filter((t) => !go.has(t))).toEqual([])
  })

  it("gives every backend entry type an activity facet that claims it", () => {
    // activity-stream.test.ts asserts the same thing against its own scan of
    // internal/journal/types.go only, so it cannot see a file-local type: all
    // twelve added here were falling into the System facet unclaimed. This
    // assertion lives with the wider scan so that gap cannot reopen.
    const unclaimed = GO_ENTRY_TYPES.filter(
      (t) => activitySource(t) === "system" && !sourceEntryTypes("system").includes(t),
    )
    expect(unclaimed).toEqual([])
  })

  it("keeps routine activity on its own chip", () => {
    // pipeline.* IS routines, and it had no chip at all — the whole reason
    // the `routine` group exists.
    expect(groupOf("pipeline.run.started")).toBe("routine")
    expect(groupOf("pipeline.step.container_ready")).toBe("routine")
    expect(groupOf("automation.throttled")).toBe("routine")
  })
})

/* ------------------------------------------------------------------ *
 *  The two duplicated maps must agree
 * ------------------------------------------------------------------ */

describe("ENTRY_TYPES_BY_GROUP ↔ TYPE_TO_GROUP", () => {
  it("keys ENTRY_TYPES_BY_GROUP by exactly the EntryGroup union", () => {
    // GROUP_ORDER is the runtime witness of the union — the type itself
    // cannot be enumerated at runtime.
    expect(Object.keys(ENTRY_TYPES_BY_GROUP).sort()).toEqual([...GROUP_ORDER].sort())
  })

  it("files each listed type under the group journal-style.ts agrees with", () => {
    const wrong: string[] = []
    for (const [group, types] of Object.entries(ENTRY_TYPES_BY_GROUP)) {
      for (const t of types) {
        if (groupOf(t) !== group) wrong.push(`${t}: journal-groups says ${group}, groupOf says ${groupOf(t)}`)
      }
    }
    expect(wrong).toEqual([])
  })

  it("lists every TYPE_TO_GROUP entry under its own group", () => {
    // The direction that hook.dispatch_error failed: present in the
    // server-side `system` exclusion list, absent from TYPE_TO_GROUP.
    const missing: string[] = []
    for (const [t, group] of Object.entries(TYPE_TO_GROUP)) {
      if (!ENTRY_TYPES_BY_GROUP[group].includes(t)) missing.push(`${t} (group ${group})`)
    }
    expect(missing).toEqual([])
  })

  it("claims each type for exactly one group", () => {
    const seen = new Map<string, string>()
    for (const [group, types] of Object.entries(ENTRY_TYPES_BY_GROUP)) {
      for (const t of types) {
        expect(seen.has(t), `${t} claimed by ${seen.get(t)} and ${group}`).toBe(false)
        seen.set(t, group)
      }
    }
  })

  it("leaves 'other' empty so muting it never excludes everything", () => {
    expect(ENTRY_TYPES_BY_GROUP.other).toEqual([])
  })
})

/* ------------------------------------------------------------------ *
 *  Every group is renderable
 * ------------------------------------------------------------------ */

describe("EntryGroup tables", () => {
  const tables: [string, Record<string, unknown>][] = [
    ["GROUP_COLOR", GROUP_COLOR],
    ["GROUP_LABEL", GROUP_LABEL],
    ["GROUP_TO_BUNDLE", GROUP_TO_BUNDLE],
    ["ENTRY_TYPES_BY_GROUP", ENTRY_TYPES_BY_GROUP],
  ]

  it.each(tables)("%s covers exactly GROUP_ORDER", (_name, table) => {
    expect(Object.keys(table).sort()).toEqual([...GROUP_ORDER].sort())
  })

  it("lists each group in GROUP_ORDER once", () => {
    expect(new Set(GROUP_ORDER).size).toBe(GROUP_ORDER.length)
  })

  it("gives every group but the two grey catch-alls its own colour", () => {
    const byColour = new Map<string, EntryGroup[]>()
    for (const g of GROUP_ORDER) {
      const c = GROUP_COLOR[g]
      byColour.set(c, [...(byColour.get(c) ?? []), g])
    }
    const shared = [...byColour.entries()].filter(([, gs]) => gs.length > 1)
    // system and other are deliberately the same neutral grey.
    expect(shared.map(([, gs]) => gs.sort().join("+"))).toEqual(["other+system"])
  })

  it("adds no new group dot that borrows a severity colour", () => {
    // The severity bar and the type pill sit in the same row, so a shared
    // hex makes one read as the other. `approval` has been amber-400
    // (SEVERITY_COLOR.warn) since the palette shipped; recolouring it is a
    // visual change for an existing chip and does not belong in this fix.
    // Pinning the exception rather than dropping the assertion keeps the
    // debt visible and still fails on a NEW clash — which is why the
    // `page` group is #c4b5fd and not violet-400.
    const severity = new Set(Object.values(SEVERITY_COLOR))
    const clashes = GROUP_ORDER.filter((g) => severity.has(GROUP_COLOR[g]))
    expect(clashes).toEqual(["approval"])
  })

  it("gives every group a non-empty label", () => {
    expect(GROUP_ORDER.filter((g) => !GROUP_LABEL[g])).toEqual([])
  })
})

/* ------------------------------------------------------------------ *
 *  Server-side exclusion
 * ------------------------------------------------------------------ */

describe("entryTypesForGroups", () => {
  it("returns nothing when nothing is muted", () => {
    expect(entryTypesForGroups(new Set())).toEqual([])
  })

  it("expands a muted group to its own types", () => {
    expect(entryTypesForGroups(new Set<EntryGroup>(["chat"])).sort()).toEqual(
      [...ENTRY_TYPES_BY_GROUP.chat].sort(),
    )
  })

  it("expands the groups that could not be muted before", () => {
    // audit, provisioning and chat had no chip to click at all, so this
    // path was unreachable for them.
    for (const g of ["audit", "provisioning", "chat", "routine", "page"] as EntryGroup[]) {
      expect(entryTypesForGroups(new Set<EntryGroup>([g])).length).toBeGreaterThan(0)
    }
  })

  it("stays client-side for 'other'", () => {
    expect(entryTypesForGroups(new Set<EntryGroup>(["other"]))).toEqual([])
    // …and does not swallow the rest of the set with it.
    expect(entryTypesForGroups(new Set<EntryGroup>(["other", "file"]))).toEqual(["file.written"])
  })
})

describe("pillLabelOf", () => {
  /*
   * There is deliberately NO "every backend type has a pill label" test to
   * pair with the group/ENTRY_TYPES_BY_GROUP coverage above. #2213 replaces
   * the type pill in logs-list.tsx — pillLabelOf's only consumer — with an
   * icon plus the full dotted entry_type, so ratcheting this table would
   * demand ~65 abbreviations for a surface that is being retired, and the
   * abbreviation is lossier than the name it replaces (all fourteen
   * `pipeline.*` types share a stem the short form cannot keep). The
   * fallback below is what the newly-mapped types use, and it is the same
   * thing #2213 shows for everything.
   */
  it("falls back to the raw type for a type with no abbreviation", () => {
    expect(pillLabelOf("invented.later")).toBe("invented.later")
    // Including the ones #2207 mapped: they get a group and a colour, not a pill.
    expect(pillLabelOf("pipeline.step.container_ready")).toBe("pipeline.step.container_ready")
    expect(pillLabelOf("page.owner_transferred")).toBe("page.owner_transferred")
  })

  it("keeps the labels that do exist short enough for the row", () => {
    const long = Object.entries(TYPE_PILL_LABEL).filter(([, label]) => label.length > 16)
    expect(long).toEqual([])
  })

  it("abbreviates only types the backend can emit", () => {
    const go = new Set(GO_ENTRY_TYPES)
    expect(Object.keys(TYPE_PILL_LABEL).filter((t) => !go.has(t))).toEqual([])
  })
})
