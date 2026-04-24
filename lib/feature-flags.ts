/**
 * Frontend feature flags. Backed by `NEXT_PUBLIC_*` env vars so they work
 * under static export (values are inlined at build time).
 *
 * Default is always OFF — call sites must opt in explicitly.
 */

export function crewsUnifiedUI(): boolean {
  return process.env.NEXT_PUBLIC_CREWS_UNIFIED_UI === "true"
}
