// The work of the hour rows (craft-hour.acceptance.ts): what the person does
// in the editor each cycle, what the saved document must then contain, and the
// restart in the middle. Not a test file.
import type { APIRequestContext, BrowserContext, Frame, Page } from "@playwright/test"
import { readFacts } from "../craft-lease/sheet-workbook"
import { captureAdmission, traceCraft, type CraftCall } from "../craft-lease/helpers"
import type { PrdScenario } from "../fixtures"
import * as N from "../outage-lostack/native"
import { holdsAll, inspectDrafts, newTyping, offerEvidence, placeCaret, saveByAutosaveOrKeyReport, typedMarkers, typeLines, type DraftOffer } from "./draft"
import { checkDraftCopies, checkLines, holdsAnyLine, lineLedger, type LineCheck } from "./lines"
import { present, sha256, wordFacts } from "./facts"
import { VERIFY_AFTER_MS, isUnconfirmedStatus, verifyDue, type VerifyPress, type ForcedCtrlS, type SaveWait, type TrailMark, finishPress, markTrail, startPress, startVerify, trackedSaveWait, auditStep, baselineBefore, cellInTable, confirmCycle, describeAudit, holdsMarker, markerReport, pendingReplies, sentSince, tableRunsLeft, type CycleConfirmation, type MarkerReport } from "./confirm"
import type { RestartRecord } from "./restart"
import { restartNote } from "./restart-outcome"

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
  /**
   * The per-cycle evidence chain, the checkpoints, every "Ověřit výsledek" press, every save
   * wait and every Ctrl+S the harness pressed (save accounting, confirm.ts) and the restart
   * (filled while the session runs, so a failed run still shows them).
   */
  evidence: { chain: CycleChain[]; checkpoints: Checkpoint[]; verifications: VerifyPress[]; saveWaits: SaveWait[]; forcedCtrlS: ForcedCtrlS[]; releases: TrailMark[]; editorEnds: TrailMark[]; restart: RestartRecord | null }
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
  /** Where the stored versions can be read later: the QA workspace is kept for the hour rows. */
  workspaceId: string
  /** Per cycle, the save from the keystrokes to the stored head (compact; marker excerpts only). */
  chain: CycleChain[]
  /** The stored head at the restart and reload checkpoints, against the saved markers. */
  checkpoints: Checkpoint[]
}

/**
 * One cycle's evidence chain: (1) what the editor showed right after the typing (the canvas
 * text itself is not observable from the page; the status line and its save trace are),
 * (2)-(4) every save the editor sent in the cycle (body SHA-256, base version, time, reply
 * status and version), (5) the stored head after the cycle against every saved marker and the
 * table cell text, (6) what the editor showed after the save.
 */
export interface CycleChain {
  cycle: number
  typed: string[]
  local: { observed: string; status: string; savePhase: string | null; saveReason: string | null }
  /** The recorded requests themselves (not copies): a reply that arrives later is in the chain when it is written. */
  sent: N.Op[]
  /** Requests without a reply or failure when the cycle ended (their final state is in `sent`). */
  pendingAtCycleEnd: number[]
  head: { version: string; sha256: string; markers: MarkerReport }
  confirmed: boolean
  editor: { label: string | null; status: string; savePhase: string | null; saveReason: string | null }
}

