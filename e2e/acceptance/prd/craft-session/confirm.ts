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
// The unit is a write the server confirmed, not a save wait (#5112): a version
// answered by a save (a repeat of the same attempt answers the same version; an
// `unchanged` answer creates none, #5075) or proven by a reconcile after a lost
// reply. Each version is counted once, whichever wait saw it, or none.
//
// A write's origin is the save request that sent it, by its place in the ops trail
// (instrument() appends each request when it is sent). A press of Ctrl+S is not the
// origin: a request sent before the press is the editor's own. A request sent after
// it carries no trigger, and the editor queues captures one after another
// (lib/mooring/single-editor-save.ts, `preparations`), so an autosave queued behind
// a slow capture can leave any time later, exactly like the forced save. No time
// window proves which one it was. A write sent after a harness Ctrl+S, "Ověřit
// výsledek" (a reconcile that proves an attempt absent lets the editor send a fresh
// save) or release flush is therefore "unknown" until that editor instance ended (its page closed):
// a closed page sends nothing more. The harness cannot tell a forced save from an
// autosave; it only counts the presses, exactly.

/** A save/reconcile request as instrument() records it. */
export interface AccountedOp {
  n: number
  kind: string
  sentAt?: string
  status?: number
  version?: string
  baseVersion?: string
  contentHash?: string
  /** The snapshot identity the request carried (request body), bound by the server's receipt. */
  editorRevision?: string
  sessionId?: string
  attemptId?: string
  /** The server's #5075 answer: no version was created. */
  unchanged?: boolean
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
 * What a forced press led to: "confirm-only" (the version that followed was sent
 * before the press: an autosave the press only confirmed), "no-version" (no new
 * version followed), "unattributed" (a version followed, sent after the press or by
 * no request in the trail: the press or a queued autosave, the trail cannot tell),
 * "incomplete" (the wait was interrupted after the press). No outcome claims that
 * the press caused a save (#5112).
 */
export type PressOutcome = "confirm-only" | "no-version" | "unattributed" | "incomplete"

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

/** A harness action at `at`, with the ops trail length right before it (a release flush, a closed page). */
export interface TrailMark {
  at: string
  opsAt: number
}

/** One write the server confirmed, with the save request(s) that may have sent it. */
export interface CommittedWrite {
  version: string
  /** The first save request of that snapshot in the trail (undefined: none recorded). */
  op?: number
  /** Every save request of that snapshot (attempt repeats of the same base and hash). */
  ops: number[]
  /** The request whose answer confirmed the version (a save, or a reconcile after a lost reply). */
  confirmedBy: number
  confirmedByKind: "save" | "reconcile"
}

export interface AccountedWrite extends CommittedWrite {
  origin: "autosave" | "unknown"
  /** Why the origin is unknown. */
  reason?: string
}

const answered = (o: AccountedOp) => o.status === 200 && o.version !== undefined
/** Did this answer confirm a new version (not an `unchanged` answer of the base, #5075)? */
function confirmsWrite(o: AccountedOp): boolean {
  if (!answered(o)) return false
  if (o.kind === "reconcile") return true
  return o.kind === "save" && o.unchanged !== true && o.version !== o.baseVersion
}

const defined = (...v: Array<string | undefined>) => v.every((x) => x !== undefined)
/** Do two requests carry the same snapshot of the same attempt (what a reconcile proves, internal/mooring/native_save_receipt.go)? */
function sameSnapshot(a: AccountedOp, b: AccountedOp): boolean {
  if (!defined(a.baseVersion, a.contentHash, a.editorRevision, b.baseVersion, b.contentHash, b.editorRevision)) return false
  return a.baseVersion === b.baseVersion && a.contentHash === b.contentHash && a.editorRevision === b.editorRevision && a.sessionId === b.sessionId && a.attemptId === b.attemptId
}

/** The writes the server confirmed, one per version, in the order of their first confirmation. */
export function committedWrites(ops: readonly AccountedOp[]): CommittedWrite[] {
  const seen = new Set<string>()
  const out: CommittedWrite[] = []
  for (const c of ops) {
    if (!confirmsWrite(c) || seen.has(c.version!)) continue
    seen.add(c.version!)
    // The same snapshot sent again (a repeat of the attempt), before or after this answer,
    // may be the one that committed: replies carry no commit time. Matched on the identity
    // the server binds (base, revision, hash, session, attempt); a request that lacks any
    // of it matches nothing, so its write stays unknown.
    const same = (o: AccountedOp) => o.kind === "save" && sameSnapshot(o, c)
    const senders = ops.filter((o) => o === c || same(o)).filter((o) => o.kind === "save").map((o) => o.n)
    out.push({ version: c.version!, op: senders[0], ops: senders, confirmedBy: c.n, confirmedByKind: c.kind === "reconcile" ? "reconcile" : "save" })
  }
  return out
}

/** The save request that committed `version` (its first request in the trail), when the trail has one. */
export function producerOf(ops: readonly AccountedOp[], version: string): AccountedOp | undefined {
  const w = committedWrites(ops).find((x) => x.version === version)
  return w?.op === undefined ? undefined : ops.find((o) => o.n === w.op)
}

/** The harness actions after which a request may be the harness's: presses, "Ověřit výsledek", releases, and the closed pages that end them. */
export interface Interventions {
  presses: readonly ForcedCtrlS[]
  /** "Ověřit výsledek": a reconcile that proves an attempt absent lets the editor send a fresh save (single-editor-save.ts, settle). */
  verifications?: readonly VerifyPress[]
  releases?: readonly TrailMark[]
  editorEnds?: readonly TrailMark[]
}

/**
 * Why request `o` cannot be called the editor's own, or undefined when it can: it was
 * sent after a harness Ctrl+S, "Ověřit výsledek" or release whose editor had not ended yet.
 */
export function openIntervention(o: Pick<AccountedOp, "n" | "sentAt">, p: Interventions): string | undefined {
  const sent = o.sentAt ? Date.parse(o.sentAt) : NaN
  // After the action: by the trail cut when it was recorded, else by time; a request without a time counts as after.
  const after = (m: { at: string; opsAt?: number }) => (m.opsAt !== undefined ? o.n > m.opsAt : Number.isNaN(sent) || sent >= Date.parse(m.at))
  const ended = (m: { at: string }) => (p.editorEnds ?? []).some((e) => Date.parse(e.at) >= Date.parse(m.at) && o.n > e.opsAt)
  for (const press of p.presses) {
    const m = { at: press.at, opsAt: press.opsAtPress }
    if (after(m) && !ended(m)) return `sent after a harness Ctrl+S (cycle ${press.cycle}, ${press.site}, ${press.at}) of the same editor`
  }
  for (const v of p.verifications ?? []) {
    const m = { at: v.pressedAt, opsAt: v.opsAtPress }
    if (after(m) && !ended(m)) return `sent after a harness Ověřit výsledek (cycle ${v.cycle}, ${v.pressedAt}) of the same editor`
  }
  for (const r of p.releases ?? []) {
    if (after(r) && !ended(r)) return `sent after a harness release (flush before transfer, ${r.at}) of the same editor`
  }
  return undefined
}

/** Every confirmed write with its origin: "autosave" only when no harness action could have sent it. */
export function accountWrites(ops: readonly AccountedOp[], p: Interventions): AccountedWrite[] {
  return committedWrites(ops).map((w) => {
    if (w.op === undefined) return { ...w, origin: "unknown", reason: `confirmed by reconcile ${w.confirmedBy}, but no save request of that snapshot (base, revision, hash, session, attempt) is in the trail` }
    for (const n of w.ops) {
      const why = openIntervention(ops.find((o) => o.n === n) ?? { n }, p)
      if (why) return { ...w, origin: "unknown", reason: `save request ${n} ${why}` }
    }
    return { ...w, origin: "autosave" }
  })
}

/** Classifies a press by the request that sent the version that followed it. */
export function pressOutcome(press: { opsAtPress: number; version: string | null; completed: boolean }, ops: readonly AccountedOp[]): PressOutcome {
  if (!press.completed) return "incomplete"
  if (press.version === null) return "no-version"
  const w = committedWrites(ops).find((x) => x.version === press.version)
  if (!w || w.op === undefined) return "unattributed"
  return w.ops.every((n) => n <= press.opsAtPress) ? "confirm-only" : "unattributed"
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
  event.outcome = pressOutcome({ opsAtPress: event.opsAtPress, version: result.version, completed: result.completed }, ops)
}

/**
 * Records an "Ověřit výsledek" press at dispatch, before the click: an interrupted wait
 * still leaves the press (without `waitEndedAt`) in the evidence. The caller fills in the rest.
 */
export function startVerify(verifications: VerifyPress[], p: { cycle: number; firstSeenAt: string; opsAtPress: number; at?: string }): VerifyPress {
  const pressedAt = p.at ?? new Date().toISOString()
  const v: VerifyPress = { cycle: p.cycle, firstSeenAt: p.firstSeenAt, pressedAt, unconfirmedForMs: Date.parse(pressedAt) - Date.parse(p.firstSeenAt), statusAfter: "", reconcile: null, opsAtPress: p.opsAtPress }
  verifications.push(v)
  return v
}

/** Records a harness action (a release, a closed page) with the trail length right before it. */
export function markTrail(marks: TrailMark[], ops: readonly unknown[], at = new Date().toISOString()): void {
  marks.push({ at, opsAt: ops.length })
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

/** A reconcile within this long after a press wait ended may still be the press's request. */
export const PRESS_GRACE_MS = 2_000
/** A press wait whose end was not recorded is taken to last this long (the save wait's 4 s + 60 s, with room). */
const OPEN_PRESS_MS = 75_000
const OPEN_VERIFY_MS = 15_000

export interface SaveAccounting {
  /** Writes the server confirmed, each version once: autosaves + unknownOrigin. */
  committedWrites: number
  /** Confirmed writes sent while no harness Ctrl+S, Ověřit výsledek or release of the same editor could have caused them. */
  autosaves: number
  /** Confirmed writes whose origin the trail cannot prove: a forced save and a queued autosave look the same. */
  unknownOrigin: number
  unknownOriginOps: number[]
  /** Every confirmed write: version, request(s), confirming answer, origin and why it is unknown. */
  writes: AccountedWrite[]
  /** Measured: Control+s presses of the harness. */
  forcedCtrlS: number
  /** Presses by outcome (PressOutcome). */
  forcedCtrlSOutcomes: Record<PressOutcome, number>
  forcedCtrlSEvents: ForcedCtrlS[]
  /** Measured: releases of the editor by the harness (each flushes before the transfer). */
  releases: number
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
  "Measured: forcedCtrlS, releases and verifyPresses. committedWrites: versions the server confirmed (a save answer that created a version, or a reconcile that proved one), each once; committedWrites = autosaves + unknownOrigin. " +
  "autosaves: writes sent before every harness Ctrl+S, Ověřit výsledek and release of the same editor (or after its page closed), so no harness action caused them; " +
  "unknownOrigin: writes sent after a harness Ctrl+S, Ověřit výsledek or release of the still-open editor, or confirmed without a save request in the trail. The request carries no trigger and an autosave can queue behind a slow capture, so the harness cannot tell a forced save from an autosave (#5112); a forced save is among these, never counted apart; " +
  "forcedCtrlS: Control+s pressed by the harness (no new version within 4 s, or the Word table cell not in the head after 4 s with status Neuložený koncept), by outcome confirm-only / no-version / unattributed / incomplete; " +
  "editorReconciles: reconcile requests sent outside every press window; reconcilesDuringPressWait: reconcile requests sent between a press (Ctrl+S or Ověřit výsledek) and 2 s after its wait, cause unknown; " +
  "verifyPresses: Ověřit výsledek presses. Accounting only, not a gate."

/** Counts how the saves of an hour row happened. Pure: the session records the waits, presses, releases, closed pages and requests. */
export function saveAccounting(input: {
  waits: readonly SaveWait[]
  forced: readonly ForcedCtrlS[]
  ops: readonly AccountedOp[]
  verifications: readonly VerifyPress[]
  releases?: readonly TrailMark[]
  editorEnds?: readonly TrailMark[]
}): SaveAccounting {
  const { forced, ops, verifications } = input
  const releases = input.releases ?? []
  const writes = accountWrites(ops, { presses: forced, verifications, releases, editorEnds: input.editorEnds })
  const autosaves = writes.filter((w) => w.origin === "autosave").length
  const unknown = writes.filter((w) => w.origin === "unknown")
  // Re-classified with the final trail: a reply that arrived after the wait ended counts.
  // An interrupted press (or one still running) stays "incomplete".
  const events: ForcedCtrlS[] = forced.map((e) => {
    if (e.endedAt === null || e.outcome === "incomplete") return { ...e, outcome: "incomplete" }
    return { ...e, outcome: pressOutcome({ opsAtPress: e.opsAtPress, version: e.version, completed: true }, ops) }
  })
  const forcedCtrlSOutcomes: Record<PressOutcome, number> = { "confirm-only": 0, "no-version": 0, unattributed: 0, incomplete: 0 }
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
  const detail = (Object.entries(forcedCtrlSOutcomes) as Array<[PressOutcome, number]>).filter(([, k]) => k > 0).map(([o, k]) => `${k} ${o}`)
  const note =
    `forced Ctrl+S ${events.length}×, Ověřit výsledek ${verifications.length}×${releases.length ? `, release ${releases.length}×` : ""}; ` +
    `server-confirmed writes ${writes.length}× = autosave ${autosaves}× + origin unknown ${unknown.length}×` +
    (unknown.length ? ` (sent after a harness Ctrl+S, Ověřit výsledek or release, or without a save request in the trail: the harness cannot tell a forced save from an autosave, #5112)` : "") +
    `, ` +
    (detail.length ? `press outcomes ${detail.join(", ")}, ` : "") +
    (reconcilesDuringPressWaitOps.length ? `reconcile during a press wait (cause unknown) ${reconcilesDuringPressWaitOps.length}×, ` : "") +
    `automatic reconcile ${editorReconcileOps.length}×.`
  return {
    committedWrites: writes.length,
    autosaves,
    unknownOrigin: unknown.length,
    unknownOriginOps: unknown.map((w) => w.op ?? w.confirmedBy),
    writes,
    forcedCtrlS: events.length,
    forcedCtrlSOutcomes,
    forcedCtrlSEvents: events,
    releases: releases.length,
    editorReconciles: editorReconcileOps.length,
    editorReconcileOps,
    reconcilesDuringPressWait: reconcilesDuringPressWaitOps.length,
    reconcilesDuringPressWaitOps,
    verifyPresses: verifications.length,
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
  /** The ops trail length right before the press; without it, requests are placed by time. */
  opsAtPress?: number
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
