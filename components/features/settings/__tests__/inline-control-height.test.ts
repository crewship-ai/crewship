import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import { join } from "node:path"

import { inlineControl } from "../shared"

/**
 * #3487 SET-06 (PRD TOK-06): a control inside a table or list row is "one
 * step smaller" than a settings field — that step is the small control token,
 * --control-sm (28 px at Small), the same height as a small Button. The old
 * `h-7` came from the spacing scale (--spacing 0.23rem → ~24 px at Small), so
 * an inline select sat 4 px lower than the small button beside it.
 */
describe("inline control height", () => {
  it("is the small control token, not the spacing scale", () => {
    expect(inlineControl).toContain("h-[var(--control-sm)]")
    expect(inlineControl).not.toMatch(/(^|\s)h-7(\s|$)/)
    // A finger still gets 44 px.
    expect(inlineControl).toContain("coarse:h-[2.75rem]")
  })

  it("no small Button in Settings is squeezed below the token with h-7 / h-6", () => {
    const root = join(process.cwd(), "components/features/settings")
    const files: string[] = []
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) { if (name !== "__tests__") walk(p) }
        else if (p.endsWith(".tsx")) files.push(p)
      }
    }
    walk(root)
    const offenders: string[] = []
    for (const f of files) {
      readFileSync(f, "utf8").split("\n").forEach((line, i) => {
        if (/<Button\b/.test(line) && /size="sm"/.test(line) && /className="[^"]*(?<![\w:-])h-[67](?![\w.\-[])/.test(line)) {
          offenders.push(`${f.slice(root.length + 1)}:${i + 1}`)
        }
      })
    }
    expect(offenders).toEqual([])
  })
})
