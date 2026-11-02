// The work of the hour rows (craft-hour.acceptance.ts): what the person does
// in the editor each cycle, what the saved document must then contain, and the
// restart in the middle. Not a test file.
import type { APIRequestContext, BrowserContext, Frame, Page } from "@playwright/test"
import { readFacts } from "../craft-lease/sheet-workbook"
import { captureAdmission, traceCraft, type CraftCall } from "../craft-lease/helpers"
import type { PrdScenario } from "../fixtures"
import * as N from "../outage-lostack/native"
import { holdsAll, inspectDrafts, newTyping, placeCaret, saveByAutosaveOrKey, typedMarkers, typeLines } from "./draft"
import { present, sha256, wordFacts } from "./facts"
import type { RestartRecord } from "./restart"

export type SessionKind = "wordcraft" | "gridcraft"

export interface SessionInput {
  prd: PrdScenario
  context: BrowserContext
  api: APIRequestContext
  ref: N.NativeRef
  ops: N.Op[]
  /** Every native editor call of the person's pages (method, path, status), for the evidence. */
  trail: CraftCall[]
  /** What the person did and when (filled while the session runs, so a failed run still shows it). */
  steps: Array<{ atMs: number; step: string }>
  minutes: number
  restart(why: string): Promise<RestartRecord>
}

export interface SessionResult {
  app: SessionKind
  pageId: string
  minutes: number
  startedAt: string
  durationMs: number
  cycles: number
  saves: number
  markers: number
  restart: RestartRecord | null
  contentHash: { beforeRestart: string; afterRestart: string; afterReload: string; final: string; afterFinalReload: string } | null
  afterRestart: Record<string, unknown>
  steps: Array<{ atMs: number; step: string }>
  failures: string[]
}

export async function readHeadText(api: APIRequestContext, ref: N.NativeRef): Promise<{ version: string; text: string; hash: string }> {
  const h = await N.readHead(api, ref)
  return { version: h.version, text: h.text, hash: sha256(h.text) }
}

// 1x1 PNG.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==", "base64")

/** Browser console errors and warnings of the page, in the same list as the editor calls (for the evidence). */
function logConsole(page: Page, trail: CraftCall[]): void {
  const add = (method: string, text: string) => {
    if (trail.length < 5000) trail.push({ at: new Date().toISOString(), who: "console", method, path: text.replace(/\s+/g, " ").slice(0, 240), status: "" })
  }
  page.on("console", (m) => {
    if (m.type() === "error" || m.type() === "warning") add(m.type(), m.text())
  })
  page.on("pageerror", (e) => add("pageerror", String(e)))
}

/** What differs between Word and Sheet: how a cycle works and how the saved document is checked. */
interface Driver {
  app: SessionKind
  /** One cycle of work; returns the markers it typed and saved by Ctrl+S afterwards. */
  work(page: Page, f: Frame, cycle: number, frac: number, step: (name: string) => void, shot: (name: string) => Promise<void>): Promise<string[]>
  /** Optional first action that proves the freshly opened editor takes input (returns the saved version, or undefined). */
  warmup?(page: Page, f: Frame, after: string): Promise<string | undefined>
  /** Problems found in the saved document (empty when it holds everything the session did). */
  verify(text: string, saved: string[], stage: string): string[]
}

const CYCLE_MS = (minutes: number) => Math.round(Math.min(60_000, Math.max(20_000, (minutes * 60_000) / 12)))

