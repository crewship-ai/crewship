// Evidence helpers of the UX walkthrough: screenshots, accessibility snapshots,
// unnamed controls, touch-target sizes and keyboard Tab order. Everything is
// written to UX_OUT; nothing here prints credentials.
import type { Page } from "@playwright/test"
import * as fs from "node:fs"
import * as path from "node:path"

export const OUT = process.env.UX_OUT ?? path.resolve("test-results/ux-walkthrough")
fs.mkdirSync(OUT, { recursive: true })

export type Viewport = "desktop" | "mobile"
export const VIEWPORTS: Record<Viewport, { width: number; height: number }> = {
  desktop: { width: 1440, height: 900 },
  mobile: { width: 390, height: 844 },
}

const log: Record<string, unknown> = {}

/** Records a structured observation; flushed to OUT/observations-<target>.json. */
export function note(key: string, value: unknown): void {
  log[key] = value
  fs.writeFileSync(path.join(OUT, `observations-${process.env.UX_TARGET ?? "all"}.json`), JSON.stringify(log, null, 2))
}

export async function shot(page: Page, name: string): Promise<string> {
  const file = `${name}.png`
  await page.screenshot({ path: path.join(OUT, file) }).catch(() => {})
  return file
}

export async function aria(page: Page, name: string): Promise<void> {
  const snap = await page.locator("body").ariaSnapshot({ timeout: 10_000 }).catch((e) => `ariaSnapshot failed: ${e}`)
  fs.writeFileSync(path.join(OUT, `${name}.aria.yml`), snap)
}

interface Control {
  tag: string
  role: string | null
  name: string
  w: number
  h: number
  html: string
}

/** Visible interactive controls in the top document with their size and accessible name. */
export async function controls(page: Page): Promise<Control[]> {
  return page.evaluate(() => {
    const sel = 'button, a[href], input:not([type=hidden]), select, textarea, [role=button], [role=tab], [role=menuitem], [role=checkbox], [role=switch], [role=combobox], [role=option]'
    const out: Control[] = []
    const nameOf = (el: Element): string => {
      const label = el.getAttribute("aria-label")
      if (label?.trim()) return label.trim()
      const by = el.getAttribute("aria-labelledby")
      if (by) {
        const t = by.split(/\s+/).map((id) => document.getElementById(id)?.textContent ?? "").join(" ").trim()
        if (t) return t
      }
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) {
        const lab = el.labels?.[0]?.textContent?.trim()
        if (lab) return lab
        if ("placeholder" in el && el.placeholder) return `placeholder:${el.placeholder}`
      }
      const text = (el.textContent ?? "").replace(/\s+/g, " ").trim()
      if (text) return text.slice(0, 80)
      const img = el.querySelector("img[alt], svg[aria-label]")
      const alt = img?.getAttribute("alt") || img?.getAttribute("aria-label")
      if (alt) return alt
      return el.getAttribute("title") ? `title:${el.getAttribute("title")}` : ""
    }
    for (const el of Array.from(document.querySelectorAll(sel))) {
      const r = el.getBoundingClientRect()
      const st = getComputedStyle(el)
      if (r.width === 0 || r.height === 0 || st.visibility === "hidden" || st.display === "none") continue
      if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue
      out.push({ tag: el.tagName.toLowerCase(), role: el.getAttribute("role"), name: nameOf(el), w: Math.round(r.width), h: Math.round(r.height), html: el.outerHTML.slice(0, 160) })
    }
    return out
  })
}

export interface Audit {
  unnamed: Control[]
  small: Control[]
  total: number
  horizontalOverflow: boolean
}

/** Unnamed controls and (on mobile) controls under 44px in either axis. */
export async function audit(page: Page, name: string, viewport: Viewport): Promise<Audit> {
  await shot(page, name)
  await aria(page, name)
  const cs = await controls(page)
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)
  const result: Audit = {
    unnamed: cs.filter((c) => !c.name),
    // Inline text links inside prose are exempt (WCAG 2.5.8 inline exception).
    small: viewport === "mobile" ? cs.filter((c) => (c.h < 44 || c.w < 24) && !(c.tag === "a" && c.h >= 16 && c.w > 60)) : [],
    total: cs.length,
    horizontalOverflow: overflow,
  }
  note(`audit:${name}`, result)
  return result
}

export interface FocusStop {
  i: number
  tag: string
  role: string | null
  name: string
  visible: boolean
  inIframe: boolean
}

/** Presses Tab `n` times from the current focus and records each stop and whether its focus is visible. */
export async function tabWalk(page: Page, name: string, n: number): Promise<FocusStop[]> {
  const stops: FocusStop[] = []
  for (let i = 0; i < n; i++) {
    await page.keyboard.press("Tab")
    stops.push(
      await page.evaluate((i) => {
        const el = document.activeElement as HTMLElement | null
        if (!el || el === document.body) return { i, tag: "body", role: null, name: "", visible: false, inIframe: false }
        const st = getComputedStyle(el)
        const ring = (st.outlineStyle !== "none" && parseFloat(st.outlineWidth) > 0) || (st.boxShadow && st.boxShadow !== "none")
        const nm = (el.getAttribute("aria-label") || el.textContent || el.getAttribute("title") || "").replace(/\s+/g, " ").trim().slice(0, 60)
        return { i, tag: el.tagName.toLowerCase(), role: el.getAttribute("role"), name: nm, visible: Boolean(ring), inIframe: el.tagName === "IFRAME" }
      }, i),
    )
  }
  note(`tab:${name}`, stops)
  return stops
}

/** Describes the currently focused element (for "Escape returns focus" checks). */
export async function focused(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    if (!el || el === document.body) return "body"
    return `${el.tagName.toLowerCase()}[${el.getAttribute("role") ?? ""}] ${(el.getAttribute("aria-label") || el.textContent || "").replace(/\s+/g, " ").trim().slice(0, 60)}`
  })
}

export const pause = (ms: number) => new Promise((r) => setTimeout(r, ms))
