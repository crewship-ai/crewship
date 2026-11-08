// Shared checks of the native editor's unsaved-draft offer ("Local draft"),
// used by the stale-generation, write-revoked and restart rows. Nothing here
// saves or changes a document; it only reads what the editor shows and what
// the server holds.
import { readFile } from "node:fs/promises"
import type { APIRequestContext, Page } from "@playwright/test"
import type { NativePage } from "../craft-lease/native"
import { frameOf, recoveryRows } from "../drafts-crash/native"
import { pause, status } from "../outage-lostack/native"
import { editorTakesInput } from "./lines"

export interface DraftOffer {
  /** The offered rows, e.g. "Local draft · version 3". */
  rows: string[]
  /** For each offered row: does the draft text contain every marker. */
  hasMarker: boolean[]
  /** Does the server's version shown beside the draft contain every marker. */
  serverHasMarker: boolean[]
  /** For each offered row: the draft itself (the native JSON the editor keeps), for lines.ts checks. */
  texts: string[]
}

/**
 * Progress of a typeLines() stream. `typed` counts lines (text + Enter) sent;
 * `accepted` lists the lines the editor provably took: it took input both
 * before and after the line (lines.ts, editorTakesInput: status "Read-only"
 * or "disconnected", or a custody toolbar naming another holder or "Read-only",
 * means it no longer takes input). Every accepted line must survive, saved or in
 * the draft, including lines typed while a restart or handoff was in flight.
 */
export interface Typing {
  typed: number
  accepted: number[]
  /** The status text that ended the stream, if the editor stopped taking input. */
  stoppedBy?: string
  /** Status reads that did not answer in time (a stalled renderer); such a line is not counted as accepted. */
  unknown: number
}

export const newTyping = (): Typing => ({ typed: 0, accepted: [], unknown: 0 })

/** The markers of the given typeLines() lines: `${prefix}-${i}`. */
export function typedMarkers(prefix: string, lines: number[]): string[] {
  return lines.map((i) => `${prefix}-${i}`)
}

// The software-rendered canvas can stall the renderer for seconds while typing;
// a read that does not answer in time is unknown, not "no longer editable".
// The status line alone is not enough: with unsaved work it reads "Neuložený
// koncept" also after the custody moved away, so the custody toolbar is read too
// (lines.ts, editorTakesInput).
interface EditorState {
  status: string
  custody: string | null
}
const editorState = async (page: Page): Promise<EditorState | null> => {
  // Both reads at once: the gap between lines must stay below the autosave delay.
  const [status, custody] = await Promise.all([
    page.getByRole("status").first().innerText({ timeout: 2_000 }).catch(() => null),
    page.getByRole("toolbar", { name: "Document editing" }).first().innerText({ timeout: 2_000 }).catch(() => null)
      // In the Artifacts frame the toolbar is in the closed status popover (#5132); the chip names the holder instead.
      .then(async (bar) => bar ?? (await page.locator("button[data-status]").first().evaluate((chip) => `chip:${chip.getAttribute("data-status")} ${chip.getAttribute("aria-label") ?? ""}`, null, { timeout: 2_000 }).catch(() => null))),
  ])
  return status === null ? null : { status, custody }
}
const takesInput = (s: EditorState) => editorTakesInput(s.status, s.custody)
const describeState = (s: EditorState) => (s.custody === null ? s.status : `${s.status} | ${s.custody}`)

/** Does `text` hold every marker as a whole line number (`X-1` is not satisfied by `X-10`). */
export function holdsAll(text: string, markers: string[]): boolean {
  const escape = (m: string) => m.replace(/[.*+?^$()|[\]\\{}]/g, "\\$&")
  return markers.every((m) => new RegExp(escape(m) + "(?!\\d)").test(text))
}

/**
 * Looks into every offered draft and reports whether every marker is in it
 * (pass all lines typed before the disturbance, so a partly lost draft fails). While
 * the editor is connected "Compare" shows the draft beside the server version.
 * An editor whose connection was ended (a revoked right, a restart) cannot read
 * the server version, so the comparison never opens; the draft is then taken
 * out with "Keep a copy" (a file download), which needs no server.
 */
