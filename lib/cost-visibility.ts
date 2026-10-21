/**
 * Cost figures are OWNER/ADMIN only on the server (#3590). For every other
 * role a mixed response arrives with its cost fields left out — absent, not
 * 0 and not null — so "hidden" must never render as "$0.00" or "none
 * reported". Every cost cell checks with isCostShown before formatting.
 */
export function isCostShown(v: unknown): v is number {
  return typeof v === "number" && Number.isFinite(v)
}

/** One sentence for the places that say why an amount is missing. */
export const COST_HIDDEN_NOTE = "Amounts are visible to workspace owners and admins."
