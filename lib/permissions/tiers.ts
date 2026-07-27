/**
 * Workspace role tiers, mirroring the server's mutation gates.
 *
 * The API declares a tier per mutation route in internal/api/rbac_routes.go:
 * `roleCreate` (MANAGER and up) and `roleManage` (ADMIN and up). The UI has
 * to gate on the SAME thing, or it offers people buttons that answer 403.
 *
 * Why not CASL (lib/permissions/abilities.ts)? Because on two routes it
 * disagrees with the server, and disagreeing quietly is the failure mode
 * this module exists to avoid:
 *
 *   · CASL grants MANAGER `update Crew`, but `PATCH /api/v1/crews/{crewId}`
 *     is roleManage — ADMIN and up. Gating on CASL shows a MANAGER an
 *     editable container-limits form that cannot save.
 *   · CASL grants MANAGER only `read Member`, but
 *     `PATCH .../members/{id}` (role change) is roleCreate — MANAGER and up.
 *     Gating on CASL hides a control a MANAGER is allowed to use.
 *
 * CASL still earns its keep for coarse read/write intent across subjects.
 * Use these helpers specifically where a control maps to one known route,
 * which is every mutation in Settings.
 *
 * Routes gated by `roleInline` (notification channels, per-agent edits) are
 * deliberately absent: those run a role-OR-capability check inside the
 * handler, so a MEMBER holding an explicit grant passes. Gate those with
 * `useAbilities().hasCapability(...)` alongside the role, never with these.
 */

/** Roles that satisfy the server's `roleManage` tier. */
const ADMIN_TIER = new Set(["OWNER", "ADMIN"])

/** Roles that satisfy the server's `roleCreate` tier. */
const MANAGER_TIER = new Set(["OWNER", "ADMIN", "MANAGER"])

/**
 * True when the role may call a `roleManage` route — workspace rename and
 * delete, crew updates, adding and removing members.
 *
 * An unknown or still-loading role is treated as NOT permitted: rendering a
 * control and retracting it a beat later is worse than showing it late.
 */
export function isAdminTier(role: string | null | undefined): boolean {
  return role != null && ADMIN_TIER.has(role)
}

/**
 * True when the role may call a `roleCreate` route — crew connections,
 * member role changes.
 */
export function isManagerTier(role: string | null | undefined): boolean {
  return role != null && MANAGER_TIER.has(role)
}

/** True only for the workspace owner — irreversible actions. */
export function isOwner(role: string | null | undefined): boolean {
  return role === "OWNER"
}
