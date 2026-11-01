// Shared checks of the native editor's unsaved-draft offer ("Místní koncept"),
// used by the stale-generation, write-revoked and restart rows. Nothing here
// saves or changes a document; it only reads what the editor shows and what
// the server holds.
import { readFile } from "node:fs/promises"
import type { APIRequestContext, Page } from "@playwright/test"
import type { NativePage } from "../craft-lease/native"
import { frameOf, recoveryRows } from "../drafts-crash/native"
import { pause, status } from "../outage-lostack/native"

export interface DraftOffer {
  /** The offered rows, e.g. "Místní koncept · verze 3". */
  rows: string[]
  /** For each offered row: does the draft text contain every marker. */
  hasMarker: boolean[]
  /** Does the server's version shown beside the draft contain every marker. */
  serverHasMarker: boolean[]
}

/** Progress of a typeLines() stream: how many lines (text + Enter) were typed so far. */
export interface Typing {
  typed: number
}

/** The markers typeLines() writes for its first `n` lines: `${prefix}-0` … `${prefix}-${n - 1}`. */
export function typedMarkers(prefix: string, n: number): string[] {
  return Array.from({ length: n }, (_, i) => `${prefix}-${i}`)
}

/** Does `text` hold every marker as a whole line number (`X-1` is not satisfied by `X-10`). */
export function holdsAll(text: string, markers: string[]): boolean {
  const escape = (m: string) => m.replace(/[.*+?^$()|[\]\\{}]/g, "\\$&")
  return markers.every((m) => new RegExp(escape(m) + "(?!\\d)").test(text))
}

/**
 * Looks into every offered draft and reports whether every marker is in it
 * (pass all lines typed before the disturbance, so a partly lost draft fails). While
 * the editor is connected "Porovnat" shows the draft beside the server version.
 * An editor whose connection was ended (a revoked right, a restart) cannot read
 * the server version, so the comparison never opens; the draft is then taken
 * out with "Kopie konceptu" (a file download), which needs no server.
 */
export async function inspectDrafts(page: Page, marker: string | string[]): Promise<DraftOffer> {
  const markers = typeof marker === "string" ? [marker] : marker
  if (markers.length === 0) throw new Error("inspectDrafts needs at least one marker")
  const rows = (await recoveryRows(page).allInnerTexts()).map((r) => r.trim())
  const hasMarker: boolean[] = []
  const serverHasMarker: boolean[] = []
  for (let i = 0; i < rows.length; i++) {
    const row = recoveryRows(page).nth(i).locator("..")
    await row.getByRole("button", { name: "Porovnat", exact: true }).click()
    const serverBox = page.locator('textarea[aria-label="Serverová verze"]')
    const opened = await serverBox.waitFor({ timeout: 10_000 }).then(() => true, () => false)
    if (opened) {
      serverHasMarker.push(holdsAll(await serverBox.inputValue(), markers))
      hasMarker.push(holdsAll(await page.locator('textarea[aria-label="Místní koncept"]').inputValue(), markers))
      await page.getByRole("button", { name: "Zavřít porovnání", exact: true }).click()
    } else {
      const download = page.waitForEvent("download", { timeout: 20_000 })
      await row.getByRole("button", { name: "Kopie konceptu", exact: true }).click()
      const file = await (await download).path()
      serverHasMarker.push(false)
      hasMarker.push(holdsAll(await readFile(file, "utf8"), markers))
    }
  }
  return { rows, hasMarker, serverHasMarker }
}

/** Waits until at least one draft row is offered and returns the rows. */
export async function waitForDraftOffer(page: Page, timeout = 60_000): Promise<string[]> {
  await recoveryRows(page).first().waitFor({ timeout })
  return (await recoveryRows(page).allInnerTexts()).map((r) => r.trim())
}

/** The committed head as text (the native JSON), read with a fresh read admission. */
export async function headText(api: APIRequestContext, doc: NativePage): Promise<{ version: string; text: string }> {
  const a = await doc.session(api)
  const d = await doc.document(api, a)
  return { version: String(d.version), text: Buffer.from(d.bytes, "base64").toString("utf8") }
}

/**
 * The editor saves 300 ms after the last keystroke, so "unsaved work" exists
 * only while someone keeps typing, or while a save cannot happen. The rows that
 * need unsaved work type in a steady stream of short lines (Word: paragraphs,
 * Sheet: cells in a column), with gaps well below the autosave delay, and do
 * the disturbance (a handoff, a revoked right, a restart) in the middle.
 */
export async function placeCaret(page: Page, app: "wordcraft" | "gridcraft", row: number): Promise<void> {
  // The host remounts the editor frame after a save or a lease change: take the current one and focus it.
  const f = await frameOf(page, app)
  await f.locator("canvas").first().click({ position: app === "wordcraft" ? { x: 678, y: 324 } : { x: 54, y: 240 } })
  const kb = page.keyboard
  if (app === "wordcraft") {
    await kb.press("Control+End")
    await kb.press("Enter")
    return
  }
  const settle = () => f.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
  for (let k = 0; k < 3; k++) {
    await kb.press("Control+PageUp")
    await settle()
  }
  await f.locator("canvas").first().click({ position: { x: 68, y: 178 } })
  await kb.press("Control+a")
  await kb.type(`A${row}`)
  await kb.press("Enter")
  await settle()
}

/** Types `${prefix}-0`, `${prefix}-1`, ... each followed by Enter, without ever pausing for autosave. */
export async function typeLines(page: Page, prefix: string, lines: number, delay = 40, gapMs = 120, progress: Typing = { typed: 0 }): Promise<number> {
  // A gap at or above the 300 ms autosave delay would let every line save.
  if (gapMs >= 300 || delay >= 300) throw new Error(`typeLines gaps must stay below the 300 ms autosave delay (delay ${delay}, gap ${gapMs})`)
  try {
    for (let i = 0; i < lines; i++) {
      await page.keyboard.type(`${prefix}-${i}`, { delay })
      await page.keyboard.press("Enter")
      progress.typed++
      await page.waitForTimeout(gapMs)
    }
  } catch {
    // The page went away (restart, retired editor): what was typed so far counts.
  }
  return progress.typed
}

/** Waits for "Uloženo · verze N" with N newer than `after` (the label of an older save stays on screen). */
export async function savedAfter(page: Page, after: string, timeout: number): Promise<string | undefined> {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    const v = (await status(page)).match(/^Uloženo · verze (\d+)/)?.[1]
    if (v && BigInt(v) > BigInt(after)) return v
    await pause(150)
  }
  return undefined
}

/**
 * The editor saves by itself 300 ms after the last keystroke. Takes that save
 * when it comes and presses Ctrl+S only when it does not (Ctrl+S always sends,
 * so after an autosave it would store identical bytes as a second version).
 */
export async function saveByAutosaveOrKey(page: Page, after: string): Promise<string | undefined> {
  const v = await savedAfter(page, after, 4_000)
  if (v) return v
  await page.keyboard.press("Control+s")
  return savedAfter(page, after, 60_000)
}