async function runSession(input: SessionInput, d: Driver): Promise<SessionResult> {
  const { prd, context, api, ref, ops, minutes } = input
  const total = minutes * 60_000
  const cycleMs = CYCLE_MS(minutes)
  const tag = Math.random().toString(36).slice(2, 7)
  const startedAt = new Date()
  const t0 = startedAt.getTime()
  const failures: string[] = []
  const steps = input.steps
  const note = (step: string) => steps.push({ atMs: Date.now() - t0, step })
  const saved: string[] = []
  const hashes: Record<string, string> = {}
  let cycles = 0
  let lastVersion = ref.initVersion
  let restart: RestartRecord | null = null
  let afterRestart: Record<string, unknown> = {}

  let page = await context.newPage()
  page.setDefaultTimeout(90_000)
  traceCraft(page, "person", input.trail)
  logConsole(page, input.trail)
  const admission = captureAdmission(page)
  // Server-side view of the lease every few seconds (a GET does not renew it): if the editor
  // turns read-only, the evidence shows whether the server still saw this editor as the holder.
  let polling = true
  const leasePoll = (async () => {
    while (polling) {
      await N.pause(4_000)
      const a = admission.current()
      if (!a) continue
      const res = await context.request.get(`/api/v1/pages/${encodeURIComponent("@" + ref.pageId)}/craft/lease?workspace_id=${encodeURIComponent(ref.workspaceId)}`, { headers: { "X-Craft-Session": a.token }, timeout: 10_000 }).catch(() => null)
      const v = res && res.ok() ? ((await res.json().catch(() => null)) as { mine?: boolean; can_write?: boolean; expires_at?: string; holder?: { kind?: string } } | null) : null
      input.trail.push({ at: new Date().toISOString(), who: "lease-poll", method: "GET", path: v ? `mine=${v.mine} can_write=${v.can_write} holder=${v.holder?.kind ?? "none"} expires=${v.expires_at ?? ""}` : "unavailable", status: res?.status() ?? "failed" })
    }
  })()
  context.on("close", () => (polling = false))
  await N.openEditor(page, ref)
  let f = await N.acquire(page, d.app)
  note("editor open, editing")
  if (d.warmup) {
    // A freshly opened grid can drop its first keystrokes: prove it takes input, with up to three tries.
    let warm: string | undefined
    for (let attempt = 0; attempt < 3 && !warm; attempt++) {
      f = await N.frameOf(page, d.app)
      warm = await d.warmup(page, f, lastVersion)
    }
    if (!warm) failures.push("the freshly opened editor did not take input")
    else lastVersion = warm
    note("warm-up done")
  }

  const cycle = async (): Promise<void> => {
    const frac = (Date.now() - t0) / total
    // The host remounts the editor frame after a save or a lease change: take the current one and focus it.
    f = await N.frameOf(page, d.app)
    await f.locator("canvas").first().click({ position: d.app === "wordcraft" ? { x: 678, y: 324 } : { x: 54, y: 240 } })
    const typed = await d.work(page, f, cycles, frac, note, async (n) => void (await prd.evidence(`step-${n}.png`, await page.screenshot())))
    if (cycles < 2) await prd.evidence(`step-cycle-${cycles}-typed.png`, await page.screenshot()).catch(() => {})
    // The editor saves by itself 300 ms after the last keystroke. Take that
    // save when it comes; press Ctrl+S only when it does not (Ctrl+S always
    // sends, so after an autosave it would store identical bytes again).
    let v = await saveByAutosaveOrKey(page, lastVersion)
    if (v) lastVersion = v
    if (!v) {
      // The label can miss a save that did happen: the server is the judge.
      const head = await readHeadText(api, ref)
      if (typed.every((m) => head.text.includes(m))) v = head.version
    }
    if (!v) failures.push(`cycle ${cycles}: the save was not confirmed (${(await N.status(page)).slice(0, 80)})`)
    else saved.push(...typed)
    const ui = await N.uiState(page)
    note(`cycle ${cycles}: ${ui.status.slice(0, 40)}`)
    if (ui.alerts.length) failures.push(`cycle ${cycles}: the editor shows an alert: ${ui.alerts.join(" | ").slice(0, 200)}`)
    if (/Pouze čtení|odpojen/.test(ui.status)) {
      await prd.evidence("failure-not-editable.png", await page.screenshot({ fullPage: true })).catch(() => {})
      throw new Error(`cycle ${cycles}: the editor is no longer editable (${ui.status.slice(0, 60)})`)
    }
    cycles++
  }
  const pacedUntil = async (end: number): Promise<void> => {
    while (Date.now() - t0 < end) {
      const next = t0 + (cycles + 1) * cycleMs
      const startedCycle = Date.now()
      await cycle()
      const wait = Math.min(next, t0 + end) - Date.now()
      if (wait > 0 && Date.now() - startedCycle < cycleMs) await N.pause(wait)
    }
  }

  // First half.
  await pacedUntil(total / 2)

  // Unsaved typing, then the restart: the editor saves 300 ms after the last
  // keystroke, so the person keeps typing in a steady stream while the server
  // restarts.
  const unsaved = `PRDUNSAVED${tag}`
  f = await N.frameOf(page, d.app)
  await placeCaret(page, d.app, 200)
  const progress = newTyping()
  const typing = typeLines(page, unsaved, 40, 40, 120, progress)
  await page.waitForTimeout(2_500)
  const before = await readHeadText(api, ref)
  hashes.beforeRestart = before.hash
  if (before.text.includes(`${unsaved}-0`)) failures.push("the typing was saved before the restart; there was no unsaved work to lose")
  failures.push(...d.verify(before.text, saved, "before the restart"))
  const savesBefore = ops.filter((o) => o.kind === "save").length
  if (progress.accepted.length === 0) failures.push("no line was typed before the restart")
  note("restart")
  restart = await input.restart(`half time of the ${minutes}-minute session`)
  note("server back")
  const typedLines = await typing
  // Every line the editor took (also during the restart) must survive, saved or in the draft.
  const typedBefore = typedMarkers(unsaved, progress.accepted)

  // The open editor after the restart: what it shows on its own.
  const timeline: Awaited<ReturnType<typeof N.uiState>>[] = []
  for (let i = 0; i < 30; i++) {
    await N.pause(2_000)
    timeline.push(await N.uiState(page))
    const last = timeline.at(-1)!
    if (last.recoveries.length || last.alerts.length || /odpojen|Uloženo/.test(last.status)) break
  }
  const sentByEditor = ops.slice(savesBefore).filter((o) => o.kind === "save")
  const after = await readHeadText(api, ref)
  hashes.afterRestart = after.hash
  failures.push(...d.verify(after.text, saved, "after the restart"))
  const savedByEditor = after.text.includes(`${unsaved}-0`)
  if (savedByEditor && !holdsAll(after.text, typedBefore)) failures.push(`the editor saved only part of the ${typedBefore.length} lines it took`)
  if (!savedByEditor && sentByEditor.some((o) => o.status === 200)) failures.push("the editor saved after the restart although the draft is not on the server")

  // The disconnected editor still offers the unsaved typing: look into it before leaving.
  const draftsInSession = savedByEditor ? null : await inspectDrafts(page, typedBefore.length ? typedBefore : [`${unsaved}-0`]).catch((e) => ({ rows: [] as string[], hasMarker: [] as boolean[], serverHasMarker: [] as boolean[], error: String(e).slice(0, 200) }))
  // The person reloads (after the editor's lease has run out): the document
  // opens, takes the editor again, and a draft is offered again (a draft is
  // never imported into a read-only view).
  await N.pause(20_000)
  await page.close()
  page = await context.newPage()
  page.setDefaultTimeout(90_000)
  traceCraft(page, "person", input.trail)
  logConsole(page, input.trail)
  await N.openEditor(page, ref)
  await N.frameOf(page, d.app)
  await N.pause(2_000)
  const reopened = await N.uiState(page)
  f = await N.acquire(page, d.app)
  note("editing again")
  await N.pause(1_500)
  const drafts = savedByEditor ? null : await inspectDrafts(page, typedBefore.length ? typedBefore : [`${unsaved}-0`])
  const offered = Boolean(draftsInSession?.hasMarker.some(Boolean) || drafts?.hasMarker.some(Boolean))
  if (!savedByEditor && !offered) failures.push(`after the restart the ${typedBefore.length} lines the editor took are neither saved nor all offered as a draft (in the old editor: ${JSON.stringify(draftsInSession)}; after the reload: ${JSON.stringify(drafts)}; ui ${JSON.stringify(reopened)})`)
  if (!savedByEditor && !drafts?.hasMarker.some(Boolean)) failures.push("after the reload and taking the editor the draft with every line is not offered again")
  const reloaded = await readHeadText(api, ref)
  hashes.afterReload = reloaded.hash
  if (reloaded.hash !== after.hash) failures.push("reloading the editor changed the saved document")
  failures.push(...d.verify(reloaded.text, saved, "after the reload"))
  afterRestart = { typedLines, acceptedLines: progress.accepted, typingStoppedBy: progress.stoppedBy ?? null, timeline: timeline.slice(-3), sentByEditor: sentByEditor.length, savedByEditor, draftsInSession, reopened, drafts, version: reloaded.version }
  note("reloaded, editor reopened")

  // Second half on the reloaded editor.
  await pacedUntil(total)
  const fin = await readHeadText(api, ref)
  hashes.final = fin.hash
  failures.push(...d.verify(fin.text, saved, "at the end"))
  await N.release(page).catch(() => {})
  await page.reload()
  await N.frameOf(page, d.app)
  await N.pause(2_000)
  const last = await readHeadText(api, ref)
  hashes.afterFinalReload = last.hash
  if (last.hash !== fin.hash) failures.push("the last reload changed the saved document")
  failures.push(...d.verify(last.text, saved, "after the last reload"))
  const alerts = (await N.uiState(page)).alerts
  if (alerts.length) failures.push(`after the last reload the editor shows an alert: ${alerts.join(" | ").slice(0, 200)}`)
  await prd.evidence("final-editor.png", await page.screenshot({ fullPage: true }))

  // Versions: every committed save is one version; none appears twice.
  const committed = ops.filter((o) => o.kind === "save" && o.status === 200).length
  const repeats = ops.filter((o, i) => o.status === 200 && o.contentHash && ops.slice(0, i).some((p) => p.status === 200 && p.contentHash === o.contentHash))
  if (repeats.length) failures.push(`saves repeated identical bytes: ${repeats.map((o) => o.n).join(",")}`)
  if (BigInt(fin.version) - BigInt(ref.initVersion) > BigInt(committed + ops.filter((o) => o.kind === "save" && o.status === undefined).length)) failures.push(`the head version ${fin.version} is ahead of the saves the editor sent (${committed} committed)`)

  polling = false
  await leasePoll
  return {
    app: d.app,
    pageId: ref.pageId,
    minutes,
    startedAt: startedAt.toISOString(),
    durationMs: Date.now() - t0,
    cycles,
    saves: committed,
    markers: saved.length,
    restart,
    contentHash: { beforeRestart: hashes.beforeRestart, afterRestart: hashes.afterRestart, afterReload: hashes.afterReload, final: hashes.final, afterFinalReload: hashes.afterFinalReload },
    afterRestart,
    steps,
    failures,
  }
}

