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
