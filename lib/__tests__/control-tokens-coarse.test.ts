import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

// A small Button (and every control that reads the control tokens: xs, sm, md,
// icon sizes, inputs, selects) is a 44 px target under `pointer: coarse`, and
// that comes from the TOKENS, in px - not from rem, which shrinks with the
// text size, and not per component. A fine pointer keeps the rem values.

const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8")
const button = readFileSync(path.resolve(__dirname, "../../components/ui/button.tsx"), "utf8")

/** The body of every top-level `@media (pointer: coarse) { ... }` block. */
function coarseBlocks(src: string): string[] {
  const out: string[] = []
  const re = /@media \(pointer: coarse\)\s*\{/g
  for (let m = re.exec(src); m; m = re.exec(src)) {
    let depth = 1
    let i = re.lastIndex
    while (i < src.length && depth > 0) { if (src[i] === "{") depth++; else if (src[i] === "}") depth--; i++ }
    out.push(src.slice(re.lastIndex, i - 1))
  }
  return out
}

describe("control tokens under a coarse pointer", () => {
  it("--control-xs/sm/md are 44px inside @media (pointer: coarse)", () => {
    const root = coarseBlocks(css).map((b) => b.match(/:root\s*\{([^}]*)\}/)?.[1] ?? "").join("\n")
    for (const t of ["xs", "sm", "md"]) expect(root, t).toMatch(new RegExp(`--control-${t}:\\s*44px`))
  })

  it("a fine pointer keeps the rem values, unchanged", () => {
    expect(css).toMatch(/--control-xs:\s*1\.6154rem/)
    expect(css).toMatch(/--control-sm:\s*1\.8846rem/)
    expect(css).toMatch(/--control-md:\s*2\.1538rem/)
    // Only the coarse block redefines them: outside it each token is set once.
    const outside = coarseBlocks(css).reduce((acc, b) => acc.replace(b, ""), css)
    expect(outside.match(/--control-sm:/g)).toHaveLength(1)
  })

  it("Button sm reads the token, with no per-component 44px", () => {
    expect(button).toMatch(/sm: "h-\[var\(--control-sm\)\]/)
    expect(button).toMatch(/"icon-sm": "size-\[var\(--control-sm\)\]"/)
    expect(button).not.toMatch(/coarse:/)
  })
})
