// Dev-only console logging for client components (#1000).
//
// Several flows deliberately keep raw server error bodies OUT of the DOM
// (they can echo credential material, SQL fragments, or stack traces) and
// used to console.warn them "for operator debugging". Those calls shipped
// in production bundles and kept the No-Console-Logs pre-merge check
// yellow on every PR. devWarn is the sanctioned home for that pattern:
// the operator-debug detail survives in development, production stays
// silent (NODE_ENV is inlined at build time, so the branch is dead code
// in prod bundles). User-facing feedback belongs in a toast, not here.
export function devWarn(...args: unknown[]): void {
  if (process.env.NODE_ENV !== "production") {
    console.warn(...args)
  }
}
