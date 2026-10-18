/**
 * Every page route the app ships, as written under app/ without route groups.
 *
 * Why the client needs this: the app is a static export served by the Go
 * backend (internal/api/static.go). A path with no exported page — an old
 * bookmark, a typo — is answered with the ROOT index.html (its SPA fallback),
 * so the dashboard page hydrates under the wrong URL and Next's not-found.tsx
 * never runs. The shell checks the real URL against this list instead and
 * shows the not-found state itself.
 *
 * Kept in step with the filesystem by lib/__tests__/app-routes.test.ts: add a
 * page, add its pattern here. `[param]` matches exactly one segment, which is
 * also all the backend's dynamic-placeholder lookup serves.
 */
export const APP_ROUTE_PATTERNS = [
  "/",
  "/activity",
  "/admin",
  "/admin/backups",
  "/admin/people",
  "/admin/security",
  "/agents",
  "/approvals",
  "/audit",
  "/bootstrap",
  "/calendar",
  "/chat",
  "/chat/[agentSlug]",
  "/conversations",
  "/credentials",
  "/crews",
  "/forgot-password",
  "/inbox",
  "/inbox-v2",
  "/integrations",
  "/issues",
  "/issues/intake",
  "/issues/manage",
  "/issues/[identifier]",
  "/join",
  "/journal",
  "/login",
  "/missions/[id]/timeline",
  "/onboarding",
  "/orchestration",
  "/orchestration/issues/[identifier]",
  "/orchestration/missions/[missionId]",
  "/orchestration/projects/[projectId]",
  "/p/[token]",
  "/pages",
  "/pages/[slug]",
  "/reset-password",
  "/routines",
  "/runs",
  "/settings",
  "/settings/audit",
  "/settings/crew-links",
  "/settings/hooks",
  "/settings/members",
  "/shared-chat",
  "/signup",
  "/skills",
  "/skills/[skillId]",
  "/work",
] as const

const COMPILED = APP_ROUTE_PATTERNS.map((p) => (p === "/" ? [] : p.slice(1).split("/")))

/** Whether a pathname is one of the app's pages (trailing slash tolerated). */
export function isKnownAppPath(pathname: string): boolean {
  const parts = pathname.split("/").filter(Boolean)
  return COMPILED.some(
    (pattern) =>
      pattern.length === parts.length &&
      pattern.every((seg, i) => (seg.startsWith("[") && seg.endsWith("]")) || seg === parts[i]),
  )
}
