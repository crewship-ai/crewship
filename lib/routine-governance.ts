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

export type RoutineStatus = "active" | "proposed" | "disabled"

/** Normalise an unknown wire value to a RoutineStatus. Absent / unknown
 *  values are treated as "active" — a routine the backend hasn't tagged
 *  yet should remain fully usable, not silently locked out. */
export function normalizeRoutineStatus(status: string | null | undefined): RoutineStatus {
  return status === "proposed" || status === "disabled" ? status : "active"
}

export interface RoutineStatusBadge {
  label: string
  /** Tailwind tone classes mirroring the `bg-{c}-500/20 text-{c}-400`
   *  pattern used by lib/colors STATUS_BADGE_CLASSES so the pill matches
   *  the status pills rendered elsewhere (Inbox / Issues / Activity). */
  bg: string
  text: string
  dot: string
}

/** Returns the badge descriptor for a routine status, or null for
 *  "active" (which renders no badge — the routine is in its normal
 *  state and doesn't need to shout about it). */
export function routineStatusBadge(status: string | null | undefined): RoutineStatusBadge | null {
  switch (normalizeRoutineStatus(status)) {
    case "proposed":
      return {
        label: "Awaiting approval",
        bg: "bg-amber-500/20",
        text: "text-amber-400",
        dot: "bg-amber-400",
      }
    case "disabled":
      return {
        label: "Disabled",
        bg: "bg-muted",
        text: "text-muted-foreground",
        dot: "bg-muted-foreground/60",
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
