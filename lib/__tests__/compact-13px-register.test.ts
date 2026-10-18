import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// The v67 sizes are binding at the Compact text size (PRD TOK-06, M0-ROZMERY):
// a 13 px form field, a 13 px navigation name, a 13 px row and card title.
// Compact is a 14.857 px root (92.857 % of 16), so 13 px there is 0.875rem,
// the body size, and it grows to 14 px at Standard like everything else.
// 0.8125rem was 13 px only on the old 16 px root: at Compact it computes to
// 12.07 px, which is what the nightly Harbor 2 gate measured on the
// Activity rows, the Text size segment, the Inbox tiles and the user row.
const css = readFileSync(join(process.cwd(), "app/globals.css"), "utf8")
const ROOT_AT_COMPACT = 16 * 0.92857

function remOf(value: string): number {
  const v = value.trim()
  const typo = /^var\(--(typo-[a-z-]+)\)$/.exec(v)
  if (typo) {
    const def = new RegExp(`--${typo[1]}:\\s*([0-9.]+)rem;`).exec(css)
    if (!def) throw new Error(`--${typo[1]} is not defined in rem`)
    return Number(def[1])
  }
  const rem = /^([0-9.]+)rem$/.exec(v)
  if (!rem) throw new Error(`not a rem size: ${v}`)
  return Number(rem[1])
}

describe("13 px at Compact", () => {
  it("the form-control size (text-control) is 13 px at Compact", () => {
    const m = /--text-control:\s*([^;]+);/.exec(css)
    expect(m).not.toBeNull()
    expect(Math.round(remOf(m![1]) * ROOT_AT_COMPACT * 100) / 100).toBe(13)
  })

  it("the navigation register (.type-nav) is 13 px at Compact", () => {
    const m = /\.type-nav\s*\{[^}]*font-size:\s*([^;]+);/.exec(css)
    expect(m).not.toBeNull()
    expect(Math.round(remOf(m![1]) * ROOT_AT_COMPACT * 100) / 100).toBe(13)
  })
})
