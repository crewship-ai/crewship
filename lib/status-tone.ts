/**
 * The one translation from a feature's tone word to a StatusPill tone.
 *
 * StatusPill (components/ui/status-pill.tsx) has six tones, one per chip
 * token pair in app/globals.css (--chip-<name>-bg / -fg). Feature code grew
 * its own words for the same six meanings — the detail kit's "destructive" and
 * "default", backups' "ok/bad/none", severities "high/medium/low", and palette
 * names such as "emerald" or "amber" from the old badge maps. Each of those
 * read the same meaning slightly differently; mapping them here once is what
 * makes a failure red in every screen and a pause amber in every screen.
 *
 * For a raw status enum (IN_PROGRESS, failed …) use `statusTone`, which reads
 * lib/format-status — the word and the tone always come from the same row.
 */
import { formatStatus, type StatusTone } from "@/lib/format-status"

export type { StatusTone }

export const STATUS_TONES: readonly StatusTone[] = ["success", "blue", "warn", "danger", "muted", "purple"]

/** The chip token pair each tone renders with (`bg-chip-<name>-bg`). */
export type ChipTone = "ok" | "info" | "warn" | "danger" | "neutral" | "violet"

export const CHIP_TONE: Record<StatusTone, ChipTone> = {
  success: "ok",
  blue: "info",
  warn: "warn",
  danger: "danger",
  muted: "neutral",
  purple: "violet",
}

const ALIASES: Record<string, StatusTone> = {
  // the six tones themselves
  success: "success",
  blue: "blue",
  warn: "warn",
  danger: "danger",
  muted: "muted",
  purple: "purple",
  // chip token names
  ok: "success",
  info: "blue",
  neutral: "muted",
  violet: "purple",
  // components/ui/detail DetailTone
  default: "muted",
  destructive: "danger",
  // ok / bad words (backups, hooks, security summaries)
  bad: "danger",
  none: "muted",
  error: "danger",
  warning: "warn",
  // severities
  critical: "danger",
  high: "danger",
  medium: "warn",
  low: "muted",
  // palette names the old badge maps were keyed by
  emerald: "success",
  green: "success",
  amber: "warn",
  orange: "warn",
  yellow: "warn",
  red: "danger",
  slate: "muted",
  gray: "muted",
  primary: "blue",
}

/** Any tone word a feature uses → one of the six StatusPill tones. */
export function toStatusTone(word: string | null | undefined, fallback: StatusTone = "muted"): StatusTone {
  if (!word) return fallback
  return ALIASES[word.trim().toLowerCase()] ?? fallback
}

/** The tone of a raw status enum, from the same table that gives its word. */
export function statusTone(status: string | null | undefined): StatusTone {
  return formatStatus(status).tone
}
