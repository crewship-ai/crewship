import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs"
import path from "node:path"

// Harbor 2 token guard. Sizes, radii and colours come from tokens
// (app/globals.css); a raw `text-[13px]`, `rounded-[12px]`, `rounded-2xl` or a
// Tailwind palette colour (`bg-red-500`) bypasses them and is how pages drifted
// apart. Existing uses are recorded per file in the baseline next to this test;
// a file may only go DOWN. New files start at zero.
//
// Shrinking the baseline after a cleanup:
//   UPDATE_TOKEN_GUARD_BASELINE=1 pnpm exec vitest run lib/__tests__/design-token-guard.test.ts
// The update refuses to raise any count.

const ROOT = path.resolve(__dirname, "../..")
const BASELINE_FILE = path.join(__dirname, "design-token-guard.baseline.json")
const DIRS = ["app", "components"]

const PALETTE = "red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone"
export const RULES: Record<string, RegExp> = {
  "raw-text-size": /\btext-\[\d+(?:\.\d+)?(?:px|rem)\]/g,
  "raw-radius": /\brounded(?:-[trblse]{1,2})?-\[\d+(?:\.\d+)?(?:px|rem)\]/g,
  "large-radius": /\brounded(?:-[trblse]{1,2})?-(?:2xl|3xl)\b/g,
  "palette-colour": new RegExp(`\\b(?:bg|text|border|ring|fill|stroke|from|via|to|outline|decoration|divide|placeholder|caret|accent|shadow)-(?:${PALETTE})-\\d{2,3}\\b`, "g"),
}

type Counts = Record<string, Record<string, number>>

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

export function scan(): Counts {
  const counts: Counts = {}
  for (const d of DIRS) {
    for (const f of files(path.join(ROOT, d))) {
      const src = readFileSync(f, "utf8")
      for (const [rule, rx] of Object.entries(RULES)) {
        const n = src.match(rx)?.length ?? 0
        if (n > 0) ((counts[rule] ??= {})[path.relative(ROOT, f)] = n)
      }
    }
  }
  return counts
}

describe("design token guard", () => {
  const current = scan()
  const baseline: Counts = JSON.parse(readFileSync(BASELINE_FILE, "utf8"))

  it("no file uses more raw sizes, radii or palette colours than the baseline allows", () => {
    const grown: string[] = []
    for (const [rule, byFile] of Object.entries(current)) {
      for (const [file, n] of Object.entries(byFile)) {
        const allowed = baseline[rule]?.[file] ?? 0
        if (n > allowed) grown.push(`${rule}: ${file} has ${n}, baseline ${allowed}`)
      }
    }
    expect(grown, "use a token or a kit component instead (docs/ux/README.md › Harbor)").toEqual([])
  })

  it("the baseline only shrinks", () => {
    if (process.env.UPDATE_TOKEN_GUARD_BASELINE !== "1") return
    const next: Counts = {}
    for (const [rule, byFile] of Object.entries(baseline)) {
      for (const [file, allowed] of Object.entries(byFile)) {
        const n = current[rule]?.[file] ?? 0
        expect(n, `${rule}: ${file}`).toBeLessThanOrEqual(allowed)
        if (n > 0) (next[rule] ??= {})[file] = n
      }
    }
    const sorted: Counts = {}
    for (const rule of Object.keys(next).sort()) {
      sorted[rule] = {}
      for (const file of Object.keys(next[rule]).sort()) sorted[rule][file] = next[rule][file]
    }
    writeFileSync(BASELINE_FILE, JSON.stringify(sorted, null, 1) + "\n")
  })

  it("the rules catch what they are meant to", () => {
    const hit = (rule: string, s: string) => (s.match(RULES[rule]) ?? []).length
    expect(hit("raw-text-size", 'className="text-[13px] text-sm"')).toBe(1)
    expect(hit("raw-radius", "rounded-[12px] rounded-t-[6px] rounded-md")).toBe(2)
    expect(hit("large-radius", "rounded-2xl rounded-xl rounded-t-3xl")).toBe(2)
    expect(hit("palette-colour", "bg-red-500 text-emerald-600 bg-primary text-muted-foreground")).toBe(2)
  })
})