export async function inspectDrafts(page: Page, marker: string | string[]): Promise<DraftOffer> {
  const markers = typeof marker === "string" ? [marker] : marker
  if (markers.length === 0) throw new Error("inspectDrafts needs at least one marker")
  const rows = (await recoveryRows(page).allInnerTexts()).map((r) => r.trim())
  const hasMarker: boolean[] = []
  const serverHasMarker: boolean[] = []
  const texts: string[] = []
  for (let i = 0; i < rows.length; i++) {
    const row = recoveryRows(page).nth(i).locator("..")
    await row.getByRole("button", { name: "Compare", exact: true }).click()
    const serverBox = page.locator('textarea[aria-label="Server version"]')
    const opened = await serverBox.waitFor({ timeout: 10_000 }).then(() => true, () => false)
    if (opened) {
      serverHasMarker.push(holdsAll(await serverBox.inputValue(), markers))
      const draft = await page.locator('textarea[aria-label="Local draft"]').inputValue()
      texts.push(draft)
      hasMarker.push(holdsAll(draft, markers))
      await page.getByRole("button", { name: "Close comparison", exact: true }).click()
    } else {
      const download = page.waitForEvent("download", { timeout: 20_000 })
      await row.getByRole("button", { name: "Keep a copy", exact: true }).click()
      const file = await (await download).path()
      serverHasMarker.push(false)
      const draft = await readFile(file, "utf8")
      texts.push(draft)
      hasMarker.push(holdsAll(draft, markers))
    }
  }
  return { rows, hasMarker, serverHasMarker, texts }
}

/** A DraftOffer for the evidence: the draft texts themselves are left out (their sizes stay). */
export function offerEvidence(offer: DraftOffer | null): (Omit<DraftOffer, "texts"> & { draftChars: number[] }) | null {
  if (!offer) return null
  const { texts, ...rest } = offer
  return { ...rest, draftChars: texts.map((t) => t.length) }
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

/**
 * Types `${prefix}-0`, `${prefix}-1`, ... each followed by Enter, without ever
 * pausing for autosave, and records in `progress` which lines the editor took.
 * Stops once the editor reports it no longer takes input; an unanswered status
 * read only leaves that line unproven.
 */
export async function typeLines(page: Page, prefix: string, lines: number, delay = 40, gapMs = 120, progress: Typing = newTyping()): Promise<number> {
  // A gap at or above the 300 ms autosave delay would let every line save.
  if (gapMs >= 300 || delay >= 300) throw new Error(`typeLines gaps must stay below the 300 ms autosave delay (delay ${delay}, gap ${gapMs})`)
  try {
    let before = await editorState(page)
    for (let i = 0; i < lines; i++) {
      if (before !== null && !takesInput(before)) {
        progress.stoppedBy = describeState(before)
        break
      }
      await page.keyboard.type(`${prefix}-${i}`, { delay })
      await page.keyboard.press("Enter")
      progress.typed++
      const started = Date.now()
      const after = await editorState(page)
      // A line during which the editor stopped taking input, or whose state before
      // or after is unknown, is ambiguous: not required.
      if (after === null) progress.unknown++
      if (before !== null && after !== null && takesInput(after)) progress.accepted.push(i)
      before = after
      await page.waitForTimeout(Math.max(0, gapMs - (Date.now() - started)))
    }
  } catch {
    // The page went away (restart, retired editor): what was typed so far counts.
  }
  return progress.typed
}

/** Waits for "Saved · version N" with N newer than `after` (the label of an older save stays on screen). */
export async function savedAfter(page: Page, after: string, timeout: number): Promise<string | undefined> {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    const v = (await status(page)).match(/^Saved · version (\d+)/)?.[1]
    if (v && BigInt(v) > BigInt(after)) return v
    await pause(150)
  }
  return undefined
}

/** What one saveByAutosaveOrKey wait did: the version it ended with, and whether it pressed Ctrl+S. */
export interface SaveOutcome {
  version: string | undefined
  pressed: boolean
  /** When Ctrl+S was pressed (only when pressed). */
  pressedAt?: string
}

/**
 * The editor saves by itself 300 ms after the last keystroke. Takes that save
 * when it comes and presses Ctrl+S only when it does not. After an autosave
 * Ctrl+S sends nothing: it confirms the saved version (#5075). Reports whether it
 * pressed; `onPress` runs right before the press (the hour rows mark the requests
 * sent from then on as following the press).
 */
export async function saveByAutosaveOrKeyReport(page: Page, after: string, onPress?: () => void): Promise<SaveOutcome> {
  const v = await savedAfter(page, after, 4_000)
  if (v) return { version: v, pressed: false }
  onPress?.()
  const pressedAt = new Date().toISOString()
  await page.keyboard.press("Control+s")
  return { version: await savedAfter(page, after, 60_000), pressed: true, pressedAt }
}

/** saveByAutosaveOrKeyReport, only the version. */
export async function saveByAutosaveOrKey(page: Page, after: string): Promise<string | undefined> {
  return (await saveByAutosaveOrKeyReport(page, after)).version
}
