/**
 * Where a signed-out visitor of an app screen is sent: the login page, with
 * the screen they asked for as `?redirect=` so signing in brings them back to
 * it instead of to the dashboard (the login page already honours it through
 * `safeRedirectPath`). A deep link opened in a fresh browser used to lose its
 * target here.
 *
 * `reason=expired` is NOT added here. A session that dies while the app is
 * open is caught by the auth provider (hooks/use-auth.tsx, goLoginExpired),
 * which sends both parameters itself; the shell only sees the visitor who
 * never had a session, or the one who just pressed Log out — and an explicit
 * Log out goes to the plain login page.
 */
export function loginRedirectUrl(pathname: string, search = "", hadSession = false): string {
  if (hadSession) return "/login"
  const current = pathname + search
  if (!current.startsWith("/") || current.startsWith("//") || pathname === "/login" || current === "/") return "/login"
  return `/login?${new URLSearchParams({ redirect: current }).toString()}`
}
