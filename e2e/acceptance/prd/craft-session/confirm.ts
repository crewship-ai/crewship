// When the hour rows (hour-steps.ts) may count a cycle's typing as saved. Pure
// functions, no browser: confirm.test.mjs covers them.
//
// The save label ("Uloženo · verze N") only says that *some* save happened. A
// step earlier in the same cycle (inserting a table, a picture, a second sheet)
// saves too, so its label can already sit on screen before the cycle types
// anything (#4871, RC-4: the table step left "verze 14", the typing never reached
// the document, and "verze 14" was taken as the cycle's save). The server head is
// the judge: a cycle's markers count as saved only when the head holds every one.
// No imports: node --test loads this file directly (type stripping, no resolver).

type Node = Record<string, unknown>
function* walk(value: unknown): Generator<Node> {
  if (Array.isArray(value)) for (const v of value) yield* walk(v)
  else if (value && typeof value === "object") {
    yield value as Node
    for (const v of Object.values(value)) yield* walk(v)
  }
}

/** "Uloženo · verze N" -> "N"; anything else (saving, unsaved draft, read-only) -> undefined. */
export function labelVersion(status: string): string | undefined {
  return /^Uloženo · verze (\d+)/.exec(status)?.[1]
}

/**
 * The version the cycle's own save must be newer than: the newer of the last
 * confirmed version and the label right before the typing (which includes the
 * saves of the steps that ran earlier in the cycle).
 */
export function baselineBefore(lastVersion: string, statusBeforeTyping: string): string {
  const shown = labelVersion(statusBeforeTyping)
  return shown !== undefined && BigInt(shown) > BigInt(lastVersion) ? shown : lastVersion
}

/** `marker` as a whole token: "C010" is not satisfied by "C010B" and "R012" not by "R0120". */
export function holdsMarker(text: string, marker: string): boolean {
  const escaped = marker.replace(/[.*+?^$()|[\]\\{}]/g, "\\$&")
  return new RegExp(escaped + "(?![0-9A-Za-z])").test(text)
}

export interface CycleConfirmation {
  confirmed: boolean
  /** The server version that holds the typing (only when confirmed). */
  version?: string
  /** Why the typing is not counted as saved. */
  problem?: string
}

/**
 * Counts a cycle's markers as saved only when the server head holds every one of
 * them. The label is reported, never trusted: a label newer than the baseline
 * with a head that lacks the markers is not a save of this cycle's typing.
 */
export function confirmCycle(input: { typed: string[]; baseline: string; label: string | undefined; head: { version: string; text: string } }): CycleConfirmation {
  const { typed, baseline, label, head } = input
  if (typed.length === 0) return { confirmed: false, problem: "the cycle typed no markers" }
  const missing = typed.filter((m) => !holdsMarker(head.text, m))
  if (missing.length === 0) return { confirmed: true, version: head.version }
  const said = label ? `the label says saved (verze ${label}, baseline ${baseline})` : `no save newer than verze ${baseline} was shown`
  return {
    confirmed: false,
    problem: `${said}, but the server head v${head.version} lacks ${missing.length} of ${typed.length} typed markers, first ${missing.slice(0, 2).join(", ")}; the typing is not counted as saved`,
  }
}

/** Does the saved Word JSON hold `cell` in a paragraph inside a table node (not just anywhere)? */
export function cellInTable(raw: string, cell: string): boolean {
  let doc: unknown
  try {
    doc = JSON.parse(raw)
  } catch {
    return false
  }
  for (const n of walk(doc)) {
    if (n.kind !== "table") continue
    for (const inner of walk(Object.values(n))) if (inner.kind === "para" && holdsMarker(String(inner.text ?? ""), cell)) return true
  }
  return false
}

/**
 * What a passed hour row must not hide: the row keeps going by pressing "Ověřit
 * výsledek", so a pass alone reads as if saving recovered by itself. Intervals are
 * the page-poll gaps (pageStalls) and the unconfirmed spells up to each press,
 * merged so that an overlap is not counted twice. `longestStallMs` (name kept for
 * the evidence readers) is the longest merged interruption, not a measured
 * renderer or input stall. With `accounting`, the note starts with how the saves
 * happened (saveAccounting).
 */
