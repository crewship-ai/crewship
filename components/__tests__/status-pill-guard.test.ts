import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

// KIT-02: StatusPill (components/ui/status-pill.tsx) is the only component
// that shows a status or a severity; Badge is for counts and neutral labels.
// The product had ~69 feature-local Chip/Pill/Badge components and ~20 local
// status → colour maps, each reading the same meaning a little differently.
// This guard keeps new ones from appearing: a component named *Pill or
// *StatusBadge declared outside components/ui fails unless it is listed
// below with the reason it is not a second status pill. A tone word goes
// through lib/status-tone, not a new map.

const ROOT = path.resolve(__dirname, "../..")
const DIRS = ["app", "components"]

const DECLARATION = /^\s*(?:export\s+)?(?:default\s+)?(?:function|const|let|class)\s+((?:[A-Z][A-Za-z0-9]*)?(?:Pill|StatusBadge))\b/gm

/** file → component names that are allowed, each with its reason. */
const ALLOWED: Record<string, Record<string, string>> = {
  "components/features/missions/mission-status-badge.tsx": {
    MissionStatusBadge: "renders StatusIconBadge → StatusPill; only the mission word and glyph are local",
    TaskStatusBadge: "renders StatusIconBadge → StatusPill; only the task word and glyph are local",
  },
  "components/features/credentials/provider-login-bits.tsx": {
    LoginStatusPill: "renders StatusPill; derives the word from the login's quota state",
  },
  "components/features/work/work-state-pill.tsx": {
    WorkStatePill: "renders StatusPill; the outcome → tone rule for work items (needs_reconciliation is warn, never red or green)",
  },
  "components/features/activity/source-pill.tsx": {
    SourcePill: "provenance link (issue, schedule, webhook), not a status",
  },
  "components/layout/create-surface.tsx": {
    CreateSurfacePill: "a form control (button) on the create surface, not a status",
  },
  "components/layout/status-pill.tsx": {
    SystemStatusPill: "the toolbar's system-health trigger that opens its popover; follow-up: render its face with StatusPill",
  },
  "components/features/settings/members/member-bits.tsx": {
    RolePill: "a neutral role label, deliberately colourless; follow-up: fold into Badge",
    RestrictedPill: "a neutral access-mode label; follow-up: fold into Badge",
  },
  "components/features/skills/skills-explorer.tsx": {
    CountPill: "a rail count with a selected state, not a status; follow-up: fold into Badge",
  },
  "components/features/activity-stream/activity-status-rows.tsx": {
    CountPill: "a rail count with a selected state, not a status; follow-up: fold into Badge",
  },
}

/** StatusBadge (the old Badge-based status pill) stays only where another team owns the caller. */
const STATUS_BADGE_IMPORT = /import\s*(?:type\s*)?\{[^}]*\bStatusBadge\b[^}]*\}\s*from\s*["']@\/components\/ui\/status-badge["']/
const STATUS_BADGE_ALLOWED = new Set([
  // Pages/Artifacts own this file; its type register pins the 12px house pill.
  "components/features/pages/panels/status-panel.tsx",
])

function files(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name)
    if (name === "node_modules" || name === "__tests__" || name.startsWith(".")) continue
    const st = statSync(p)
    if (st.isDirectory()) files(p, out)
    else if (/\.(tsx?|jsx?)$/.test(name) && !/\.(test|spec)\.[tj]sx?$/.test(name)) out.push(p)
  }
  return out
}

function sources(): { rel: string; src: string }[] {
  const out: { rel: string; src: string }[] = []
  for (const d of DIRS) {
    for (const f of files(path.join(ROOT, d))) {
      out.push({ rel: path.relative(ROOT, f).split(path.sep).join("/"), src: readFileSync(f, "utf8") })
    }
  }
  return out
}

export function localStatusComponents(all = sources()): { file: string; name: string }[] {
  const found: { file: string; name: string }[] = []
  for (const { rel, src } of all) {
    if (rel.startsWith("components/ui/")) continue
    for (const m of src.matchAll(DECLARATION)) found.push({ file: rel, name: m[1] })
  }
  return found
}

describe("status pill guard", () => {
  const all = sources()

  it("no feature-local *Pill or *StatusBadge component outside components/ui", () => {
    const unexpected = localStatusComponents(all)
      .filter(({ file, name }) => !ALLOWED[file]?.[name])
      .map(({ file, name }) => `${file}: ${name}`)
    expect(
      unexpected,
      "show a status with <StatusPill> (components/ui/status-pill) and map tone words through lib/status-tone; counts and neutral labels use <Badge>",
    ).toEqual([])
  })

  it("the allow-list holds no stale entries", () => {
    const present = new Set(localStatusComponents(all).map(({ file, name }) => `${file}:${name}`))
    const stale = Object.entries(ALLOWED).flatMap(([file, names]) =>
      Object.keys(names).filter((n) => !present.has(`${file}:${n}`)).map((n) => `${file}: ${n}`),
    )
    expect(stale, "remove entries for components that are gone").toEqual([])
  })

  it("StatusBadge is not imported outside the files another team owns", () => {
    const importers = all
      .filter(({ rel, src }) => !rel.startsWith("components/ui/") && STATUS_BADGE_IMPORT.test(src))
      .map(({ rel }) => rel)
      .filter((rel) => !STATUS_BADGE_ALLOWED.has(rel))
    expect(importers, "use <StatusPill> instead of <StatusBadge>").toEqual([])
  })

  it("catches a new local status pill", () => {
    const fake = [{ rel: "components/features/demo/thing.tsx", src: "function RunStatusPill() {}\nexport const HealthStatusBadge = () => null\nfunction StatusBadge() {}\nfunction FilterChip() {}" }]
    expect(localStatusComponents(fake).map((f) => f.name)).toEqual(["RunStatusPill", "HealthStatusBadge", "StatusBadge"])
  })
})
