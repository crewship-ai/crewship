import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"

// PAR-152 / PAR-153: card, section and table headers are sentence-case sans,
// and numerals (counts, KPI values, pills) are sans tabular; mono is for code,
// ids, hashes and log times. The crews area keeps to that.

const ROOT = join(__dirname, "..")
function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    if (n === "__tests__") return []
    return statSync(p).isDirectory() ? walk(p) : /\.tsx$/.test(n) ? [p] : []
  })
}
const files = walk(ROOT)
// Log levels in the dock are code-like output and stay as they are.
const LOG_FILES = /bottom-panel\/(exec|logs)-tab\.tsx$/

describe("crews typography", () => {
  it("no uppercase label in the area (log levels excepted)", () => {
    const hits: string[] = []
    for (const f of files) {
      if (LOG_FILES.test(f)) continue
      readFileSync(f, "utf8").split("\n").forEach((l, i) => { if (/\buppercase\b/.test(l) && /className|cn\(/.test(l) && !/max \d+ · uppercase/.test(l)) hits.push(`${f.slice(ROOT.length + 1)}:${i + 1}`) })
    }
    expect(hits).toEqual([])
  })
  it("tabular numerals are sans: font-mono and tabular-nums never share a class list", () => {
    const hits: string[] = []
    for (const f of files) {
      readFileSync(f, "utf8").split("\n").forEach((l, i) => { if (/font-mono[^"`']*tabular-nums|tabular-nums[^"`']*font-mono/.test(l)) hits.push(`${f.slice(ROOT.length + 1)}:${i + 1}`) })
    }
    expect(hits).toEqual([])
  })
})
