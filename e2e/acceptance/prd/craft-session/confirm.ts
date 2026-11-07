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
 * the page stalls and the unconfirmed spells up to each press, merged so that an
 * overlap is not counted twice. The longest one is the longest stall.
 */
export function hourInterruptions(verifications: readonly VerifyPress[], stalls: readonly PageStall[]): { presses: number; interruptedMs: number; longestStallMs: number; note: string } {
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
  const note = `Ověřit výsledek pressed ${presses} times, interrupted ${seconds(interruptedMs)} in total, longest stall ${seconds(longestStallMs)}.` +
    (presses ? " Saving did not recover by itself." : "")
  return { presses, interruptedMs, longestStallMs, note }
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
}

/** A gap in the page's own proposal polling: the renderer did not run its timers. */
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