/** (7) The stored head at a checkpoint: version, hash, whether it equals the previous checkpoint, and the marker set. */
export interface Checkpoint {
  stage: string
  version: string
  sha256: string
  sameAsPrevious: boolean | null
  markers: MarkerReport
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

/** The status line with its save trace, as attributes. */
async function statusTrace(page: Page): Promise<{ status: string; savePhase: string | null; saveReason: string | null }> {
  const s = page.getByRole("status").first()
  const [status, savePhase, saveReason] = await Promise.all([
    N.status(page),
    s.getAttribute("data-save-phase", { timeout: 2_000 }).catch(() => null),
    s.getAttribute("data-save-reason", { timeout: 2_000 }).catch(() => null),
  ])
  return { status: status.slice(0, 80), savePhase, saveReason }
}

/** The status line and the save trace, for a failure line. */
async function uiEvidence(page: Page): Promise<string> {
  const t = await statusTrace(page)
  return `${t.status.slice(0, 60)}; data-save-phase=${t.savePhase ?? "none"} data-save-reason=${t.saveReason ?? "none"}`
}

/** What a cycle's work can report back to the session. */
interface CycleHooks {
  step(name: string): void
  shot(name: string): Promise<void>
  /**
   * Call right before the cycle's own marker typing, after the steps that save by
   * themselves (table, picture): their saves must not count as the cycle's save (#4871).
   */
  beforeTyping(): Promise<void>
  /** An input problem of a step (the person's typing did not reach the document), not a lost save. */
  fail(problem: string): void
  /** The markers the server confirmed so far (a step checks they survive its own typing). */
  savedMarkers(): string[]
  /**
   * Saved markers the step's own typing changed (harness input, with evidence): they leave
   * the saved list, so the later checks do not report them as lost saves, and the row fails.
   */
  harnessInput(markers: string[], problem: string): void
}

/** What differs between Word and Sheet: how a cycle works and how the saved document is checked. */
interface Driver {
  app: SessionKind
  /** One cycle of work; returns the markers it typed (the session then confirms them on the server). */
  work(page: Page, f: Frame, cycle: number, frac: number, hooks: CycleHooks): Promise<string[]>
  /** Optional first action that proves the freshly opened editor takes input: the save label it saw and the markers it typed. */
  warmup?(page: Page, f: Frame, after: string, save: (page: Page, after: string) => Promise<string | undefined>): Promise<{ label: string | undefined; typed: string[] }>
  /** The table step's cell text, reported in the evidence chain (Word). */
  cell?: string
  /** Problems found in the saved document (empty when it holds everything the session did). */
  verify(text: string, saved: string[], stage: string): string[]
}

const newer = (a: string, b: string | undefined) => (b !== undefined && BigInt(b) > BigInt(a) ? b : a)

/**
 * saveByAutosaveOrKey with the save accounting (confirm.ts, trackedSaveWait): the wait is
 * recorded when it starts, a forced Ctrl+S when it is dispatched, and both are closed
 * even when the wait throws. Same waits and timeouts; returns the version only.
 */
function recordedSave(input: SessionInput, page: Page, after: string, cycle: number | "warmup", context = ""): Promise<string | undefined> {
  return trackedSaveWait(input.evidence, input.ops, { cycle, reason: `no autosave within 4 s (no "Uloženo · verze" newer than ${after})${context}` }, (onPress) => saveByAutosaveOrKeyReport(page, after, onPress))
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
  const { chain, checkpoints } = input.evidence
  const checkpoint = (stage: string, head: { version: string; text: string; hash: string }) => {
    const prev = checkpoints.at(-1)
    checkpoints.push({ stage, version: head.version, sha256: head.hash, sameAsPrevious: prev ? prev.sha256 === head.hash : null, markers: markerReport(head.text, saved, d.cell) })
  }
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
  // A cycle's typing counts as saved only when the server head holds every marker; the
  // label is not trusted, an earlier step's save can already sit on it (confirm.ts, #4871).
  // A read can race the save the label just announced: re-read for a few seconds.
  let lastHead: { version: string; text: string; hash: string } | undefined
  const confirmTyped = async (typed: string[], baseline: string, label: string | undefined): Promise<CycleConfirmation> => {
    let head = await readHeadText(api, ref)
    for (const end = Date.now() + 5_000; !typed.every((m) => holdsMarker(head.text, m)) && Date.now() < end; ) {
      await N.pause(1_000)
      head = await readHeadText(api, ref)
    }
    lastHead = head
    return confirmCycle({ typed, baseline, label, head })
  }
  // A stalled renderer leaves "Výsledek nepotvrzen" and the editor then sends nothing. Do
  // what a person does once it stays past the deadline, and keep the record. It never
  // counts as a save: confirmTyped below reads the server.
  const verifyIfUnconfirmed = async (cycle: number): Promise<boolean> => {
    let since: number | undefined
    for (const end = Date.now() + VERIFY_AFTER_MS + 5_000; Date.now() < end; ) {
      const status = await N.status(page)
      if (!isUnconfirmedStatus(status)) return false
      since ??= Date.now()
      if (verifyDue(status, since, Date.now())) {
        const opsAt = ops.length
        // In the evidence from the dispatch on: an interrupted wait keeps the press (no waitEndedAt).
        const v = startVerify(input.evidence.verifications, { cycle, firstSeenAt: new Date(since).toISOString(), opsAtPress: opsAt })
        let statusAfter = ""
        try {
          await page.getByRole("button", { name: "Ověřit výsledek", exact: true }).click()
          for (const limit = Date.now() + 15_000; isUnconfirmedStatus(await N.status(page)) && Date.now() < limit; ) await N.pause(250)
          const reconcile = ops.slice(opsAt).find((o) => o.kind === "reconcile")
          statusAfter = (await N.status(page)).slice(0, 60)
          Object.assign(v, { unconfirmedForMs: Date.now() - since, statusAfter, reconcile: reconcile ? { status: reconcile.status, version: reconcile.version, failed: reconcile.failed } : null, waitEndedAt: new Date().toISOString() })
        } catch (e) {
          v.statusAfter = `interrupted: ${String(e).slice(0, 80)}`
          throw e
        }
        note(`cycle ${cycle}: pressed Ověřit výsledek after ${Date.now() - since} ms: ${statusAfter}`)
        return true
      }
      await N.pause(500)
    }
    return false
  }
  await N.openEditor(page, ref)
  let f = await N.acquire(page, d.app)
  note("editor open, editing")
  if (d.warmup) {
    // A freshly opened grid can drop its first keystrokes: prove it takes input, with up to three tries.
    let warm: CycleConfirmation | undefined
    for (let attempt = 0; attempt < 3 && !warm?.confirmed; attempt++) {
      f = await N.frameOf(page, d.app)
      const w = await d.warmup(page, f, lastVersion, (p, after) => recordedSave(input, p, after, "warmup"))
      warm = await confirmTyped(w.typed, lastVersion, w.label)
      lastVersion = newer(newer(lastVersion, w.label), warm.version)
    }
    if (!warm?.confirmed) failures.push(`the freshly opened editor did not take input: ${warm?.problem ?? "no attempt"}`)
    note("warm-up done")
  }

  const cycle = async (): Promise<void> => {
    const frac = (Date.now() - t0) / total
    // The host remounts the editor frame after a save or a lease change: take the current one and focus it.
    f = await N.frameOf(page, d.app)
    await f.locator("canvas").first().click({ position: d.app === "wordcraft" ? { x: 678, y: 324 } : { x: 54, y: 240 } })
    const n = cycles
    const opsAt = ops.length
    let baseline = lastVersion
    const typed = await d.work(page, f, n, frac, {
      step: note,
      shot: async (name) => void (await prd.evidence(`step-${name}.png`, await page.screenshot())),
      beforeTyping: async () => {
        // Let a save of an earlier step finish so its version becomes the baseline.
        for (const end = Date.now() + 5_000; /^Ukládám/.test(await N.status(page)) && Date.now() < end; ) await N.pause(150)
        baseline = baselineBefore(lastVersion, await N.status(page))
      },
      fail: (problem) => failures.push(`cycle ${n}: ${problem}`),
      savedMarkers: () => [...saved],
      harnessInput: (markers, problem) => {
        for (const m of markers) {
          const i = saved.indexOf(m)
          if (i >= 0) saved.splice(i, 1)
        }
        failures.push(`cycle ${n}: ${problem}`)
      },
    })
    // (1) What the editor showed right after the typing, before the save.
    const local = await statusTrace(page)
    if (cycles < 2) await prd.evidence(`step-cycle-${cycles}-typed.png`, await page.screenshot()).catch(() => {})
    // The editor saves by itself 300 ms after the last keystroke. Take that
    // save when it comes; press Ctrl+S only when it does not. After an autosave
    // Ctrl+S sends nothing and confirms the saved version (#5075).
    await verifyIfUnconfirmed(n)
    let label = await recordedSave(input, page, baseline, n)
    if (await verifyIfUnconfirmed(n)) label = (await recordedSave(input, page, newer(baseline, label), n, ", after an Ověřit výsledek press")) ?? label
    // The server is the judge, also when the label moved (#4871).
    const c = await confirmTyped(typed, baseline, label)
    lastVersion = newer(newer(baseline, label), c.version)
    if (c.confirmed) saved.push(...typed)
    else failures.push(`cycle ${n}: ${c.problem} (${await uiEvidence(page)})`)
    const shown = await statusTrace(page)
    const head = lastHead ?? (await readHeadText(api, ref))
    chain.push({
      cycle: n,
      typed,
      local: { observed: "status line only; the canvas text is not observable from the page", ...local },
      sent: sentSince(ops, opsAt),
      pendingAtCycleEnd: pendingReplies(ops.slice(opsAt)),
      head: { version: head.version, sha256: head.hash, markers: markerReport(head.text, saved, d.cell) },
      confirmed: c.confirmed,
      editor: { label: label ?? null, ...shown },
    })
    const ui = await N.uiState(page)
    note(`cycle ${n}: ${ui.status.slice(0, 40)}${c.confirmed ? ` · server v${c.version} holds the typing` : " · NOT on the server"}`)
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
  // Wait (bounded) for the first line the editor provably took; a stalled
  // renderer can take seconds to answer a status read.
  for (const end = Date.now() + 30_000; progress.accepted.length === 0 && Date.now() < end; ) await N.pause(250)
  const before = await readHeadText(api, ref)
  hashes.beforeRestart = before.hash
  checkpoint("beforeRestart", before)
  if (before.text.includes(`${unsaved}-0`)) failures.push("the typing was saved before the restart; there was no unsaved work to lose")
  failures.push(...d.verify(before.text, saved, "before the restart"))
  const savesBefore = ops.filter((o) => o.kind === "save").length
  if (progress.accepted.length === 0) failures.push(`no line was typed before the restart; typing progress: ${JSON.stringify(progress)}`)
  note("restart")
  restart = await input.restart(`half time of the ${minutes}-minute session`)
  input.evidence.restart = restart
  note(restartNote(restart))
  note("server back")
  const typedLines = await typing
  // Every line the editor took (also during the restart) must survive, saved or in the draft.
  const typedBefore = typedMarkers(unsaved, progress.accepted)
  // Per line: typed, accepted, then found once and in order in each copy (draft
  // or server-confirmed head); a missing line fails with its last state (lines.ts).
  const ledger = lineLedger(unsaved, progress)
  const lineChecks: LineCheck[] = []
  const lineFailures = (c: LineCheck) => {
    lineChecks.push(c)
    failures.push(...c.problems)
  }

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
  checkpoint("afterRestart", after)
  failures.push(...d.verify(after.text, saved, "after the restart"))
  const savedByEditor = after.text.includes(`${unsaved}-0`)
  if (savedByEditor && !holdsAll(after.text, typedBefore)) failures.push(`the editor saved only part of the ${typedBefore.length} lines it took`)
  // Any line on the server (not only the first) means the editor saved: then it saved all of them.
  if (savedByEditor || holdsAnyLine(ledger, after.text)) lineFailures(checkLines(ledger, after.text, `server head v${after.version} after the restart`))
  if (!savedByEditor && sentByEditor.some((o) => o.status === 200)) failures.push("the editor saved after the restart although the draft is not on the server")

  // The disconnected editor still offers the unsaved typing: look into it before leaving.
  let inSessionError: string | null = null
  const draftsInSession: DraftOffer | null = savedByEditor ? null : await inspectDrafts(page, typedBefore.length ? typedBefore : [`${unsaved}-0`]).catch((e) => {
    inSessionError = String(e).slice(0, 200)
    return { rows: [] as string[], hasMarker: [] as boolean[], serverHasMarker: [] as boolean[], texts: [] as string[] }
  })
  const linesInSession = draftsInSession?.texts.length ? checkDraftCopies(ledger, draftsInSession.texts, "draft in the open editor after the restart") : null
  // The person reloads (after the editor's lease has run out): the document
  // opens, takes the editor again, and a draft is offered again (a draft is
  // never imported into a read-only view).
  await N.pause(20_000)
  await page.close()
  // The closed page's editor sends nothing more: its presses cannot cause a later save.
  markTrail(input.evidence.editorEnds, ops)
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
  const linesAfterReload = drafts ? checkDraftCopies(ledger, drafts.texts, "draft offered after the reload") : null
  const offered = Boolean(draftsInSession?.hasMarker.some(Boolean) || drafts?.hasMarker.some(Boolean))
  if (!savedByEditor && !offered) failures.push(`after the restart the ${typedBefore.length} lines the editor took are neither saved nor all offered as a draft (in the old editor: ${JSON.stringify({ ...offerEvidence(draftsInSession), error: inSessionError })}; after the reload: ${JSON.stringify(offerEvidence(drafts))}; ui ${JSON.stringify(reopened)})`)
  if (!savedByEditor && !drafts?.hasMarker.some(Boolean)) failures.push("after the reload and taking the editor the draft with every line is not offered again")
  // Every offered draft that was inspected holds every line once and in order.
  if (linesInSession) lineFailures(linesInSession)
  if (linesAfterReload) lineFailures(linesAfterReload)
  const reloaded = await readHeadText(api, ref)
  hashes.afterReload = reloaded.hash
  checkpoint("afterReload", reloaded)
  if (reloaded.hash !== after.hash) failures.push("reloading the editor changed the saved document")
  failures.push(...d.verify(reloaded.text, saved, "after the reload"))
  afterRestart = { typedLines, acceptedLines: progress.accepted, typingStoppedBy: progress.stoppedBy ?? null, timeline: timeline.slice(-3), sentByEditor: sentByEditor.length, savedByEditor, draftsInSession: draftsInSession && { ...offerEvidence(draftsInSession), error: inSessionError }, reopened, drafts: offerEvidence(drafts), lines: { ledger, checks: lineChecks }, version: reloaded.version }
  note("reloaded, editor reopened")

  // Second half on the reloaded editor.
  await pacedUntil(total)
  const fin = await readHeadText(api, ref)
  hashes.final = fin.hash
  checkpoint("final", fin)
  failures.push(...d.verify(fin.text, saved, "at the end"))
  if (savedByEditor) lineFailures(checkLines(ledger, fin.text, `server head v${fin.version} at the end`))
  // Releasing flushes the editor before the transfer: a harness intervention in the save accounting.
  markTrail(input.evidence.releases, ops)
  await N.release(page).catch(() => {})
  await page.reload()
  await N.frameOf(page, d.app)
  await N.pause(2_000)
  const last = await readHeadText(api, ref)
  hashes.afterFinalReload = last.hash
  checkpoint("afterFinalReload", last)
  if (last.hash !== fin.hash) failures.push("the last reload changed the saved document")
  failures.push(...d.verify(last.text, saved, "after the last reload"))
  if (savedByEditor) lineFailures(checkLines(ledger, last.text, `server head v${last.version} after the last reload`))
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
    workspaceId: ref.workspaceId,
    chain,
    checkpoints,
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
  // No point on the page itself: where the document sits on the canvas depends on the
  // scroll and the page breaks. In the RC-4 rerun a fixed "first cell" point hit the
  // previous cycle's B line and the cell text split a saved marker (#4877).
  pictures: { x: 218, y: 94 },
} as const

export function wordSession(input: SessionInput) {
  const tag = Math.random().toString(36).slice(2, 7)
  const m = { heading: `HOURHEAD${tag}`, bold: `HOURBOLD${tag}`, cell: `HOURCELL${tag}`, font: `HOURFONT${tag}` }
  const done = new Set<string>()
  let tableRuns = 0
  // Did the cell text reach a table cell of the server head? The editor saves 300 ms
  // after the last keystroke; an unsaved draft gets Ctrl+S once, as in saveByAutosaveOrKey.
  // The press is recorded in the save accounting (confirm.ts), with whether a newer head followed.
  const cellSaved = async (page: Page, cycle: number): Promise<boolean> => {
    let forced: { event: ForcedCtrlS; headVersion: string } | undefined
    let newerHead: string | null = null
    let completed = false
    try {
      for (const start = Date.now(); Date.now() - start < 15_000; ) {
        const head = await readHeadText(input.api, input.ref)
        if (forced && newerHead === null && BigInt(head.version) > BigInt(forced.headVersion)) newerHead = head.version
        if (cellInTable(head.text, m.cell)) {
          completed = true
          return true
        }
        if (!forced && Date.now() - start > 4_000 && /^Neuložený koncept/.test(await N.status(page))) {
          // In the evidence from the dispatch on: a press whose wait is cut short stays "incomplete".
          const event = startPress(input.evidence.forcedCtrlS, { cycle, site: "table-cell", reason: `table cell text not in a table cell of the server head v${head.version} after 4 s, status Neuložený koncept`, opsAtPress: input.ops.length })
          forced = { event, headVersion: head.version }
          await page.keyboard.press("Control+s")
        }
        await N.pause(1_000)
      }
      completed = true
      return false
    } finally {
      if (forced) finishPress(forced.event, input.ops, { version: newerHead, completed })
    }
  }
  const driver: Driver = {
    app: "wordcraft",
    cell: m.cell,
    async work(page, f, cycle, frac, { step, shot, beforeTyping, fail, savedMarkers, harnessInput }) {
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
        await shot(`${name}-c${cycle}-before`).catch(() => {})
        await fn()
        await shot(`${name}-c${cycle}-after`).catch(() => {})
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
        // A paragraph of its own: typed at the end of the last B line it would sit right after a saved marker.
        await kb.press("Control+End")
        await kb.press("Enter")
        await kb.type(m.font)
        await kb.press("Shift+Home")
        await kb.press("Control+Shift+Period")
        await kb.press("Control+Shift+Period")
        await kb.press("End")
        await kb.press("Enter")
      })
      await once("table", 0.16, async () => {
        tableRuns++
        // The saved markers this step must leave intact, as the server holds them now.
        const pre = await readHeadText(input.api, input.ref)
        const saved = savedMarkers()
        // The cell text with a space on each side: wherever it lands it cannot glue onto a marker.
        const cellText = ` ${m.cell} `
        /** Did the attempt change saved text? */
        const audit = async (attempt: string): Promise<boolean> => {
          const head = await readHeadText(input.api, input.ref)
          const a = auditStep({ before: pre.text, after: head.text, saved, inserted: m.cell })
          if (a.missingBefore.length) step(`table: before the step the server head v${pre.version} already lacked ${a.missingBefore.length} saved marker(s), first ${a.missingBefore.slice(0, 2).join(", ")}`)
          const problem = describeAudit(a, attempt, `v${pre.version} -> v${head.version}; ${await uiEvidence(page)}`)
          if (!problem) return false
          await shot(`table-c${cycle}-damaged`).catch(() => {})
          if (a.splitByStep.length) harnessInput(a.splitByStep.map((d) => d.marker), problem)
          // Not split by the cell text: the markers stay saved, so the later checks report them as missing.
          else fail(problem)
          return true
        }
        await kb.press("Control+End")
        await kb.press("Enter")
        await click(WORD_UI.insertTab)
        await click(WORD_UI.table)
        await click(WORD_UI.table2x2)
        await shot(`table-c${cycle}-inserted`).catch(() => {})
        // The person types into the new table at once. Where the text went is decided by
        // the saved document (a table cell of the server head), right here in the step.
        await kb.type(cellText)
        let landed = await cellSaved(page, cycle)
        let damaged = await audit("typing right after the 2x2 insert")
        if (landed) step("table: the cell text reached a table cell right after the 2x2 insert")
        else {
          // #4877, kept as a failure while the product bug is present: after the ribbon's
          // 2x2 insert the keyboard does not return to the document.
          fail(`typing right after 2x2 insert did not reach the document (#4877): ${m.cell} is not in a table cell of the server head (${await uiEvidence(page)})`)
          await shot(`table-c${cycle}-no-focus`).catch(() => {})
          if (!damaged) {
            // Keyboard focus back without touching the page: a click on the ribbon's Insert
            // tab is chrome, and the editor hands the keyboard back to the document with the
            // caret where the insert put it (WordCraft #1093), i.e. in the new table.
            await click(WORD_UI.insertTab)
            await kb.type(cellText)
            landed = await cellSaved(page, cycle)
            damaged = await audit("typing after the focus came back through the ribbon")
            await shot(`table-c${cycle}-after-refocus`).catch(() => {})
            step(landed ? "table: the cell text reached a table cell after the focus came back through the ribbon" : "table: the cell text is not in a table cell also after the focus came back through the ribbon")
          }
        }
        if (!landed) {
          // An input failure, not a lost save: the table is not checked as saved work. One
          // more run in a later cycle at most, none when the step changed saved text.
          if (tableRunsLeft(tableRuns, damaged) > 0) done.delete("table")
          fail(`typing after the table insert did not reach a table cell (run ${tableRuns}${damaged ? ", saved text changed by the step" : ""}): ${m.cell} is not in a table cell of the server head (${await uiEvidence(page)})`)
        }
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
      await beforeTyping()
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
    async warmup(page, f, after, save) {
      await f.locator("canvas").first().click({ position: { x: 54, y: 240 } })
      await goto(page, f, "Z1")
      await page.keyboard.type(`HOUR${tag}WARM`, { delay: 8 })
      await page.keyboard.press("Enter")
      return { label: await save(page, after), typed: [`HOUR${tag}WARM`] }
    },
    async work(page, f, cycle, frac, { step, shot, beforeTyping }) {
      const kb = page.keyboard
      const c = cellOf(cycle)
      for (let k = 0; k < 3; k++) {
        await kb.press("Control+PageUp")
        await f.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
      }
      // One row per cycle: text, number, formula, date, boolean; Tab moves right, Enter ends the row.
      await beforeTyping()
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
