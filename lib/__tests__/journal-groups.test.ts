import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync } from "node:fs"
import { join, resolve } from "node:path"

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
 *  Three patterns cover every emit site in the tree:
 *    1. `EntryFoo EntryType = "foo.bar"`         — the shared vocabulary
 *    2. `journal.EntryType("foo.bar")`           — a file-local constant
 *    3. `Type: "foo.bar"` inside `journal.Entry{…}` — a literal at the emit
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

/** `Type: "x.y"` occurrences inside each brace-balanced composite literal. */
function typesInCompositeLiterals(src: string, needle: string, sink: Set<string>) {
  let i = 0
  while ((i = src.indexOf(needle, i)) !== -1) {
    let depth = 0
    let j = i + needle.length - 1
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
    i = j
  }
}

const GO_ENTRY_TYPES: string[] = (() => {
  const found = new Set<string>()
  for (const dir of ["internal", "cmd", "ee"]) {
    for (const file of goFiles(resolve(REPO_ROOT, dir))) {
      const src = readFileSync(file, "utf8")
      const rel = file.slice(REPO_ROOT.length + 1)
      for (const m of src.matchAll(/EntryType\s*(?:=\s*|\(\s*)"([^"]+)"/g)) found.add(m[1])
      typesInCompositeLiterals(src, "journal.Entry{", found)
      // Inside the journal package itself the literal is unqualified.
      if (rel.startsWith(join("internal", "journal"))) typesInCompositeLiterals(src, " Entry{", found)
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
    // One of each non-shared declaration shape, so a regression in the
    // scan is a failure here and not a silent narrowing of the corpus.
    expect(GO_ENTRY_TYPES).toContain("page.owner_transferred") // file-local const
    expect(GO_ENTRY_TYPES).toContain("keeper.rule_auto_tuned") // inline conversion
    expect(GO_ENTRY_TYPES).toContain("memory.priority_changed") // types.go, promoted
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

  it("gives every backend entry type a short pill label", () => {
    expect(GO_ENTRY_TYPES.filter((t) => pillLabelOf(t) === t)).toEqual([])
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
  it("falls back to the raw type for something the backend has not shipped yet", () => {
    expect(pillLabelOf("invented.later")).toBe("invented.later")
  })

  it("keeps pill labels short enough for the row", () => {
    const long = Object.entries(TYPE_PILL_LABEL).filter(([, label]) => label.length > 16)
    expect(long).toEqual([])
  })
})
