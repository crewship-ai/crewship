import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

// A touch target is 44 CSS px on a coarse pointer (`coarse:`, globals.css),
// at every text size. The spacing scale is neither: `--spacing` is 0.23rem,
// so `coarse:h-11` is 40.5 px at Standard, and the root shrinks at Small
// (the Harbor 2 default), where `coarse:h-12` is 41 px. A coarse size is
// written in px — `coarse:h-[44px]`, `coarse:min-h-[44px]` — so neither the
// scale nor the reader's text size can take it under the target.

const ROOT = path.resolve(__dirname, "../..")
const DIRS = ["app", "components", "lib", "hooks"]
// A coarse height/width/size from the spacing scale: coarse:h-11, coarse:min-w-12 …
const SCALE_SIZE = /\bcoarse:(?:min-)?(?:h|w|size)-(\d+(?:\.\d+)?)\b/g
// … or in rem/em, which shrink with the text size: coarse:h-[2.75rem].
const REM_SIZE = /\bcoarse:(?:min-)?(?:h|w|size)-\[[0-9.]+r?em\]/g

function files(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name)
    if (name === "node_modules" || name === "__tests__" || name === "generated" || name.startsWith(".")) continue
    const st = statSync(p)
    if (st.isDirectory()) files(p, out)
    else if (/\.tsx?$/.test(name) && !/\.(test|spec)\.tsx?$/.test(name)) out.push(p)
  }
  return out
}

describe("coarse-pointer tap targets", () => {
  it("are sized in px, not from the rem spacing scale, when they are meant to be a target", () => {
    const found: string[] = []
    for (const d of DIRS) {
      for (const f of files(path.join(ROOT, d))) {
        const src = readFileSync(f, "utf8")
        for (const m of src.matchAll(SCALE_SIZE)) {
          // 11 and up is a tap target (40 px and more on the scale); smaller
          // values are a coarse layout tweak (an icon, a gap), not a target.
          if (Number(m[1]) >= 11) found.push(`${path.relative(ROOT, f)}: ${m[0]}`)
        }
        for (const m of src.matchAll(REM_SIZE)) found.push(`${path.relative(ROOT, f)}: ${m[0]}`)
      }
    }
    expect(found).toEqual([])
  })
})
