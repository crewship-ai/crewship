import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

// Harbor 2 (v67): card / section / table / panel-group headers and field
// labels are sentence-case sans; numerals are sans with tabular figures. Mono
// is for code, ids, hashes and log timestamps. The mono + UPPERCASE micro
// label ("eyebrow") is retired, so:
//
//  1. the kit utilities that used to be it (`.eyebrow`, `.type-section`,
//     `.type-page-label`) must not declare `text-transform: uppercase` or the
//     mono family any more, and
//  2. no class string in components/** or app/** may carry `uppercase` AND
//     `font-mono`.
//
// BASELINE is shrink-only: a file may have fewer offenders than listed, never
// more, and an entry that reaches zero must be deleted. Area agents burn it
// down when they convert their files. The kit (components/ui, components/layout)
// has no entries and must stay clean.

const ROOT = path.resolve(__dirname, "../..")
const DIRS = ["components", "app"]

const BASELINE: Record<string, number> = {
  "app/(dashboard)/admin/tabs/admin-kit.tsx": 1,
  "components/admin/member-capabilities.tsx": 1,
  "components/features/activity-stream/activity-detail.tsx": 2,
  "components/features/activity-stream/activity-run-page.tsx": 1,
  "components/features/activity-stream/activity-run-pieces.tsx": 1,
  "components/features/activity-stream/activity-work-page.tsx": 1,
  "components/features/activity-stream/workflow-page.tsx": 2,
  "components/features/activity/trace-step-node.tsx": 1,
  "components/features/admin/backups/backups-console.tsx": 1,
  "components/features/admin/backups/backups-kit.tsx": 1,
  "components/features/admin/backups/backups-page.tsx": 1,
  "components/features/admin/backups/data-retention.tsx": 1,
  "components/features/admin/security/security-activity.tsx": 1,
  "components/features/chat/right-panel-tabs/agent-artifacts-tab.tsx": 1,
  "components/features/issues/issue-card-detail.tsx": 3,
  "components/features/issues/tiptap-editor-toolbar.tsx": 1,
  "components/features/journal/journal-entry-card.tsx": 1,
  "components/features/journal/runs-view.tsx": 2,
  "components/features/logs/logs-panel.tsx": 1,
  "components/features/logs/logs-toolbar.tsx": 1,
  "components/features/paymaster/time-range-picker.tsx": 1,
  "components/features/paymaster/top-spenders-table.tsx": 1,
  "components/features/settings/audit-log/audit-time.tsx": 2,
  "components/features/settings/crew-links/crew-profile.tsx": 2,
  "components/features/settings/hooks/hook-editor.tsx": 1,
  "components/features/settings/hooks/hooks-page.tsx": 1,
  "components/features/settings/members/member-detail.tsx": 1,
  "components/features/settings/members/members-page.tsx": 1,
  "components/features/settings/members/people-table.tsx": 3,
  "components/features/settings/sections/crew-audit-section.tsx": 1,
  "components/layout/drill-page.tsx": 1,
  "components/layout/sidebar-kit.tsx": 2,
  "components/layout/sub-bar.tsx": 1,
}

const STRING_LITERAL = /"([^"\n]*)"|'([^'\n]*)'|`([^`]*)`/g
const UPPER = /(?<![\w-])uppercase(?![\w-])/
const MONO = /(?<![\w-])font-mono(?![\w-])/

function walk(dir: string, out: string[]): void {
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name === "__tests__" || name.startsWith(".")) continue
    const full = path.join(dir, name)
    const st = statSync(full)
    if (st.isDirectory()) walk(full, out)
    else if (/\.(ts|tsx)$/.test(name) && !/\.test\.tsx?$/.test(name)) out.push(full)
  }
}

function offenders(src: string): number {
  let n = 0
  for (const m of src.matchAll(STRING_LITERAL)) {
    if (UPPER.test(m[0]) && MONO.test(m[0])) n++
  }
  return n
}

function cssRule(css: string, selector: string): string {
  const i = css.indexOf(`${selector} {`)
  if (i < 0) return ""
  return css.slice(i, css.indexOf("}", i))
}

describe("mono-caps micro labels are retired", () => {
  it("kit utilities are sentence-case sans", () => {
    const css = readFileSync(path.join(ROOT, "app/globals.css"), "utf8")
    for (const sel of [".eyebrow", ".type-section", ".type-page-label"]) {
      const rule = cssRule(css, sel)
      expect(rule, `${sel} is defined`).not.toBe("")
      expect(rule, `${sel} uppercases`).not.toMatch(/text-transform:\s*uppercase/)
      expect(rule, `${sel} uses mono`).not.toMatch(/font-family:\s*var\(--font-mono\)/)
    }
  })

  it("no class string combines uppercase and font-mono beyond the baseline", () => {
    const files: string[] = []
    for (const d of DIRS) walk(path.join(ROOT, d), files)
    const rel = (f: string) => path.relative(ROOT, f).split(path.sep).join("/")
    const grown: string[] = []
    const stale: string[] = []
    const seen = new Set<string>()
    for (const f of files) {
      const key = rel(f)
      const n = offenders(readFileSync(f, "utf8"))
      const allowed = BASELINE[key] ?? 0
      if (n > allowed) grown.push(`${key}: ${n} > baseline ${allowed}`)
      if (key in BASELINE) {
        seen.add(key)
        if (n < allowed) stale.push(`${key}: ${n} < baseline ${allowed} (lower it${n === 0 ? " / delete the entry" : ""})`)
      }
    }
    for (const key of Object.keys(BASELINE)) if (!seen.has(key)) stale.push(`${key}: file gone (delete the entry)`)
    expect(grown, "new uppercase+font-mono class strings").toEqual([])
    expect(stale, "baseline is shrink-only; tighten it").toEqual([])
  })

  it("the kit (components/ui, components/layout) has no baseline entries", () => {
    const kit = Object.keys(BASELINE).filter((k) => /^components\/(ui|layout)\//.test(k))
    expect(kit).toEqual([])
  })
})