export function hourInterruptions(verifications: readonly VerifyPress[], stalls: readonly PageStall[], accounting?: SaveAccounting): { presses: number; interruptedMs: number; longestStallMs: number; pollGaps: number; note: string } {
  const spans = [
    ...stalls.map((s) => [Date.parse(s.from), Date.parse(s.to)]),
    ...verifications.map((v) => [Date.parse(v.firstSeenAt), Date.parse(v.pressedAt)]),
  ].sort((a, b) => a[0] - b[0])
  let interruptedMs = 0, longestStallMs = 0, from = NaN, to = NaN
  const close = () => {
    if (Number.isNaN(from)) return
    interruptedMs += to - from
    longestStallMs = Math.max(longestStallMs, to - from)
  }
  for (const [a, b] of spans) {
    if (a > to) { close(); from = a; to = b }
    else if (Number.isNaN(from)) { from = a; to = b }
    else to = Math.max(to, b)
  }
  close()
  const seconds = (ms: number) => `${Math.round(ms / 1000)} s`
  const presses = verifications.length
  const pollGaps = stalls.length
  const note = (accounting ? accounting.note + " " : "") +
    `Ověřit výsledek pressed ${presses} times, interrupted ${seconds(interruptedMs)} in total (page-poll gaps and unconfirmed spells), longest interruption ${seconds(longestStallMs)}; ` +
    `${pollGaps} page-poll gap(s) of at least 10 s (${PAGE_STALLS_DESCRIPTION}).` +
    (presses ? " Saving did not recover by itself." : "")
  return { presses, interruptedMs, longestStallMs, pollGaps, note }
}

// ---------------------------------------------------------------------------
// How each save of the hour rows happened (save accounting). Accounting only: a
// forced Ctrl+S never fails the row; confirmCycle stays the judge of the typing.
//
// A press of Ctrl+S is not the origin of a save. The origin is the save request
// that committed the version: one sent before the press is the editor's autosave,
// even when its reply arrives after the press (Ctrl+S then only confirms the saved
// bytes, #5075); one sent after the press is the forced save. Requests are told
// apart by their place in the ops trail (instrument() appends each request when it
// is sent), so `opsAtPress` (the trail length right before the press) is the cut.
//
// The limit: a request sent after the press is not proof that the press caused it.
// An autosave already being prepared when the key goes out (snapshot, local draft,
// validation) sends its request after the press, and the request carries no trigger.
// A committing request sent within PRESS_AMBIGUITY_MS after the press is therefore
// "unattributed" and counted as an ambiguous save: neither an autosave nor a forced save.

/**
 * How long after a press an autosave that was already being prepared can still send
 * its request. The editor prepares a save in three steps, each bounded by its own
 * 10 s deadline and nothing shorter: the engine snapshot RPC and the validation RPC
 * (lib/mooring/native-editor-port.ts, `timeoutMs` 10_000 per request) and the local
 * draft write (lib/mooring/single-editor-save.ts, journalStep, `timeoutMs` 10_000);
 * the SHA-256 digest after them is in-memory. Preparations run one after another, so
 * a capture under way at the press can send up to 3 x 10 s later. Typical captures are
 * far faster, but the code promises no shorter bound, so the window is the bound.
 */
export const PRESS_AMBIGUITY_MS = 30_000

/** A save/reconcile request as instrument() records it. */
export interface AccountedOp {
  n: number
  kind: string
  sentAt?: string
  status?: number
  version?: string
}

/** One wait of saveByAutosaveOrKey (draft.ts): where a cycle's save comes from. */
export interface SaveWait {
  /** The cycle number, or "warmup" for the Sheet warm-up. */
  cycle: number | "warmup"
  at: string
  /** When the wait ended (also when it threw); null while it runs. */
  endedAt: string | null
  /** Did the harness press Ctrl+S because no new version came within 4 s. */
  pressed: boolean
  /** The ops trail length right before the press (only when pressed). */
  opsAtPress?: number
  /** When the press was dispatched (only when pressed). */
  pressedAt?: string
  /** The new version the wait ended with, or null when none came. */
  version: string | null
  /** False when the wait threw (a closed page, a keyboard error). */
  completed: boolean
}

