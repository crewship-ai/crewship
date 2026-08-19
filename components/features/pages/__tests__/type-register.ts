/**
 * Reading the Pages type register out of `app/globals.css`.
 *
 * Not a test — a reader, shared by the tests that assert the register. It
 * exists because the assertions worth making about type are about the SCALE,
 * and the scale lives in CSS: a test that hard-codes `text-label` on a card is
 * green forever after the house moves on, which is the failure mode
 * `table-panel.test.tsx` was already written to avoid by comparing against
 * `PropertyRow`'s live DOM. This does the same job one level down — it compares
 * the register's roles against the `--typo-*` tokens the rest of the product is
 * built from, so a scale change moves one block of CSS and every surface that
 * named a role moves with it.
 *
 * jsdom/happy-dom load no stylesheet, so computed styles are not available and
 * the CSS has to be read as text. That is a feature here: the thing under test
 * is what the file DECLARES, and a role declared as a literal `0.75rem` would
 * pass a computed-style check while being exactly the drift these tests exist
 * to catch.
 */
import { readFileSync, readdirSync } from "node:fs"
import path from "node:path"

const REPO_ROOT = path.resolve(__dirname, "../../../..")
const GLOBALS = path.join(REPO_ROOT, "app", "globals.css")
export const PAGES_DIR = path.join(REPO_ROOT, "components", "features", "pages")

/** The roles the register declares, and the job each one does. */
export const PAGE_TYPE_ROLES = [
  "type-page-label",
  "type-page-value",
  "type-page-meta",
  "type-page-stamp",
  "type-page-metric",
] as const

export type PageTypeRole = (typeof PAGE_TYPE_ROLES)[number]

let cachedCss: string | null = null

export function globalsCss(): string {
  if (cachedCss === null) cachedCss = readFileSync(GLOBALS, "utf8")
  return cachedCss
}

/** The declaration block of a single class, comments stripped. */
export function ruleBody(selector: string): string {
  const css = globalsCss()
  const re = new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`)
  const m = css.match(re)
  if (!m) throw new Error(`no .${selector} rule in app/globals.css`)
  return m[1].replace(/\/\*[\s\S]*?\*\//g, "")
}

export function declaration(selector: string, property: string): string | null {
  const re = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  const m = ruleBody(selector).match(re)
  return m ? m[1].trim() : null
}

/**
 * The `--typo-*` token a role's `font-size` is written in, or `null` when it
 * spelled a length out instead. `null` is the whole point of this function.
 */
export function fontSizeToken(selector: string): string | null {
  const value = declaration(selector, "font-size")
  const m = value?.match(/^var\((--typo-[a-z-]+)\)$/)
  return m ? m[1] : null
}

/** What a `--typo-*` token is actually worth, read from `:root`. */
export function typoValue(token: string): string {
  const m = globalsCss().match(new RegExp(`${token}\\s*:\\s*([^;]+);`))
  if (!m) throw new Error(`no ${token} in app/globals.css`)
  return m[1].trim()
}

export function remToPx(value: string): number {
  const m = value.match(/^([\d.]+)rem$/)
  if (!m) throw new Error(`not a rem length: ${value}`)
  return Number(m[1]) * 16
}

/**
 * The `--typo-*` token behind a Tailwind size utility, read from the
 * `@theme inline` block — `text-body` is `--text-body`, which is declared as
 * `var(--typo-body)`. This is how a class rendered by a house component
 * (`PropertyRow`) is compared against a role declared in the register without
 * either side hard-coding the other's spelling.
 */
export function tailwindSizeToken(utility: string): string | null {
  const name = utility.replace(/^text-/, "")
  const m = globalsCss().match(new RegExp(`--text-${name}\\s*:\\s*var\\((--typo-[a-z-]+)\\)`))
  return m ? m[1] : null
}

/** The one size utility on an element, if it carries one. */
export function sizeUtility(el: Element): string | null {
  const known = ["micro", "label", "body", "default", "heading", "title", "display"]
  for (const cls of el.className.split(/\s+/)) {
    if (known.includes(cls.replace(/^text-/, "")) && cls.startsWith("text-")) return cls
  }
  return null
}

/** Every `.ts`/`.tsx` source under Pages, tests excluded, comments stripped. */
export function pagesSources(): { file: string; code: string }[] {
  const out: { file: string; code: string }[] = []
  const walk = (dir: string) => {
    // withFileTypes, so the directory test and the read are not two separate
    // questions about the same path. `readdirSync` + `statSync` + `readFileSync`
    // asks the filesystem three times and acts on the first answer — a
    // time-of-check/time-of-use race CodeQL flags, and one fewer syscall per
    // entry is the incidental win. The Dirent carries the type the directory
    // listing already knew.
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        if (entry.name !== "__tests__") walk(full)
        continue
      }
      if (!/\.tsx?$/.test(entry.name) || /\.test\.tsx?$/.test(entry.name)) continue
      const raw = readFileSync(full, "utf8")
      const code = raw
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .split("\n")
        .filter((line) => {
          const t = line.trim()
          return !t.startsWith("//") && !t.startsWith("*")
        })
        .join("\n")
      out.push({ file: path.relative(REPO_ROOT, full), code })
    }
  }
  walk(PAGES_DIR)
  return out
}
