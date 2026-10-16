import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join, relative } from "node:path"

// The browser's confirm() looks like an error from the browser, cannot be
// styled or read as part of the page, blocks the thread and cannot be
// answered in happy-dom. Every confirmation in app/ and components/ goes
// through ConfirmDialog (or its promise form, useConfirm) instead (#3342).

const ROOTS = ["app", "components"]

function sourceFilesUnder(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === ".next" || entry === "__tests__") continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) out.push(...sourceFilesUnder(full))
    else if (/\.(ts|tsx|js|jsx|mjs)$/.test(entry) && !/\.(test|spec)\.[jt]sx?$/.test(entry)) out.push(full)
  }
  return out
}

/** Drop comments so prose that names the old call does not count. */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`\\])\/\/.*$/gm, "$1")
}

/** Each line that calls the native prompt, as `line: text`. */
function nativeConfirmCalls(source: string): string[] {
  const pattern = /\b(?:window|globalThis|self)\s*\.\s*confirm\b|(?<![\w.$])confirm\s*\(/
  return stripComments(source)
    .split("\n")
    .flatMap((line, i) => (pattern.test(line) ? [`${i + 1}: ${line.trim()}`] : []))
}

describe("no native confirm()", () => {
  it("recognises the forms the app used", () => {
    expect(nativeConfirmCalls(`if (!window.confirm("Delete?")) return`)).toHaveLength(1)
    expect(nativeConfirmCalls(`if (!confirm("Delete?")) return`)).toHaveLength(1)
    expect(nativeConfirmCalls(`const ok = globalThis.confirm(msg)`)).toHaveLength(1)
    expect(nativeConfirmCalls(`const allow = () => window.confirm`)).toHaveLength(1)
    // Not the browser's prompt.
    expect(nativeConfirmCalls(`await ask.confirm({ title })`)).toHaveLength(0)
    expect(nativeConfirmCalls(`const ok = await confirmDelete()`)).toHaveLength(0)
    expect(nativeConfirmCalls(`// replaces confirm() here`)).toHaveLength(0)
    expect(nativeConfirmCalls(`/** replaces the browser's confirm() */`)).toHaveLength(0)
    expect(nativeConfirmCalls(`if (action.confirm && open) run()`)).toHaveLength(0)
  })

  it("appears nowhere in app/ or components/ outside tests", () => {
    const offenders: string[] = []
    for (const root of ROOTS) {
      for (const file of sourceFilesUnder(join(process.cwd(), root))) {
        for (const hit of nativeConfirmCalls(readFileSync(file, "utf8"))) {
          offenders.push(`${relative(process.cwd(), file)}:${hit}`)
        }
      }
    }
    expect(offenders, "use ConfirmDialog or useConfirm from components/ui instead").toEqual([])
  })
})
