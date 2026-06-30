/**
 * Routine lifecycle governance helpers (pure, UI-agnostic).
 *
 * Routines carry a lifecycle `status`:
 *   - "active"   — normal; runnable.
 *   - "proposed" — agent-authored / risky; awaiting MANAGER+ approval.
 *                  Cannot be run until promoted.
 *   - "disabled" — killed by OWNER/ADMIN; cannot be run until re-enabled.
 *
 * These helpers translate that status (and the viewer's role) into the
 * badge descriptors, control-visibility flags, and run-guard reasons the
 * routine UI renders. Kept pure so they're unit-testable without React.
 */

import { STATUS_BADGE_CLASSES, STATUS_DOT_CLASSES } from "@/lib/colors"

export type RoutineStatus = "active" | "proposed" | "disabled"

/** Normalise an unknown wire value to a RoutineStatus. Absent / unknown
 *  values are treated as "active" — a routine the backend hasn't tagged
 *  yet should remain fully usable, not silently locked out. */
export function normalizeRoutineStatus(status: string | null | undefined): RoutineStatus {
  return status === "proposed" || status === "disabled" ? status : "active"
}

export interface RoutineStatusBadge {
  label: string
  /** Combined Tailwind bg+text classes pulled straight from lib/colors
   *  STATUS_BADGE_CLASSES so the pill matches the status pills rendered
   *  elsewhere (Inbox / Issues / Activity). */
  className: string
  /** Solid dot fill from lib/colors STATUS_DOT_CLASSES. */
  dot: string
}

/** Returns the badge descriptor for a routine status, or null for
 *  "active" (which renders no badge — the routine is in its normal
 *  state and doesn't need to shout about it).
 *
 *  Colors route through the shared palette: a proposed routine is
 *  "awaiting approval" (violet, matching AWAITING_APPROVAL everywhere
 *  else); a disabled one reads as inactive (muted, matching SKIPPED). */
export function routineStatusBadge(status: string | null | undefined): RoutineStatusBadge | null {
  switch (normalizeRoutineStatus(status)) {
    case "proposed":
      return {
        label: "Awaiting approval",
        className: STATUS_BADGE_CLASSES.AWAITING_APPROVAL,
        dot: STATUS_DOT_CLASSES.AWAITING_APPROVAL,
      }
    case "disabled":
      return {
        label: "Disabled",
        className: STATUS_BADGE_CLASSES.SKIPPED,
        dot: STATUS_DOT_CLASSES.SKIPPED,
      }
    default:
      return null
  }
}

/** Role hierarchy, highest privilege first. Mirrors helpers.go roleRank. */
const ROLE_RANK: Record<string, number> = {
  OWNER: 5,
  ADMIN: 4,
  MANAGER: 3,
  MEMBER: 2,
  VIEWER: 1,
}

/** True when `role` is at least as privileged as `min`. Unknown / null
 *  roles rank below everything, so they never clear a gate. */
export function roleAtLeast(role: string | null | undefined, min: keyof typeof ROLE_RANK): boolean {
  return (ROLE_RANK[role ?? ""] ?? 0) >= (ROLE_RANK[min] ?? Infinity)
}

/** MANAGER+ may approve / reject a proposed routine. */
export function canApproveRoutine(role: string | null | undefined): boolean {
  return roleAtLeast(role, "MANAGER")
}

/** OWNER / ADMIN may disable (kill) or re-enable a routine. */
export function canKillRoutine(role: string | null | undefined): boolean {
  return roleAtLeast(role, "ADMIN")
}

/** The reason a routine's Run / Test run / Dry run controls are disabled,
 *  or null when the routine is runnable (active). Used as the button
 *  tooltip + the disabled predicate. */
export function runDisabledReason(status: string | null | undefined): string | null {
  switch (normalizeRoutineStatus(status)) {
    case "proposed":
      return "Routine is awaiting approval"
    case "disabled":
      return "Routine is disabled"
    default:
      return null
  }
}