/**
 * What a forced press led to: "new-save" (a save request sent after the press
 * committed the version), "confirm-only" (the version was committed by a request
 * sent before the press: an autosave the press only confirmed), "no-version" (no
 * new version followed), "unattributed" (a version followed, but no answered save in
 * the trail carries it, or the committing request was sent within PRESS_AMBIGUITY_MS
 * after the press, when an autosave prepared before the press cannot be told from the
 * forced save), "incomplete" (the wait was interrupted after the press).
 */
export type PressOutcome = "new-save" | "confirm-only" | "no-version" | "unattributed" | "incomplete"

/** One Ctrl+S the harness pressed (the hour rows have no Ctrl+S of the person's own). */
export interface ForcedCtrlS {
  cycle: number | "warmup"
  /** When the press was dispatched; the event exists from then on. */
  at: string
  /** Which harness code pressed it: the cycle's save wait or the Word table step's cell check. */
  site: "save-wait" | "table-cell"
  reason: string
  opsAtPress: number
  /** When the wait after the press ended (also when it threw); null while it runs. */
  endedAt: string | null
  /** Did a new version follow the press (a newer "Uloženo · verze N", or a newer server head for the table cell). */
  newVersion: boolean
  version: string | null
  outcome: PressOutcome
}

/** The save request that committed `version`: the first answered save carrying it (a repeat answers the same version, #5075). */
export function producerOf(ops: readonly AccountedOp[], version: string): AccountedOp | undefined {
  return ops.find((o) => o.kind === "save" && o.status === 200 && o.version === version)
}

/** A press's outcome, and the committing request when it is an ambiguous save (sent within PRESS_AMBIGUITY_MS after the press). */
export interface PressAttribution {
  outcome: PressOutcome
  ambiguousOp?: number
}

/** Classifies a press by the request that committed the version that followed it. */
export function attributePress(press: { opsAtPress: number; pressedAt?: string; version: string | null; completed: boolean }, ops: readonly AccountedOp[]): PressAttribution {
  if (!press.completed) return { outcome: "incomplete" }
  if (press.version === null) return { outcome: "no-version" }
  const producer = producerOf(ops, press.version)
  if (!producer) return { outcome: "unattributed" }
  if (producer.n <= press.opsAtPress) return { outcome: "confirm-only" }
  const sent = producer.sentAt ? Date.parse(producer.sentAt) : NaN
  const pressed = press.pressedAt ? Date.parse(press.pressedAt) : NaN
  // Without both times the request cannot be placed after the window.
  if (Number.isNaN(sent) || Number.isNaN(pressed) || sent - pressed <= PRESS_AMBIGUITY_MS) return { outcome: "unattributed", ambiguousOp: producer.n }
  return { outcome: "new-save" }
}

export function pressOutcome(press: { opsAtPress: number; pressedAt?: string; version: string | null; completed: boolean }, ops: readonly AccountedOp[]): PressOutcome {
  return attributePress(press, ops).outcome
}

/** Records a press at dispatch: the event is in the evidence before the key goes out. */
export function startPress(forced: ForcedCtrlS[], p: { cycle: number | "warmup"; site: ForcedCtrlS["site"]; reason: string; opsAtPress: number; at?: string }): ForcedCtrlS {
  const event: ForcedCtrlS = { cycle: p.cycle, at: p.at ?? new Date().toISOString(), site: p.site, reason: p.reason, opsAtPress: p.opsAtPress, endedAt: null, newVersion: false, version: null, outcome: "incomplete" }
  forced.push(event)
  return event
}

/** Fills in a press's outcome once its wait ended; an interrupted wait stays "incomplete". */
export function finishPress(event: ForcedCtrlS, ops: readonly AccountedOp[], result: { version: string | null; completed: boolean }, endedAt = new Date().toISOString()): void {
  event.endedAt = endedAt
  event.version = result.version
  event.newVersion = result.version !== null
  event.outcome = pressOutcome({ opsAtPress: event.opsAtPress, pressedAt: event.at, version: result.version, completed: result.completed }, ops)
}

/**
 * Runs one save wait with the accounting: the wait is recorded when it starts, a press
 * when it is dispatched (`wait` calls `onPress` right before the key), and both are
 * closed in a finally, so an interrupted wait leaves an explicit "incomplete" record.
 */
