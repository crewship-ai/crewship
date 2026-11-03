// Per-line ledger of a typeLines() stream (craft-session/draft.ts) and the
// check that no line the editor took is lost, doubled or reordered in a copy
// of the document: the local draft the editor keeps, or the head the server
// confirms (read through the product API). Pure: no Playwright, no relative
// imports, so `node --test` loads it directly (lines.test.mjs).
//
// The states of a line, in the order the work moves through them:
//   typed     the harness sent `${prefix}-${i}` and Enter;
//   accepted  the editor's status was editable before and after the line;
//   captured  the line was found in a copy (draft in the open editor, the
//             draft offered after a reload, or the server head).
// A line that is accepted, or captured in an earlier copy, must be in every
// later copy exactly once and in typing order. A line typed while the editor
// turned read-only is unproven by status alone; it is still required as soon
// as any copy proves it captured, or when a later line is in the copy (the
// editor took input after it).

export interface TypingProgress {
  typed: number
  accepted: number[]
  stoppedBy?: string
}

export interface LineSighting {
  stage: string
  /** How many times the line is in that copy. */
  count: number
}

export interface LineRecord {
  index: number
  marker: string
  /** "accepted": the status was editable before and after the line; "unproven": it was not shown to be. */
  typing: "accepted" | "unproven"
  /** Every check of a copy that must hold the lines, oldest first. */
  seen: LineSighting[]
}

export interface LineLedger {
  prefix: string
  typed: number
  stoppedBy: string | null
  lines: LineRecord[]
}

export interface LineCheck {
  stage: string
  /** How the copy was read: native Word paragraphs, native Sheet cells, or plain text. */
  kind: "word" | "sheet" | "text"
  /** Line indices of the ledger's prefix in document order, as found. */
  found: number[]
  problems: string[]
}

/** The ledger of a finished (or interrupted) typeLines() stream. */
export function lineLedger(prefix: string, progress: TypingProgress): LineLedger {
  const accepted = new Set(progress.accepted)
  const lines: LineRecord[] = []
  for (let i = 0; i < progress.typed; i++) lines.push({ index: i, marker: `${prefix}-${i}`, typing: accepted.has(i) ? "accepted" : "unproven", seen: [] })
  return { prefix, typed: progress.typed, stoppedBy: progress.stoppedBy ?? null, lines }
}

type Node = Record<string, unknown>

function* walk(value: unknown): Generator<Node> {
  if (Array.isArray(value)) for (const v of value) yield* walk(v)
  else if (value && typeof value === "object") {
    yield value as Node
    for (const v of Object.values(value)) yield* walk(v)
  }
}

const byNumber = (a: [string, unknown], b: [string, unknown]) => Number(a[0]) - Number(b[0])

/**
 * The text units of a copy in document order: the paragraphs of a native Word
 * document, the cells of a native Sheet workbook (sheet, row, column), or the
 * whole text when it is not native JSON.
 */
export function documentUnits(raw: string): { kind: LineCheck["kind"]; units: string[] } {
  let doc: unknown
  try {
    doc = JSON.parse(raw)
  } catch {
    return { kind: "text", units: [raw] }
  }
  const d = doc as { body?: unknown; sheets?: unknown }
  if (Array.isArray(d?.body)) {
    const units = [...walk(d.body)].filter((n) => n.kind === "para").map((n) => String(n.text ?? ""))
    return { kind: "word", units }
  }
  if (Array.isArray(d?.sheets)) {
    const units: string[] = []
    for (const sheet of d.sheets as Array<{ cells?: { bands?: Record<string, { rows?: Record<string, Record<string, { value?: { v?: unknown } }>> }> } }>) {
      const cells: Array<{ row: number; col: number; text: string }> = []
      for (const [, band] of Object.entries(sheet?.cells?.bands ?? {}).sort(byNumber)) {
        for (const [row, cols] of Object.entries(band?.rows ?? {})) {
          for (const [col, cell] of Object.entries(cols ?? {})) {
            const v = cell?.value?.v
            cells.push({ row: Number(row), col: Number(col), text: typeof v === "string" ? v : JSON.stringify(cell ?? null) })
          }
        }
      }
      cells.sort((a, b) => a.row - b.row || a.col - b.col)
      units.push(...cells.map((c) => c.text))
    }
    return { kind: "sheet", units }
  }
  return { kind: "text", units: [raw] }
}

const escape = (s: string) => s.replace(/[.*+?^$()|[\]\\{}]/g, "\\$&")

/** The ledger's line indices in `raw`, in document order (`X-1` is never read from `X-10`). */
export function foundLines(prefix: string, raw: string): { kind: LineCheck["kind"]; found: number[] } {
  const { kind, units } = documentUnits(raw)
  const re = new RegExp(escape(prefix) + "-(\\d+)(?!\\d)", "g")
  const found: number[] = []
  for (const unit of units) for (const m of unit.matchAll(re)) found.push(Number(m[1]))
  return { kind, found }
}

