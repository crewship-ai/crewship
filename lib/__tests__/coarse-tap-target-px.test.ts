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

// Any variant chain, then min-h/min-w-10..12: min-h-11, sm:min-h-12, max-sm:min-w-10 …
const FLOOR = /(?<![\w-])((?:[\w[\]=&>*()/.-]+:)*)(min-[hw])-(1[0-2])(?![\w.\-/[])/g
// h-/size-10..12 (not w-: a width alone is not a target height).
const FIXED = /(?<![\w-])((?:[\w[\]=&>*()/.-]+:)*)(h|size)-(1[0-2])(?![\w.\-/[])/g
const INTERACTIVE = /^(button|a|label|input|select|textarea|summary|Button|Link|ListRow|SidebarRow|IconButton|DropdownMenuItem|MenuSheetItem|SelectTrigger|TabsTrigger|Toggle|ToggleGroupItem|Checkbox|Switch)$/
const INTERACTIVE_ROLE = /role=["'{](?:button|menuitem|menuitemcheckbox|menuitemradio|option|switch|radio|checkbox|tab|link)/

// Reason per entry; a key is "<file> <class>". A stale entry fails the test.
const FLOOR_ALLOW: Record<string, string> = {
  "components/features/activity-stream/feed-row.tsx min-w-12": "timestamp column width in a feed row, not a control",
  "components/features/calendar/calendar-views.tsx min-h-11": "month-grid day cell; the events inside it are the targets",
  "components/features/crews/cr-card.tsx min-h-12": "card header band, not a control (its own buttons size themselves)",
  "components/features/dashboard/dash-card.tsx min-h-12": "card header band, not a control",
  "components/features/pages/folder-sharing-dialog.tsx min-h-10": "ACL table rows; the selects and buttons inside are the targets",
  "components/features/skills/skill-page.tsx min-h-12": "read-only list items; inline actions size themselves",
  "components/features/skills/skills-views.tsx min-h-12": "read-only list items; inline actions size themselves",
  "components/features/settings/sections/pages-appearance-card.tsx min-h-10": "theme-token row layout; the swatch/colour input is the target and fills its own box",
}
const FIXED_ALLOW: Record<string, string> = {}

function lineOf(src: string, idx: number): number {
  return src.slice(0, idx).split("\n").length
}

/**
 * The class string already carries a px floor for this property that applies
 * wherever the scale one does: the same variant prefix, or an unprefixed one,
 * or `coarse:` for an unprefixed/coarse scale class. A floor under another
 * variant (`group-data-[mobile=true]/surface:`) does not cover it.
 */
function hasPxFloor(lit: string, prefix: string, prop: string): boolean {
  const esc = (x: string) => x.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&")
  const px = `${prop}-\\[44px\\]`
  const covers = ["", prefix, ...(prefix === "" || prefix === "coarse:" ? ["coarse:"] : [])]
  return covers.some((c) => new RegExp(`(^|\\s)${esc(c)}${px}`).test(lit))
}

/** A match inside a `//` or block-comment line is prose, not a class. */
function inComment(src: string, idx: number): boolean {
  const lineStart = src.lastIndexOf("\n", idx) + 1
  return /^\s*(\/\/|\*|\/\*)/.test(src.slice(lineStart, idx + 1))
}

/** The quoted/backticked string literal that holds position idx. */
function literalAround(src: string, idx: number): string {
  const lineStart = src.lastIndexOf("\n", idx) + 1
  let end = src.indexOf("\n", idx)
  if (end < 0) end = src.length
  const line = src.slice(lineStart, end)
  const at = idx - lineStart
  const quotes = /["'`]/
  let a = at
  while (a > 0 && !quotes.test(line[a - 1])) a--
  let b = at
  while (b < line.length && !quotes.test(line[b])) b++
  return line.slice(a, b)
}

/** The nearest JSX tag opened before idx is an interactive one (or carries an interactive role). */
function onInteractiveElement(src: string, idx: number): boolean {
  const before = src.slice(Math.max(0, idx - 1500), idx)
  const tags = [...before.matchAll(/<([A-Za-z][\w.]*)(?=[\s/>])/g)]
  if (!tags.length) return false
  const last = tags[tags.length - 1]
  const open = before.slice(last.index!)
  if (INTERACTIVE.test(last[1])) return true
  const after = src.slice(idx, idx + 400)
  return INTERACTIVE_ROLE.test(open + after.split(/\/?>/)[0])
}

/** Scale floors (min-h/min-w 10..12) in a source text that no covering px floor backs up. */
export function floorFindings(src: string): { token: string; cls: string; line: number }[] {
  const out: { token: string; cls: string; line: number }[] = []
  for (const m of src.matchAll(FLOOR)) {
    if (inComment(src, m.index!)) continue
    // A px floor in the same class string that applies wherever this one does.
    if (hasPxFloor(literalAround(src, m.index!), m[1], m[2])) continue
    out.push({ token: m[2] + "-" + m[3], cls: m[0], line: lineOf(src, m.index!) })
  }
  return out
}

// An unprefixed px floor is 44 px at a fine pointer too, which grows a desktop
// control past its v67 size (ODCH-06: fine pointer follows v67, coarse is 44).
// It is for controls that only exist on a phone (a bottom sheet row), or that
// already hand the size back at sm: (`h-[44px] sm:h-8`). Anything else keeps
// its fine size and adds `coarse:min-h-[44px]`.
const PLAIN_PX = /(?<![\w:-])(min-h|min-w|h)-\[44px\]/g
const PX_ALLOW: Record<string, string> = {
  "components/ui/menu-sheet.tsx min-h": "MenuSheet sheet rows: the bottom sheet only exists on a phone",
  "components/features/chat/composer/chat-composer.tsx min-h": "composer actions: Chat-owned, 44 px before this guard (needs Chat team review)",
}
export function plainPxFindings(src: string): { token: string; cls: string; line: number }[] {
  const out: { token: string; cls: string; line: number }[] = []
  for (const m of src.matchAll(PLAIN_PX)) {
    if (inComment(src, m.index!)) continue
    // Handed back above the phone breakpoint: `h-[44px] sm:h-8`, `min-h-[44px] sm:min-h-9`.
    if (new RegExp(`(^|\\s)(sm|md|lg|xl):${m[1]}-`).test(literalAround(src, m.index!))) continue
    out.push({ token: m[1], cls: m[0], line: lineOf(src, m.index!) })
  }
  return out
}

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

  // The same trap without the `coarse:` prefix. A floor from the spacing scale
  // (`min-h-11`, `min-h-12`) is a rem size too: 37–41 px under this app's
  // `--spacing` and Small text size, which is under the 44 px a touch target
  // needs (the "Notification sounds" sheet row was 37.6 px). A floor is written
  // `min-h-[44px]`, or `coarse:min-h-[44px]` where the fine pointer stays smaller.
  it("has no spacing-scale min-h / min-w floor (10..12) outside the allowlist", () => {
    const found: string[] = []
    const seen = new Set<string>()
    for (const d of DIRS) {
      for (const f of files(path.join(ROOT, d))) {
        const rel = path.relative(ROOT, f)
        const src = readFileSync(f, "utf8")
        for (const hit of floorFindings(src)) {
          const key = `${rel} ${hit.token}`
          if (FLOOR_ALLOW[key]) { seen.add(key); continue }
          found.push(`${rel}:${hit.line}: ${hit.cls}`)
        }
      }
    }
    expect(found).toEqual([])
    for (const k of Object.keys(FLOOR_ALLOW)) expect(seen.has(k), `stale allowlist entry: ${k}`).toBe(true)
  })

  it("has no h-/size- 10..12 from the scale on an interactive element outside the allowlist", () => {
    const found: string[] = []
    const seen = new Set<string>()
    for (const d of DIRS) {
      for (const f of files(path.join(ROOT, d))) {
        const rel = path.relative(ROOT, f)
        const src = readFileSync(f, "utf8")
        for (const m of src.matchAll(FIXED)) {
          if (inComment(src, m.index!)) continue
          if (!onInteractiveElement(src, m.index!)) continue
          const lit = literalAround(src, m.index!)
          if (hasPxFloor(lit, m[1], m[2])) continue
          const key = `${rel} ${m[2]}-${m[3]}`
          if (FIXED_ALLOW[key]) { seen.add(key); continue }
          found.push(`${rel}:${lineOf(src, m.index!)}: ${m[0]}`)
        }
      }
    }
    expect(found).toEqual([])
    for (const k of Object.keys(FIXED_ALLOW)) expect(seen.has(k), `stale allowlist entry: ${k}`).toBe(true)
  })

  it("keeps the fine-pointer size and adds a coarse px floor (scanner cases)", () => {
    // The intended pattern passes …
    expect(floorFindings('<a className="min-h-11 coarse:min-h-[44px]" />')).toEqual([])
    expect(floorFindings('<b className="min-h-12 px-3 coarse:min-h-[44px]" />')).toEqual([])
    expect(plainPxFindings('<a className="min-h-11 coarse:min-h-[44px]" />')).toEqual([])
    expect(plainPxFindings('<a className="h-[44px] sm:h-8" />')).toEqual([])
    // … a bare scale floor, or one a different variant's px floor does not cover, fails …
    expect(floorFindings('<a className="min-h-11" />')).toHaveLength(1)
    expect(floorFindings('<a className="min-h-12 group-data-[mobile=true]/surface:min-h-12 coarse:min-h-[44px]" />').map((f) => f.cls)).toEqual(["group-data-[mobile=true]/surface:min-h-12"])
    // … and so does a plain px floor that grows the fine-pointer size.
    expect(plainPxFindings('<a className="flex min-h-[44px] items-center" />')).toHaveLength(1)
    expect(plainPxFindings('<a className="group-data-[mobile=true]/surface:min-h-[44px]" />')).toEqual([])
  })

  it("has no plain min-h/min-w/h px floor outside phone-only controls", () => {
    const found: string[] = []
    const seen = new Set<string>()
    for (const d of DIRS) {
      for (const f of files(path.join(ROOT, d))) {
        const rel = path.relative(ROOT, f)
        for (const hit of plainPxFindings(readFileSync(f, "utf8"))) {
          const key = `${rel} ${hit.token}`
          if (PX_ALLOW[key]) { seen.add(key); continue }
          found.push(`${rel}:${hit.line}: ${hit.cls}`)
        }
      }
    }
    expect(found).toEqual([])
    for (const k of Object.keys(PX_ALLOW)) expect(seen.has(k), `stale allowlist entry: ${k}`).toBe(true)
  })
})