export async function trackedSaveWait(
  rec: { saveWaits: SaveWait[]; forcedCtrlS: ForcedCtrlS[] },
  ops: readonly AccountedOp[],
  p: { cycle: number | "warmup"; reason: string; now?: () => Date },
  wait: (onPress: () => void) => Promise<{ version: string | undefined }>,
): Promise<string | undefined> {
  const now = () => (p.now ? p.now() : new Date()).toISOString()
  const w: SaveWait = { cycle: p.cycle, at: now(), endedAt: null, pressed: false, version: null, completed: false }
  rec.saveWaits.push(w)
  let event: ForcedCtrlS | undefined
  try {
    const r = await wait(() => {
      w.pressed = true
      w.opsAtPress = ops.length
      w.pressedAt = now()
      event = startPress(rec.forcedCtrlS, { cycle: p.cycle, site: "save-wait", reason: p.reason, opsAtPress: ops.length, at: w.pressedAt })
    })
    w.version = r.version ?? null
    w.completed = true
    return r.version
  } finally {
    w.endedAt = now()
    if (event) finishPress(event, ops, { version: w.version, completed: w.completed }, w.endedAt)
  }
}

/**
 * Where a wait's version came from: "autosave" (no press), "late-autosave" (pressed,
 * but the committing request was sent before the press), "forced" (sent after the
 * press and outside PRESS_AMBIGUITY_MS), "unattributed" (no answered save carries the
 * version, or the request was sent within the window), or "none" (no version).
 */
export function waitOrigin(w: SaveWait, ops: readonly AccountedOp[]): "autosave" | "late-autosave" | "forced" | "unattributed" | "none" {
  if (w.version === null) return "none"
  if (!w.pressed) return "autosave"
  const o = pressOutcome({ opsAtPress: w.opsAtPress ?? 0, pressedAt: w.pressedAt, version: w.version, completed: true }, ops)
  return o === "new-save" ? "forced" : o === "confirm-only" ? "late-autosave" : "unattributed"
}

/** A reconcile within this long after a press wait ended may still be the press's request. */
export const PRESS_GRACE_MS = 2_000
/** A press wait whose end was not recorded is taken to last this long (the save wait's 4 s + 60 s, with room). */
const OPEN_PRESS_MS = 75_000
const OPEN_VERIFY_MS = 15_000

export interface SaveAccounting {
  /** Save waits whose version was committed by a request the editor sent before any press (late-autosave included). */
  autosaves: number
  /** Of the autosaves: those acknowledged only after the harness had pressed Ctrl+S. */
  lateAutosaves: number
  forcedCtrlS: number
  /**
   * Committing save requests sent within PRESS_AMBIGUITY_MS after a forced press: an
   * autosave prepared before the press or the forced save, the trail cannot tell. Neither
   * an autosave nor a forced save.
   */
  ambiguousSaves: number
  ambiguousSaveOps: number[]
  /** Presses by outcome (PressOutcome). */
  forcedCtrlSOutcomes: Record<PressOutcome, number>
  forcedCtrlSEvents: ForcedCtrlS[]
  /** Reconcile requests sent outside every harness press window: the editor's own. */
  editorReconciles: number
  editorReconcileOps: number[]
  /** Reconcile requests sent during a press wait (press to its end plus PRESS_GRACE_MS): the network trail cannot tell press from timer. */
  reconcilesDuringPressWait: number
  reconcilesDuringPressWaitOps: number[]
  verifyPresses: number
  description: string
  note: string
}

export const SAVE_ACCOUNTING_DESCRIPTION =
  "autosaves: save waits whose version was committed by a save request sent before any forced press (lateAutosaves: of those, acknowledged after the press); " +
  "forcedCtrlS: Control+s pressed by the harness (no new version within 4 s, or the Word table cell not in the head after 4 s with status Neuložený koncept), by outcome new-save / confirm-only / no-version / unattributed / incomplete; " +
  "ambiguousSaves: committing save requests sent within 30 s after a forced press (an autosave prepared before the press cannot be told from the forced save), neither autosaves nor forced saves; " +
  "editorReconciles: reconcile requests sent outside every press window; reconcilesDuringPressWait: reconcile requests sent between a press (Ctrl+S or Ověřit výsledek) and 2 s after its wait, cause unknown; " +
  "verifyPresses: Ověřit výsledek presses. Accounting only, not a gate."

