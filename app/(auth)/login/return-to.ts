import { areaOf } from "@/lib/nav-sections"
import { safeRedirectPath } from "./safe-redirect"

/**
 * The page a sign-in returns to, by its name in the navigation (AUTH-01,
 * auth-public.md A06): "/issues/ISS-12?tab=activity" → "Issues".
 *
 * The raw `?redirect=` goes through the same `safeRedirectPath` the sign-in
 * navigates with, so a target it refuses (an absolute URL, `//host`, a
 * backslash, `/login`) names nothing. A path the navigation has no name for
 * names nothing either: the card never shows a raw path.
 */
export function returnPageName(raw: string | null): string | null {
  if (!raw) return null
  const target = safeRedirectPath(raw)
  // safeRedirectPath answers "/" for a refused target; only an explicit "/"
  // is the Dashboard.
  if (target === "/" && raw !== "/") return null
  const pathname = target.split(/[?#]/, 1)[0] || "/"
  return areaOf(pathname)?.title ?? null
}
