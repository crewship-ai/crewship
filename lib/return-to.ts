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

export function parseReturnTo(from: string | null, label: string | null): ReturnTo | null {
  if (!from) return null
  // Same-origin, in-app paths only. Reject "//host", "https://…", "javascript:"
  // and anything else that would leave the app.
  if (!from.startsWith("/") || from.startsWith("//")) return null
  const trimmed = (label ?? "").trim()
  return { href: from, label: trimmed === "" ? "Back" : trimmed }
}