/** Counts how the saves of an hour row happened. Pure: the session records the waits, presses and requests. */
export function saveAccounting(input: {
  waits: readonly SaveWait[]
  forced: readonly ForcedCtrlS[]
  ops: readonly AccountedOp[]
  verifications: readonly VerifyPress[]
}): SaveAccounting {
  const { waits, forced, ops, verifications } = input
  const origins = waits.map((w) => waitOrigin(w, ops))
  const lateAutosaves = origins.filter((o) => o === "late-autosave").length
  const autosaves = origins.filter((o) => o === "autosave").length + lateAutosaves
  // Re-classified with the final trail: a reply that arrived after the wait ended counts.
  // An interrupted press (or one still running) stays "incomplete".
  const ambiguous = new Set<number>()
  for (const w of waits) {
    if (!w.pressed || w.version === null) continue
    const a = attributePress({ opsAtPress: w.opsAtPress ?? 0, pressedAt: w.pressedAt, version: w.version, completed: true }, ops)
    if (a.ambiguousOp !== undefined) ambiguous.add(a.ambiguousOp)
  }
  const events: ForcedCtrlS[] = forced.map((e) => {
    if (e.endedAt === null || e.outcome === "incomplete") return { ...e, outcome: "incomplete" }
    const a = attributePress({ opsAtPress: e.opsAtPress, pressedAt: e.at, version: e.version, completed: true }, ops)
    if (a.ambiguousOp !== undefined) ambiguous.add(a.ambiguousOp)
    return { ...e, outcome: a.outcome }
  })
  const ambiguousSaveOps = [...ambiguous].sort((x, y) => x - y)
  const forcedCtrlSOutcomes: Record<PressOutcome, number> = { "new-save": 0, "confirm-only": 0, "no-version": 0, unattributed: 0, incomplete: 0 }
  for (const e of events) forcedCtrlSOutcomes[e.outcome]++
  const windows = [
    ...forced.map((e) => [Date.parse(e.at), (e.endedAt ? Date.parse(e.endedAt) : Date.parse(e.at) + OPEN_PRESS_MS) + PRESS_GRACE_MS]),
    ...verifications.map((v) => [Date.parse(v.pressedAt), (v.waitEndedAt ? Date.parse(v.waitEndedAt) : Date.parse(v.pressedAt) + OPEN_VERIFY_MS) + PRESS_GRACE_MS]),
  ]
  const duringPress = (o: AccountedOp) => {
    const t = o.sentAt ? Date.parse(o.sentAt) : NaN
    // A request without a time cannot be placed outside the windows.
    return Number.isNaN(t) ? windows.length > 0 : windows.some(([a, b]) => t >= a && t <= b)
  }
  const reconciles = ops.filter((o) => o.kind === "reconcile")
  const reconcilesDuringPressWaitOps = reconciles.filter(duringPress).map((o) => o.n)
  const editorReconcileOps = reconciles.filter((o) => !duringPress(o)).map((o) => o.n)
  const counts = { autosaves, forcedCtrlS: events.length, editorReconciles: editorReconcileOps.length, verifyPresses: verifications.length }
  const detail = (Object.entries(forcedCtrlSOutcomes) as Array<[PressOutcome, number]>).filter(([, k]) => k > 0).map(([o, k]) => `${k} ${o}`)
  const note =
    `autosave ${counts.autosaves}×${lateAutosaves ? ` (${lateAutosaves} acknowledged after a forced press)` : ""}, ` +
    `forced Ctrl+S ${counts.forcedCtrlS}×${detail.length ? ` (${detail.join(", ")})` : ""}, ` +
    (ambiguousSaveOps.length ? `ambiguous save (sent within ${PRESS_AMBIGUITY_MS / 1000} s after a forced press) ${ambiguousSaveOps.length}×, ` : "") +
    `automatic reconcile ${counts.editorReconciles}×, ` +
    (reconcilesDuringPressWaitOps.length ? `reconcile during a press wait (cause unknown) ${reconcilesDuringPressWaitOps.length}×, ` : "") +
    `Ověřit výsledek ${counts.verifyPresses}×.`
  return {
    ...counts,
    lateAutosaves,
    ambiguousSaves: ambiguousSaveOps.length,
    ambiguousSaveOps,
    forcedCtrlSOutcomes,
    forcedCtrlSEvents: events,
    editorReconcileOps,
    reconcilesDuringPressWait: reconcilesDuringPressWaitOps.length,
    reconcilesDuringPressWaitOps,
    description: SAVE_ACCOUNTING_DESCRIPTION,
    note,
  }
}