function describe(line: LineRecord): string {
  const typing = line.typing === "accepted" ? "accepted by the editor (editable before and after the line)" : "typed, not proven accepted by status"
  const last = [...line.seen].reverse().find((s) => s.count > 0)
  return `${typing}; ${last ? `last seen in "${last.stage}" (x${last.count})` : "never seen in a copy"}`
}

/**
 * Checks a copy that must hold the work: every accepted line, every line seen
 * in an earlier copy and every line before the last one found is there exactly
 * once, in typing order, and nothing that was never typed is there. Does not
 * record the sighting (see recordLines / checkLines).
 */
export function evaluateLines(ledger: LineLedger, raw: string, stage: string): LineCheck {
  const { kind, found } = foundLines(ledger.prefix, raw)
  const problems: string[] = []
  // The editor can stop taking input in the middle of the last line: "X-1" left
  // of "X-12" is that cut line, not a second X-1. Only the very last occurrence,
  // and only when the stream stopped, is read that way.
  const cut = found.at(-1)
  if (cut !== undefined && ledger.stoppedBy !== null) {
    const last = [ledger.typed - 1, ledger.typed].map(String)
    if (last.some((l) => l !== String(cut) && l.startsWith(String(cut))) && found.slice(0, -1).includes(cut)) found.pop()
  }
  const counts = new Map<number, number>()
  for (const i of found) counts.set(i, (counts.get(i) ?? 0) + 1)
  for (const i of [...counts.keys()].sort((a, b) => a - b)) {
    // Index `typed` is the line in flight when the stream ended; it may be there once.
    if (i > ledger.typed) problems.push(`"${stage}" holds ${ledger.prefix}-${i}, which was never typed (${ledger.typed} lines typed)`)
    else if (counts.get(i)! > 1) problems.push(`"${stage}" holds ${ledger.prefix}-${i} ${counts.get(i)} times`)
  }
  for (let k = 1; k < found.length; k++) {
    if (found[k] <= found[k - 1]) {
      problems.push(`"${stage}" holds the lines out of order: ${found.map((i) => `-${i}`).join(" ")}`)
      break
    }
  }
  const lastFound = found.length ? Math.max(...found.filter((i) => i <= ledger.typed), -1) : -1
  for (const line of ledger.lines) {
    if (counts.has(line.index)) continue
    const why =
      line.typing === "accepted"
        ? "the editor took it"
        : line.seen.some((s) => s.count > 0)
          ? "an earlier copy held it"
          : line.index < lastFound
            ? `the editor kept the later line ${ledger.prefix}-${lastFound}`
            : null
    if (why) problems.push(`lost ${line.marker} in "${stage}" (${why}): ${describe(line)}`)
  }
  return { stage, kind, found, problems }
}

/** Records in the ledger which lines a checked copy holds. */
export function recordLines(ledger: LineLedger, check: LineCheck): void {
  for (const line of ledger.lines) line.seen.push({ stage: check.stage, count: check.found.filter((i) => i === line.index).length })
}

/** evaluateLines + recordLines. */
export function checkLines(ledger: LineLedger, raw: string, stage: string): LineCheck {
  const check = evaluateLines(ledger, raw, stage)
  recordLines(ledger, check)
  return check
}

/**
 * The offered drafts: one of them must hold the work (see evaluateLines). The
 * first draft without a problem is recorded; when none passes, the problems of
 * every draft are returned and the one with the fewest is recorded.
 */
export function checkDraftCopies(ledger: LineLedger, drafts: string[], stage: string): LineCheck {
  if (drafts.length === 0) return { stage, kind: "text", found: [], problems: [`"${stage}": no draft was offered`] }
  const checks = drafts.map((text, i) => evaluateLines(ledger, text, drafts.length > 1 ? `${stage}, draft ${i + 1}` : stage))
  const pass = checks.find((c) => c.problems.length === 0)
  const best = pass ?? [...checks].sort((a, b) => a.problems.length - b.problems.length)[0]
  recordLines(ledger, best)
  return pass ?? { ...best, problems: checks.flatMap((c) => c.problems) }
}

/** A copy that must hold none of the lines (nothing was saved): every line found is a problem. */
export function checkNoLines(ledger: LineLedger, raw: string, stage: string): LineCheck {
  const { kind, found } = foundLines(ledger.prefix, raw)
  const problems = found.length ? [`"${stage}" holds ${found.length} typed line(s) although nothing may be saved: ${found.map((i) => `${ledger.prefix}-${i}`).join(", ")}`] : []
  return { stage, kind, found, problems }
}

/** Does the copy hold any line of the ledger. */
export const holdsAnyLine = (ledger: LineLedger, raw: string): boolean => foundLines(ledger.prefix, raw).found.length > 0
