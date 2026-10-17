import { describe, it, expect } from "vitest"
import { readFileSync, existsSync } from "node:fs"
import { createHash } from "node:crypto"
import path from "node:path"

// Harbor 2 type and shape: Inter for the interface and JetBrains Mono for
// ids and code, both shipped with the app; cards at 10 px.

const root = path.resolve(__dirname, "../../..")
const read = (p: string) => readFileSync(path.join(root, p), "utf8")

describe("Harbor 2 fonts", () => {
  it("Inter is a bundled variable font with its licence and checksum", () => {
    const woff = path.join(root, "public/fonts/inter/InterVariable.woff2")
    expect(existsSync(woff)).toBe(true)
    expect(existsSync(path.join(root, "public/fonts/inter/OFL.txt"))).toBe(true)
    const sum = createHash("sha256").update(readFileSync(woff)).digest("hex")
    expect(read("public/fonts/inter/README.md")).toContain(sum)
  })

  it("the root layout loads Inter through next/font/local and exposes --font-inter", () => {
    const layout = read("app/layout.tsx")
    expect(layout).toMatch(/InterVariable\.woff2/)
    expect(layout).toMatch(/variable:\s*"--font-inter"/)
    expect(layout).toMatch(/inter\.variable/)
  })

  it("the sans stack starts with Inter in both themes", () => {
    const css = read("app/globals.css")
    const stacks = [...css.matchAll(/--font-sans:\s*([^;]+);/g)].map((m) => m[1])
    expect(stacks.length).toBeGreaterThanOrEqual(2)
    for (const s of stacks.filter((v) => !v.startsWith("var(--font-sans)"))) expect(s.startsWith("var(--font-inter")).toBe(true)
  })

  it("no stylesheet or layout fetches fonts from a third-party host", () => {
    for (const f of ["app/layout.tsx", "app/globals.css", "app/styles/accents.css"]) {
      expect(read(f), f).not.toMatch(/fonts\.googleapis|fonts\.gstatic|next\/font\/google|@import\s+url\(\s*["']?https?:/)
    }
  })
})

describe("Harbor 2 shape", () => {
  it("cards are 10 px at every text size and controls keep the 6/8/10/14 scale", () => {
    const css = read("app/globals.css")
    // A radius is drawn, not read: it does not grow with the text size
    // (0.625rem measured 9.29 px at Small, found by the Settings e2e).
    expect(css).toMatch(/--radius-card:\s*10px;/)
    expect(css).toMatch(/--radius:\s*0\.625rem;/)
  })
})

describe("Harbor 2 type scale", () => {
  const css = read("app/globals.css")
  it("the page title is 21 px at Compact (1.4rem on a 13/14 root)", () => {
    expect(css).toMatch(/--typo-title:\s*1\.4rem;/)
  })
  it("the smallest text never drops below the 11 px floor, even at Compact", () => {
    expect(css).toMatch(/--typo-micro:\s*max\(11px,\s*0\.6875rem\);/)
  })
})
