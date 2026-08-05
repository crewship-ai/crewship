import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"

// A button that paints its label in its own background colour.
//
// This shipped twice. The waitpoint Approve was `bg-warn ... text-warn`;
// it was fixed, and the routine-governance Approve — a different
// component, same two classes — kept shipping a blank orange rectangle
// for another day. Both were reported by a human looking at the screen,
// because nothing else can see it: the element exists, the accessible
// name is correct, every DOM query finds it, and `getByRole("button",
// { name: /approve/i })` passes while a user sees nothing.
//
// So the check has to be on the source, and it has to cover every file
// rather than the one that was just fixed. That is the whole lesson of
// the second occurrence.
//
// Tinted backgrounds are fine and everywhere: `bg-warn/15 text-warn` is
// the standard pill. Only a SOLID background paired with its own
// full-strength text is invisible, so only that is flagged.

const TOKENS = ["warn", "success", "destructive", "primary", "info", "notice", "purple"]

function tsxFilesUnder(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === ".next" || entry === "__tests__") continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) out.push(...tsxFilesUnder(full))
    else if (entry.endsWith(".tsx")) out.push(full)
  }
  return out
}

/**
 * Class strings pairing a solid `bg-<token>` with a bare
 * `text-<token>`.
 *
 * The lookaheads matter. `bg-warn/15` is tinted and legible, so the
 * background must not be followed by `/`. And `text-primary-foreground`
 * is the CORRECT pairing for `bg-primary`, so the text token must not
 * be followed by `-` either — without that, every correct button in the
 * kit reports as a bug and the test gets deleted.
 */
function offendingClassStrings(source: string): string[] {
  const hits: string[] = []
  // Any quoted or backticked run of class-ish text.
  for (const m of source.matchAll(/["'`]([^"'`\n]{0,600})["'`]/g)) {
    const s = m[1]
    for (const t of TOKENS) {
      const bg = new RegExp(`\\bbg-${t}(?![\\w/-])`)
      const text = new RegExp(`\\btext-${t}(?![\\w-])`)
      if (bg.test(s) && text.test(s)) hits.push(s.trim())
    }
  }
  return hits
}

describe("no button paints its label in its own background colour", () => {
  const roots = ["components", "app"]

  it("has no solid bg-<token> paired with a bare text-<token>", () => {
    const offenders: string[] = []
    for (const root of roots) {
      for (const file of tsxFilesUnder(root)) {
        const src = readFileSync(file, "utf8")
        for (const s of offendingClassStrings(src)) {
          offenders.push(`${file}: ${s}`)
        }
      }
    }
    expect(offenders, offenders.join("\n")).toEqual([])
  })
})

describe("the detector itself", () => {
  // A guard test that cannot fail is worse than no guard test, so the
  // detector is checked against the two real strings that shipped and
  // against the patterns it must not flag.
  it("catches the two that actually shipped", () => {
    expect(
      offendingClassStrings('"h-8 gap-1.5 bg-warn px-3 text-sm font-semibold text-warn hover:bg-warn"'),
    ).toHaveLength(1)
    expect(
      offendingClassStrings('"h-8 gap-1.5 bg-warn px-3.5 text-xs font-semibold text-warn"'),
    ).toHaveLength(1)
  })

  it("leaves tinted pills alone", () => {
    expect(offendingClassStrings('"bg-warn/15 text-warn"')).toEqual([])
    expect(offendingClassStrings('"bg-success/20 text-success"')).toEqual([])
  })

  it("leaves the kit's correct pairings alone", () => {
    expect(offendingClassStrings('"bg-primary text-primary-foreground hover:bg-primary/90"')).toEqual([])
    expect(offendingClassStrings('"bg-warn text-background"')).toEqual([])
  })
})