// ---------------------------------------------------------------------------
// Word

const WORD_UI = {
  homeTab: { x: 76, y: 53 },
  insertTab: { x: 130, y: 53 },
  heading1: { x: 918, y: 94 },
  table: { x: 150, y: 94 },
  table2x2: { x: 166, y: 200 },
  pictures: { x: 218, y: 94 },
} as const

export function wordSession(input: SessionInput) {
  const tag = Math.random().toString(36).slice(2, 7)
  const m = { heading: `HOURHEAD${tag}`, bold: `HOURBOLD${tag}`, cell: `HOURCELL${tag}`, font: `HOURFONT${tag}` }
  const done = new Set<string>()
  const driver: Driver = {
    app: "wordcraft",
    async work(page, f, cycle, frac, step, shot) {
      const kb = page.keyboard
      const canvas = f.locator("canvas").first()
      const click = async (at: { x: number; y: number }) => {
        await canvas.click({ position: at })
        await page.waitForTimeout(700)
      }
      const typed: string[] = []
      const tick = async (label: string) => step(`${label}: ${(await N.status(page)).slice(0, 36)}`)
      const once = async (name: string, at: number, fn: () => Promise<void>) => {
        if (frac < at || done.has(name)) return
        done.add(name)
        step(name)
        await shot(`${name}-before`).catch(() => {})
        await fn()
        await shot(`${name}-after`).catch(() => {})
      }
      await kb.press("Control+End")
      await once("heading", 0, async () => {
        await click(WORD_UI.homeTab)
        await tick("home tab")
        await kb.press("Control+End")
        await kb.type(m.heading)
        await tick("heading typed")
        await click(WORD_UI.heading1)
        await tick("heading style")
        await kb.press("End")
        await kb.press("Enter")
        await tick("enter")
        await kb.press("Control+b")
        await kb.type(m.bold)
        await tick("bold typed")
        await kb.press("Control+b")
        await kb.press("Enter")
        await tick("bold off")
      })
      await once("font", 0.08, async () => {
        await kb.press("Control+End")
        await kb.type(m.font)
        await kb.press("Shift+Home")
        await kb.press("Control+Shift+Period")
        await kb.press("Control+Shift+Period")
        await kb.press("End")
        await kb.press("Enter")
      })
      await once("table", 0.16, async () => {
        await kb.press("Control+End")
        await kb.press("Enter")
        await click(WORD_UI.insertTab)
        await click(WORD_UI.table)
        await click(WORD_UI.table2x2)
        await kb.type(m.cell)
        await kb.press("Control+End")
      })
      await once("image", 0.25, async () => {
        // The Insert tab is still open from the table step.
        await kb.press("Control+End")
        const chooser = page.waitForEvent("filechooser", { timeout: 30_000 })
        await click(WORD_UI.pictures)
        await (await chooser).setFiles({ name: "prd.png", mimeType: "image/png", buffer: PNG })
        await page.waitForTimeout(1500)
        await kb.press("Control+End")
      })
      const marker = `HOUR${tag}C${String(cycle).padStart(3, "0")}`
      await kb.press("Control+End")
      await kb.press("Enter")
      await kb.type(`${marker} the quick brown fox jumps over the lazy dog`, { delay: 8 })
      await kb.press("Enter")
      await kb.type(`${marker}B second line of the cycle`, { delay: 8 })
      typed.push(marker, `${marker}B`)
      return typed
    },
    verify(text, savedMarkers, stage) {
      const out: string[] = []
      const lost = present(text, savedMarkers)
      if (lost.missing.length) out.push(`${stage}: ${lost.missing.length} saved passages are missing, first ${lost.missing.slice(0, 3).join(", ")}`)
      const facts = wordFacts(text, m)
      for (const name of done) {
        const key = name === "heading" ? ["heading", "bold"] : [name]
        for (const k of key) if (!facts[k as keyof typeof facts]) out.push(`${stage}: ${k} is missing from the saved document`)
      }
      return out
    },
  }
  return { run: () => runSession(input, driver) }
}