// ---------------------------------------------------------------------------
// The Word table step and the markers already saved (RC-4 rerun, #4877).
//
// In the RC-4 rerun a retry click at a fixed point of the canvas missed the new
// table and put the caret inside the B line of the previous cycle; the cell text
// typed next split the saved marker ("…C009" + "HOURCELL…" + "B second line"),
// and the row reported nine lost saves that the harness itself had edited. The
// step now compares the server head before and after its own typing: a saved
// marker that the cell text split is the harness's input, not a lost save.

/** A saved marker the step's own typing changed, with the text around the place it was split. */
export interface MarkerDamage {
  marker: string
  /** Where the cell text sits inside the marker, or "" when the marker vanished without the cell text in it. */
  context: string
}

export interface StepAudit {
  /** Saved markers the server head already lacked before the step (not the step's doing). */
  missingBefore: string[]
  /** Saved markers split by the step's cell text: harness input, never a lost save. */
  splitByStep: MarkerDamage[]
  /** Saved markers held before the step and gone after it, without the cell text in them: possibly a lost save. */
  unexplained: MarkerDamage[]
}

const escapeRe = (s: string) => s.replace(/[.*+?^$()|[\]\\{}]/g, "\\$&")

/** `marker` with `inserted` typed somewhere inside it (spaces around allowed), and the text around it. */
export function splitBy(text: string, marker: string, inserted: string): string {
  for (let k = 1; k < marker.length; k++) {
    const re = new RegExp(escapeRe(marker.slice(0, k)) + "\\s*" + escapeRe(inserted) + "\\s*" + escapeRe(marker.slice(k)) + "(?![0-9A-Za-z])")
    const hit = re.exec(text)
    if (hit) return text.slice(Math.max(0, hit.index - 24), hit.index + hit[0].length + 24)
  }
  return ""
}

/**
 * Compares the saved markers (as whole tokens, holdsMarker) in the server head
 * before a step that types `inserted` and after it. Pure: the caller reads both heads.
 */
export function auditStep(input: { before: string; after: string; saved: string[]; inserted: string }): StepAudit {
  const { before, after, saved, inserted } = input
  const out: StepAudit = { missingBefore: [], splitByStep: [], unexplained: [] }
  for (const marker of saved) {
    if (!holdsMarker(before, marker)) out.missingBefore.push(marker)
    else if (!holdsMarker(after, marker)) {
      const context = splitBy(after, marker, inserted)
      ;(context ? out.splitByStep : out.unexplained).push({ marker, context })
    }
  }
  return out
}

/** One line for the failures list; "" when the step left every saved marker intact. */
export function describeAudit(a: StepAudit, attempt: string, versions: string): string {
  const parts: string[] = []
  if (a.splitByStep.length) {
    parts.push(
      `harness-input: ${attempt} split ${a.splitByStep.length} saved marker(s) (${versions}), not a lost save: ` +
        a.splitByStep.slice(0, 3).map((d) => `${d.marker} in ${JSON.stringify(d.context)}`).join("; "),
    )
  }
  if (a.unexplained.length) {
    parts.push(`${a.unexplained.length} saved marker(s) held before ${attempt} are missing after it without the cell text in them (${versions}), first ${a.unexplained.slice(0, 3).map((d) => d.marker).join(", ")}`)
  }
  return parts.join(" | ")
}

/** How many more times a failed table step may run: one more after a plain failure, none after it damaged saved text. */
export function tableRunsLeft(runs: number, damaged: boolean): number {
  return damaged ? 0 : Math.max(0, 2 - runs)
}

// ---------------------------------------------------------------------------
// The per-cycle evidence chain of the hour rows (result.json): compact, marker
// excerpts only, so a later reader can follow each save from the keystrokes to
// the stored head without the full documents.

