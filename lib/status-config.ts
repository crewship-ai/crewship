/**
 * Status entries for features that pair a status with a glyph.
 *
 * Each entry names a StatusPill tone (lib/status-tone). The palette class maps
 * that used to live here (STATUS_STYLES, STATUS_STYLES_SUBTLE: bg-emerald-100
 * and friends) were replaced by the chip tokens StatusPill renders with, so
 * every status reads the same in every screen and theme.
 */

import { CheckCircle2, Loader2, XCircle } from "lucide-react"

import type { StatusTone } from "@/lib/status-tone"

/** A status entry: the word shown and the StatusPill tone. */
export interface StatusConfigEntry {
  label: string
  tone: StatusTone
}

/** Status entry extended with a React icon component. */
export interface StatusConfigEntryWithIcon extends StatusConfigEntry {
  icon: React.ComponentType<{ className?: string }>
}

// ── Shared async-run status entries ─────────────────────────────────────────
/**
 * Completed / Running / Failed status entries shared by features that track an
 * async run (crew assignments, crew peer conversations).
 */
export const RUN_STATUS_CONFIG: Record<"COMPLETED" | "RUNNING" | "FAILED", StatusConfigEntryWithIcon> = {
  COMPLETED: { label: "Completed", tone: "success", icon: CheckCircle2 },
  RUNNING:   { label: "Running",   tone: "blue",    icon: Loader2 },
  FAILED:    { label: "Failed",    tone: "danger",  icon: XCircle },
}