// ---------------------------------------------------------------------------
// Sheet

export function sheetSession(input: SessionInput) {
  const tag = Math.random().toString(36).slice(2, 7)
  const done = new Set<string>()
  const SHEET_NAME_BOX = { x: 68, y: 178 }
  const cellOf = (cycle: number) => ({ row: 2 + cycle, marker: `HOUR${tag}R${String(2 + cycle).padStart(3, "0")}`, number: cycle * 3 + 7 })
  const goto = async (page: Page, f: Frame, addr: string) => {
    await f.locator("canvas").first().click({ position: SHEET_NAME_BOX })
    await page.keyboard.press("Control+a")
    await page.keyboard.type(addr)
    await page.keyboard.press("Enter")
    await N.pause(300)
  }
  const driver: Driver = {
    app: "gridcraft",
    async warmup(page, f, after) {
      await f.locator("canvas").first().click({ position: { x: 54, y: 240 } })
      await goto(page, f, "Z1")
      await page.keyboard.type(`HOUR${tag}WARM`, { delay: 8 })
      await page.keyboard.press("Enter")
      return saveByAutosaveOrKey(page, after)
    },
    async work(page, f, cycle, frac, step, shot) {
      const kb = page.keyboard
      const c = cellOf(cycle)
      for (let k = 0; k < 3; k++) {
        await kb.press("Control+PageUp")
        await f.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
      }
      // One row per cycle: text, number, formula, date, boolean; Tab moves right, Enter ends the row.
      await goto(page, f, `A${c.row}`)
      await kb.type(c.marker, { delay: 8 })
      await kb.press("Tab")
      await kb.type(String(c.number))
      await kb.press("Tab")
      await kb.type(`=B${c.row}*2`)
      await kb.press("Tab")
      await kb.type("2026-10-31")
      await kb.press("Tab")
      await kb.type("TRUE")
      await kb.press("Enter")
      if (frac >= 0.1 && !done.has("bold")) {
        done.add("bold")
        step("bold header cell")
        await goto(page, f, `A${c.row}`)
        await kb.press("Control+b")
        await shot("bold-after").catch(() => {})
      }
      if (frac >= 0.2 && !done.has("sheet")) {
        done.add("sheet")
        step("second sheet")
        await kb.press("Shift+F11")
        await N.pause(500)
        await shot("sheet-after").catch(() => {})
      }
      return [c.marker]
    },
    verify(text, savedMarkers, stage) {
      const out: string[] = []
      const facts = readFacts(Buffer.from(text))
      for (const marker of savedMarkers) {
        const row = Number(/R(\d{3})$/.exec(marker)?.[1])
        const number = (row - 2) * 3 + 7
        const a = facts.cells[`Sheet1!A${row}`]
        const b = facts.cells[`Sheet1!B${row}`]
        const cc = facts.cells[`Sheet1!C${row}`]
        if (a?.value !== marker) out.push(`${stage}: Sheet1!A${row} is ${JSON.stringify(a?.value)}, expected ${marker}`)
        else if (b?.value !== number) out.push(`${stage}: Sheet1!B${row} is ${JSON.stringify(b?.value)}, expected ${number}`)
        else if (cc?.formula !== `B${row}*2` || cc.value !== number * 2) out.push(`${stage}: Sheet1!C${row} is ${JSON.stringify(cc)}, expected =B${row}*2 = ${number * 2}`)
        if (out.length >= 5) break
      }
      if (done.has("sheet") && facts.sheets.length < 2) out.push(`${stage}: the second sheet is missing`)
      return out
    },
  }
  return { run: () => runSession(input, driver) }
}
