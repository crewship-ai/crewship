/**
 * The freshness contract (PRD §4) and the em-dash rule (§9b.4), as functions.
 *
 * | state           | rendering                                        |
 * |-----------------|--------------------------------------------------|
 * | `fresh`         | the value, full contrast                         |
 * | `stale`         | the value dimmed, with an ABSOLUTE age beside it |
 * | `failed`        | `—` plus the failure, in the destructive tone    |
 * | never produced  | `—` plus the empty-state sentence                |
 *
 * A measured `0` is a `0`. Only "no basis to compute" is a `—`. There is no
 * fourth glyph — the product already has one and Pages inherits it verbatim.
 */
import type { PanelSnapshot, PanelSpec, PanelState } from "./types"

/** U+2014. The one and only no-data glyph. */
export const EM_DASH = "—"

/**
 * Whether the value on screen stands for something that was measured, or for
 * nothing at all. Rendered as `data-basis` so the distinction is assertable
 * and survives a restyle.
 */
export type ValueBasis = "measured" | "none"

/** What the body of the panel is allowed to draw. */
export type PanelGate =
  | { kind: "render"; dimmed: boolean }
  | { kind: "failed" }
  | { kind: "never" }

export function panelGate(data: Pick<PanelSnapshot, "state" | "payload">): PanelGate {
  if (data.state === "failed") return { kind: "failed" }
  if (data.state === "never_produced") return { kind: "never" }
  if (data.payload === null || data.payload === undefined) return { kind: "never" }
  return { kind: "render", dimmed: data.state === "stale" }
}

/**
 * The right-aligned muted word in the card header (§9b.2). Always the answer
 * to "is this current?", never a repeat of the label on the left.
 */
export function panelStateWord(state: PanelState): string {
  switch (state) {
    case "fresh":
      return "current"
    case "stale":
      return "stale"
    case "failed":
      return "failed"
    case "never_produced":
      return "no data yet"
  }
}

export function toDate(value: string | Date | null | undefined): Date | null {
  if (value === null || value === undefined) return null
  const d = value instanceof Date ? value : new Date(value)
  return Number.isFinite(d.getTime()) ? d : null
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n)
}

/**
 * An exact instant: `12 Aug 12:40`, plus the year when it is not the current
 * one. Hand-formatted rather than `toLocaleString`, so the same input renders
 * the same string on a server, in a browser and in a test runner.
 */
export function formatInstant(at: Date, now?: Date): string {
  const stamp = `${at.getDate()} ${MONTHS[at.getMonth()]} ${pad2(at.getHours())}:${pad2(at.getMinutes())}`
  if (now && at.getFullYear() !== now.getFullYear()) {
    return `${at.getDate()} ${MONTHS[at.getMonth()]} ${at.getFullYear()} ${pad2(at.getHours())}:${pad2(at.getMinutes())}`
  }
  return stamp
}

/**
 * An exact elapsed amount — `2 h 15 min`, `3 d 4 h`, `45 s`. Two units at
 * most, never rounded up to a vague one. "a while ago" is the phrasing §4
 * bans; this function has no vocabulary for it.
 */
export function formatExactDuration(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const d = Math.floor(total / 86400)
  const h = Math.floor((total % 86400) / 3600)
  const min = Math.floor((total % 3600) / 60)
  const s = total % 60
  if (d > 0) return h > 0 ? `${d} d ${h} h` : `${d} d`
  if (h > 0) return min > 0 ? `${h} h ${min} min` : `${h} h`
  if (min > 0) return `${min} min`
  return `${s} s`
}

/**
 * The age line that sits next to a dimmed value: how long ago, exactly, and
 * exactly when. Returns null when there is no timestamp to be exact about —
 * an age we cannot compute is not written as prose.
 */
export function formatAbsoluteAge(
  producedAt: string | Date | null | undefined,
  now: Date,
): string | null {
  const at = toDate(producedAt)
  if (!at) return null
  return `${formatExactDuration(now.getTime() - at.getTime())} old · ${formatInstant(at, now)}`
}

/**
 * Empty states are instructions (§9b.3): name the next action, do not just
 * report the absence.
 */
export function defaultEmptyHint(panel: Pick<PanelSpec, "id">): string {
  return `Nothing has been pushed here yet. Run the producer, or send a first payload with crewship page set <page>/${panel.id} --data -`
}
