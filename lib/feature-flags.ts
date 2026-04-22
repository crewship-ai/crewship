/**
 * Frontend feature flags. Backed by `NEXT_PUBLIC_*` env vars so they work
 * under static export (values are inlined at build time).
 *
 * Default is always OFF — call sites must opt in explicitly.
 */

export function cruiseUnifiedUI(): boolean {
  return process.env.NEXT_PUBLIC_CRUISE_UNIFIED_UI === "true"
}