/** The saved markers in a stored head: how many it holds as whole tokens, and the first missing ones. */
export interface MarkerReport {
  saved: number
  held: number
  missing: string[]
  /** The table step's cell text: inside a table cell, elsewhere in the document, absent, or not typed yet. */
  cell?: "in-table" | "elsewhere" | "absent"
}

export function markerReport(text: string, saved: string[], cell?: string, limit = 10): MarkerReport {
  const missing = saved.filter((m) => !holdsMarker(text, m))
  const out: MarkerReport = { saved: saved.length, held: saved.length - missing.length, missing: missing.slice(0, limit) }
  if (cell) out.cell = cellInTable(text, cell) ? "in-table" : holdsMarker(text, cell) ? "elsewhere" : "absent"
  return out
}

/** A save/reconcile request as instrument() records it; its reply fields fill in when the answer arrives. */
export interface SentOp {
  n: number
  status?: number
  failed?: string
}

/**
 * The requests sent since index `from`, as the same objects (not copies): a reply that
 * arrives after the cycle ended still shows in the chain when result.json is written.
 */
export function sentSince<T extends SentOp>(ops: T[], from: number): T[] {
  return ops.slice(from)
}

/** The numbers of the requests that had neither a reply nor a failure yet. */
export function pendingReplies(ops: SentOp[]): number[] {
  return ops.filter((o) => o.status === undefined && o.failed === undefined).map((o) => o.n)
}

// ---------------------------------------------------------------------------
// "Výsledek nepotvrzen" in the hour rows and the lost-reply rows (RC f3035b06c).
//
// A renderer stalled past the editor's 10 s save deadline turns a committed save into
// "Výsledek nepotvrzen", and the editor sends nothing until somebody asks. A person
// presses "Ověřit výsledek"; the hour session does too, a little after that deadline,
// and records it. The press proves nothing about the typing: confirmCycle stays the judge.

/** Past the editor's own 10 s save deadline, so a reply that is merely late can still land. */
export const VERIFY_AFTER_MS = 15_000

export const isUnconfirmedStatus = (status: string): boolean => /^Výsledek nepotvrzen/.test(status)

/** Has the status said "Výsledek nepotvrzen" for longer than the deadline, counted from `since`? */
export function verifyDue(status: string, since: number, now: number, after = VERIFY_AFTER_MS): boolean {
  return isUnconfirmedStatus(status) && now - since >= after
}

/** One press of "Ověřit výsledek", for the row's evidence. */
export interface VerifyPress {
  cycle: number
  firstSeenAt: string
  pressedAt: string
  unconfirmedForMs: number
  statusAfter: string
  reconcile: { status?: number; version?: string; failed?: string } | null
  /** When the session stopped waiting for the press's effect (the press window of the save accounting). */
  waitEndedAt?: string
}

/**
 * What a PageStall is, for the evidence and the note. The name `pageStalls` is kept for
 * the readers of the evidence; it does not prove that the page froze.
 */
export const PAGE_STALLS_DESCRIPTION =
  "gaps in the page's own polling (craft/proposals requests of the editor page); not a measured renderer or input stall"

/** A gap of at least `minGapMs` between two of the page's own craft/proposals polls (see PAGE_STALLS_DESCRIPTION). */
export interface PageStall {
  from: string
  to: string
  gapMs: number
}

export function pageStalls(trail: ReadonlyArray<{ at: string; who: string; path: string }>, minGapMs = 10_000): PageStall[] {
  const polls = trail.filter((c) => c.who === "person" && c.path === "craft/proposals")
  const stalls: PageStall[] = []
  for (let i = 1; i < polls.length; i++) {
    const gapMs = Date.parse(polls[i].at) - Date.parse(polls[i - 1].at)
    if (gapMs >= minGapMs) stalls.push({ from: polls[i - 1].at, to: polls[i].at, gapMs })
  }
  return stalls
}

/**
 * The version of the last save the server answered. A repeat of the same bytes creates no
 * version (#5075), so an `unchanged` answer names the version the editor already holds.
 * The contiguity of the first save is a separate assertion.
 */
export function lastCommittedSave(ops: ReadonlyArray<{ kind: string; status?: number; version?: string }>): string | undefined {
  return ops.filter((o) => o.kind === "save" && o.status === 200 && o.version !== undefined).at(-1)?.version
}
