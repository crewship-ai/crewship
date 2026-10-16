/**
 * Layout breakpoints have one home: hooks/use-layout-breakpoint.ts (and
 * useIsMobile beside it), on the Tailwind md / xl / 2xl edges — phone < 768,
 * tablet 768–1279, desktop ≥ 1280, wide ≥ 1536 (#3345).
 *
 * This guard fails when a new hand-written viewport query appears anywhere
 * else: a `matchMedia(` call, a width `@media` rule in CSS on another edge,
 * or an arbitrary `min-[…px]:` / `max-[…px]:` Tailwind variant. Queries that
 * do not decide layout — reduced motion, pointer, hover, colour scheme,
 * display mode, print — stay allowed, and so do the chat-owned files, whose
 * own tree breakpoint (900px) and details-panel edge (720px) belong to the
 * chat work.
 *
 * A non-layout `matchMedia` call must carry its query on the same line, so
 * the guard can read it.
 */
import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join, relative, sep } from "node:path"

const ROOT = process.cwd()
const SCANNED = ["app", "components", "hooks", "lib", "stores"]

/** Files that ARE the breakpoint source. */
const BREAKPOINT_HOOKS = new Set(["hooks/use-mobile.ts", "hooks/use-layout-breakpoint.ts"])

/** Chat-owned trees (their breakpoints are listed in #3345's PR). */
const CHAT_OWNED = [
  "app/(dashboard)/chat/",
  "components/features/conversations/",
  "components/features/chat/",
]

/** Media features that never decide page layout. */
const NON_LAYOUT_QUERY =
  /\((?:prefers-reduced-motion|prefers-reduced-transparency|prefers-contrast|prefers-color-scheme|pointer|any-pointer|hover|any-hover|display-mode|forced-colors)\b|\bprint\b/

/** The only width edges a CSS rule may name: md, xl, 2xl, in px or rem. */
const ALLOWED_CSS_EDGES = new Set(["767px", "768px", "1279px", "1280px", "1535px", "1536px", "48rem", "80rem", "96rem"])

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name === "__tests__" || name.startsWith(".")) continue
    const full = join(dir, name)
    if (statSync(full).isDirectory()) walk(full, out)
    else out.push(full)
  }
  return out
}

function sources(pattern: RegExp): { file: string; text: string }[] {
  return SCANNED.flatMap((d) => walk(join(ROOT, d)))
    .filter((f) => pattern.test(f) && !/\.test\.tsx?$/.test(f) && !f.endsWith(".d.ts"))
    .map((f) => ({ file: relative(ROOT, f).split(sep).join("/"), text: readFileSync(f, "utf8") }))
}

const isChatOwned = (file: string) => CHAT_OWNED.some((p) => file.startsWith(p))

function hits(text: string, re: RegExp): { line: number; text: string }[] {
  const out: { line: number; text: string }[] = []
  text.split("\n").forEach((line, i) => {
    if (re.test(line)) out.push({ line: i + 1, text: line.trim() })
  })
  return out
}

const CODE = sources(/\.(ts|tsx|js|jsx|mjs)$/)
const CSS = sources(/\.css$/)

describe("layout breakpoints come from one hook", () => {
  it("scans real files", () => {
    // A guard that matches nothing would pass forever.
    expect(CODE.map((s) => s.file)).toContain("hooks/use-layout-breakpoint.ts")
    expect(CSS.map((s) => s.file)).toContain("app/globals.css")
  })

  it("has no layout matchMedia outside the breakpoint hooks and chat-owned files", () => {
    const offenders: string[] = []
    for (const { file, text } of CODE) {
      if (BREAKPOINT_HOOKS.has(file) || isChatOwned(file)) continue
      for (const h of hits(text, /matchMedia\s*(?:\?\.)?\s*\(/)) {
        if (!NON_LAYOUT_QUERY.test(h.text)) offenders.push(`${file}:${h.line}: ${h.text}`)
      }
    }
    expect(offenders, "use useLayoutBreakpoint() / useIsMobile() or md:/xl:/2xl:").toEqual([])
  })

  it("names only md / xl / 2xl edges in @media width queries", () => {
    // CSS files, plus CSS carried in component strings (chat-owned excepted).
    const offenders: string[] = []
    const widthQuery = /\((?:max|min)-width:\s*([\d.]+(?:px|rem))\)|\(width\s*[<>]=?\s*([\d.]+(?:px|rem))\)/g
    for (const { file, text } of [...CSS, ...CODE.filter((s) => !isChatOwned(s.file))]) {
      text.split("\n").forEach((line, i) => {
        if (!/@media/.test(line)) return
        for (const m of line.matchAll(widthQuery)) {
          const edge = m[1] ?? m[2]
          if (!ALLOWED_CSS_EDGES.has(edge)) offenders.push(`${file}:${i + 1}: ${line.trim()}`)
        }
      })
    }
    expect(offenders, "use the md (768), xl (1280) or 2xl (1536) edge").toEqual([])
  })

  it("has no arbitrary viewport variants outside chat-owned files", () => {
    const offenders: string[] = []
    for (const { file, text } of CODE) {
      if (isChatOwned(file)) continue
      for (const h of hits(text, /(?<![\w-])(?:min|max)-\[\d+(?:px|rem)\]:/)) {
        offenders.push(`${file}:${h.line}: ${h.text}`)
      }
    }
    expect(offenders, "use md:/xl:/2xl: (or max-md:/max-xl:/max-2xl:)").toEqual([])
  })
})
