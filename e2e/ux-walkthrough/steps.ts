import type { Page } from "@playwright/test"
import { note, pause, shot } from "./audit"

/** Runs one walkthrough step; a failure is recorded with a screenshot instead of stopping the walk. */
export async function step(page: Page, name: string, fn: () => Promise<unknown>): Promise<boolean> {
  const t0 = Date.now()
  try {
    const value = await fn()
    note(`step:${name}`, { ok: true, ms: Date.now() - t0, value: value ?? null })
    return true
  } catch (err) {
    const file = await shot(page, `${name}-FAILED`)
    note(`step:${name}`, { ok: false, ms: Date.now() - t0, error: String(err).split("\n").slice(0, 4).join(" | "), screenshot: file })
    return false
  }
}

/** Samples a role=status text for `ms` and returns the distinct labels in order with their first time. */
export async function statusTimeline(page: Page, ms: number, selector = '[role="status"]'): Promise<Array<{ t: number; label: string }>> {
  const out: Array<{ t: number; label: string }> = []
  const t0 = Date.now()
  let last = ""
  while (Date.now() - t0 < ms) {
    const label = (await page.locator(selector).allInnerTexts().catch(() => [] as string[])).map((s) => s.replace(/\s+/g, " ").trim()).filter(Boolean).join(" | ")
    if (label !== last) {
      out.push({ t: Date.now() - t0, label })
      last = label
    }
    await pause(150)
  }
  return out
}
