/**
 * "Where did I come from" for detail screens.
 *
 * An issue opened from an agent's Issues cell used to send you back to the
 * issue board, because the back arrow was hard-coded to /issues. You lost the
 * agent you were working on and had to find it again. The origin travels in
 * the URL rather than in memory so it survives a refresh, a copy-pasted link
 * and a new tab — router.back() survives none of those.
 *
 * The destination is attacker-controllable by construction: it is a query
 * parameter. `parseReturnTo` therefore accepts only in-app absolute paths.
 * "//evil.example" is a protocol-relative URL that browsers treat as another
 * origin, so a naive startsWith("/") check is an open redirect.
 */

export interface ReturnTo {
  href: string
  label: string
}

/** Query string (no leading "?") that carries the origin of a navigation. */
export function buildReturnTo(href: string, label: string): string {
  return `from=${encodeURIComponent(href)}&fromLabel=${encodeURIComponent(label)}`
}

/** Appends the origin to a destination that may already carry a query. */
export function withReturnTo(destination: string, href: string, label: string): string {
  return `${destination}${destination.includes("?") ? "&" : "?"}${buildReturnTo(href, label)}`
}

/** Any origin will do — it exists only to be compared against. */
const RETURN_TO_BASE = "http://return-to.invalid"

export function parseReturnTo(from: string | null, label: string | null): ReturnTo | null {
  if (!from) return null
  // Must look like an in-app absolute path before anything else. This alone is
  // NOT the guard — see below — it just keeps relative values ("crews") from
  // resolving into something that looks in-app.
  if (!from.startsWith("/")) return null

  // The guard is: resolve it, and require the result to have stayed on the
  // origin we resolved against. A prefix check cannot do this job, because
  // browsers follow the WHATWG URL rules, where a BACKSLASH is an authority
  // separator for special schemes. "/\evil.example" starts with "/" and not
  // with "//", so it passes every prefix test — and then resolves to
  // http://evil.example/. The parser is the only thing that agrees with the
  // browser about what a string means, so the parser is what decides.
  let resolved: URL
  try {
    resolved = new URL(from, RETURN_TO_BASE)
  } catch {
    return null
  }
  if (resolved.origin !== RETURN_TO_BASE) return null

  const trimmed = (label ?? "").trim()
  // Hand back the RESOLVED path, not the raw input: they differ exactly when
  // the input was trying to be something else.
  return {
    href: `${resolved.pathname}${resolved.search}${resolved.hash}`,
    label: trimmed === "" ? "Back" : trimmed,
  }
}
