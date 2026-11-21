import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join, relative, sep } from "node:path"

// Guards for the causes behind the phone audit (#5621). Each one was found by
// looking at a phone, not by a test: the element exists, its name is right,
// every query finds it — and on a touch screen it is invisible, 44px where
// 112px was written, or off the right edge. So the checks read the source.

const ROOT = process.cwd()

function tsx(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name === "__tests__" || name === "generated" || name.startsWith(".")) continue
    const full = join(dir, name)
    if (statSync(full).isDirectory()) tsx(full, out)
    else if (name.endsWith(".tsx")) out.push(full)
  }
  return out
}
const FILES = ["app", "components"].flatMap((d) => tsx(join(ROOT, d))).map((f) => ({ file: relative(ROOT, f).split(sep).join("/"), text: readFileSync(f, "utf8") }))
const read = (p: string) => readFileSync(join(ROOT, p), "utf8")

/**
 * A control revealed with `opacity-0 … group-hover:opacity-100` never shows
 * where nothing hovers. It still takes a tap, so it is both unfindable and
 * hit by accident. It needs a touch form on the same class string.
 */
export function hoverOnlyReveals(source: string): { line: number; cls: string }[] {
  const out: { line: number; cls: string }[] = []
  source.split("\n").forEach((line, i) => {
    if (!/\bopacity-0\b/.test(line) || !/group-hover(?:\/[\w-]+)?:opacity-100/.test(line)) return
    if (/(?:^|[\s"'`])(?:coarse|pointer-coarse|max-md):(?:opacity-100|hidden)\b/.test(line)) return
    out.push({ line: i + 1, cls: line.trim().slice(0, 120) })
  })
  return out
}

// Decorative marks: nothing to tap, nothing lost when they stay hidden.
const HOVER_ALLOW: Record<string, string> = {
  "components/features/routines/routines-overview.tsx": "row chevron, decoration on a row that is itself the button",
  "components/crew-icon-popover.tsx": "pencil drawn over the icon tile; the tile is the button",
}

/**
 * The touch rule in app/globals.css is outside every @layer, so it beats
 * Tailwind's layered utilities: on a touch device `min-w-[112px]` on a button
 * computes to 44px. A floor above 44px on an element that rule matches has to
 * be important (`min-w-[112px]!`) to survive.
 */
const MIN_H_TAGS = /^(a|button|summary|select|input|Button|Link|SelectTrigger|DropdownMenuItem|TabsTrigger)$/
const MIN_W_TAGS = /^(button|Button|TabsTrigger)$/
export function unprotectedFloors(source: string): { line: number; cls: string }[] {
  const out: { line: number; cls: string }[] = []
  for (const m of source.matchAll(/(?<![\w-])(?:[\w[\]=&>*()/.-]+:)*(min-[hw])-\[(\d+(?:\.\d+)?)px\](!?)/g)) {
    if (Number(m[2]) <= 44 || m[3] === "!") continue
    const before = source.slice(Math.max(0, m.index! - 1500), m.index!)
    const tags = [...before.matchAll(/<([A-Za-z][\w.]*)(?=[\s/>])/g)]
    if (!tags.length) continue
    const tag = tags[tags.length - 1][1]
    if (!(m[1] === "min-h" ? MIN_H_TAGS : MIN_W_TAGS).test(tag)) continue
    out.push({ line: source.slice(0, m.index!).split("\n").length, cls: `${m[0]} on <${tag}>` })
  }
  return out
}

describe("touch layout guards", () => {
  it("scans real files", () => {
    expect(FILES.length).toBeGreaterThan(500)
  })

  it("has no hover-only reveal without a touch form", () => {
    const found: string[] = []
    const seen = new Set<string>()
    for (const { file, text } of FILES) {
      const hits = hoverOnlyReveals(text)
      if (!hits.length) continue
      if (HOVER_ALLOW[file]) { seen.add(file); continue }
      for (const h of hits) found.push(`${file}:${h.line}: ${h.cls}`)
    }
    expect(found, "add coarse:opacity-100 (or coarse:hidden when touch has another way)").toEqual([])
    for (const k of Object.keys(HOVER_ALLOW)) expect(seen.has(k), `stale allowlist entry: ${k}`).toBe(true)
  })

  it("recognises the reveal patterns (scanner cases)", () => {
    expect(hoverOnlyReveals('<a className="opacity-0 group-hover:opacity-100" />')).toHaveLength(1)
    expect(hoverOnlyReveals('<a className="opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100" />')).toHaveLength(1)
    expect(hoverOnlyReveals('<a className="opacity-0 group-hover:opacity-100 coarse:opacity-100" />')).toEqual([])
    expect(hoverOnlyReveals('<a className="opacity-0 group-hover:opacity-100 pointer-coarse:opacity-100" />')).toEqual([])
    expect(hoverOnlyReveals('<a className="opacity-0 group-hover:opacity-100 coarse:hidden" />')).toEqual([])
    // Hidden from md up only: a tablet is a touch screen too.
    expect(hoverOnlyReveals('<a className="md:opacity-0 group-hover:opacity-100" />')).toHaveLength(1)
  })

  it("writes a floor above 44px on a control as important", () => {
    const found: string[] = []
    for (const { file, text } of FILES) for (const h of unprotectedFloors(text)) found.push(`${file}:${h.line}: ${h.cls}`)
    expect(found, "the unlayered touch rule in globals.css resets it to 44px: write min-w-[Npx]!").toEqual([])
  })

  it("recognises unprotected floors (scanner cases)", () => {
    expect(unprotectedFloors('<button className="min-w-[112px] flex" />')).toHaveLength(1)
    expect(unprotectedFloors('<button className="coarse:min-h-[52px]" />')).toHaveLength(1)
    expect(unprotectedFloors('<button className="min-w-[112px]! flex" />')).toEqual([])
    expect(unprotectedFloors('<button className="min-h-[44px]" />')).toEqual([])
    // Not matched by the touch rule: a textarea's height, a div's width.
    expect(unprotectedFloors('<textarea className="min-h-[120px]" />')).toEqual([])
    expect(unprotectedFloors('<div className="min-w-[760px]" />')).toEqual([])
    expect(unprotectedFloors('<input className="min-w-[80px]" />')).toEqual([])
  })

  it("centres a text link inside its 44px touch box", () => {
    const css = read("app/globals.css")
    expect(css).toMatch(/:where\(a\[href\]\):not\(\[class\*="flex"\], \[class\*="grid"\]\) \{\s*align-content: center;/)
  })

  // The seam strip beside the rail is a <button>; grown to 44px it covers the
  // rail's links (x 4–48 of a 48px rail) on every touch tablet.
  it("keeps the sidebar's seam strip its drawn width on touch", () => {
    const src = read("components/ui/sidebar.tsx")
    const rail = src.slice(src.indexOf('data-sidebar="rail"'))
    expect(rail.slice(0, 900)).toContain('"min-w-0!"')
  })

  it("lets the rail scroll as a whole in a short viewport", () => {
    const css = read("app/globals.css")
    expect(css).toMatch(/@media \(max-height: 560px\) \{\s*\[data-slot="sidebar-inner"\] \{ overflow-y: auto; \}/)
  })

  // One wide child must not stretch the page column: the page scroller clips
  // what sticks out, and nothing scrolls to it.
  it("keeps the calendar's week grid inside its own scroller", () => {
    expect(read("components/features/calendar/calendar-page.tsx")).toContain("mx-auto grid max-w-[1400px] grid-cols-[minmax(0,1fr)]")
  })

  it("breaks a long link inside a chat bubble", () => {
    expect(read("components/features/chat/messages/editable-user-message.tsx")).toMatch(/whitespace-pre-wrap break-words \[overflow-wrap:anywhere\]/)
    expect(read("components/features/chat/turn-renderer.tsx")).toMatch(/whitespace-pre-wrap break-words \[overflow-wrap:anywhere\]/)
  })

  // An sr-only element is position:absolute. Inside a scroller that is not a
  // positioned box it escapes to the outer one and stretches it: the page then
  // scrolls on into blank space after its last line.
  it("makes every pb-20 page scroller a positioned box", () => {
    const found: string[] = []
    for (const { file, text } of FILES) {
      text.split("\n").forEach((line, i) => {
        if (/h-full overflow-y-auto pb-20/.test(line) && !/relative h-full overflow-y-auto pb-20/.test(line)) found.push(`${file}:${i + 1}`)
      })
    }
    expect(found).toEqual([])
  })

  // A filter chip's remove button is drawn 16px (h-4) inside a 24px chip. On
  // a touch screen it gets a 44px hit area from a pseudo-element
  // (coarse:after:-inset-4 around h-4 = 12 spacing units = 44px), so the chip
  // keeps its size and the finger still finds the X.
  it("gives every filter chip's remove button a 44px hit area on touch", () => {
    const found: string[] = []
    let seen = 0
    for (const { file, text } of FILES) {
      for (const m of text.matchAll(/aria-label=\{`Remove filter /g)) {
        const open = text.lastIndexOf("<button", m.index)
        const at = text.indexOf('className="', open)
        if (open < 0 || at < 0) continue
        const cls = text.slice(at + 11, text.indexOf('"', at + 11))
        if (!/\bh-4 w-4\b/.test(cls)) continue
        seen++
        const ok = /(^|\s)relative(\s|$)/.test(cls) && cls.includes("coarse:after:absolute") && cls.includes("coarse:after:-inset-4") && cls.includes("coarse:after:content-['']")
        if (!ok) found.push(`${file}:${text.slice(0, m.index).split("\n").length}`)
      }
    }
    expect(seen).toBeGreaterThanOrEqual(4)
    expect(found).toEqual([])
  })
})
