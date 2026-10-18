// PAR-152 / PAR-153 (Harbor 2 v67): card, section and group headers are
// sentence-case sans, not uppercase mono; numerals in counts, KPI tiles and
// pills are sans tabular. Mono stays for code, ids, hashes and log times.
import { readdirSync, readFileSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, it } from "vitest"

const DIR = join(__dirname, "..")
const files = readdirSync(DIR).filter((f) => /\.tsx?$/.test(f))

function code(src: string): string[] {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ""))
    .split("\n")
    .map((l) => (/^\s*\/\//.test(l) ? "" : l))
}

describe("credentials typography (v67)", () => {
  it("has no uppercase or eyebrow class", () => {
    const hits: string[] = []
    for (const f of files) code(readFileSync(join(DIR, f), "utf8")).forEach((l, i) => {
      if (/className|cn\(/.test(l) && /(^|[\s"'`])uppercase([\s"'`]|$)/.test(l)) hits.push(`${f}:${i + 1}`)
      if (/["'`\s]eyebrow["'`\s]/.test(l) && /className/.test(l)) hits.push(`${f}:${i + 1}`)
    })
    expect(hits).toEqual([])
  })

  it("has no mono numerals (font-mono with tabular-nums)", () => {
    const hits: string[] = []
    for (const f of files) code(readFileSync(join(DIR, f), "utf8")).forEach((l, i) => {
      if (/font-mono/.test(l) && /tabular-nums/.test(l)) hits.push(`${f}:${i + 1}`)
    })
    expect(hits).toEqual([])
  })
})
