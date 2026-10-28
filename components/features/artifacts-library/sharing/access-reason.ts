import type { StatusTone } from "@/lib/format-status"
import { levelRank, SHARE_LEVELS, type AccessEntry, type ShareLevel } from "./sharing-api"

export const LEVEL_TONE: Record<ShareLevel, StatusTone> = { write: "blue", comment: "purple", read: "muted" }
/** Edit first, as the dialog and the Access tab list them. */
export const LEVELS_DESC: ShareLevel[] = [...SHARE_LEVELS].reverse()

export function accessVia(entry: AccessEntry): string {
  switch (entry.via) {
    case "owner": return "Owner"
    case "grant": return "Shared directly"
    case "folder": return `Via folder ${entry.folderName ?? entry.folderSlug ?? ""}`.trim()
    case "role": return "Workspace role"
  }
}

/** Why a row cannot be changed here; null for a direct grant. */
export function lockedReason(entry: AccessEntry): string | null {
  switch (entry.via) {
    case "owner": return "The owner always has Edit"
    case "folder": return "Change in folder settings"
    case "role": return "Set by their workspace role"
    case "grant": return null
  }
}

export const byAccess = (a: AccessEntry, b: AccessEntry) =>
  Number(b.via === "owner") - Number(a.via === "owner") || levelRank(b.level) - levelRank(a.level) || a.label.localeCompare(b.label)
