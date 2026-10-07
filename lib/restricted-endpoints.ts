/**
 * The API surface a restricted session may reach, as the frontend knows it.
 *
 * Authority lives in the backend, not here: `internal/api/restricted_access.go`
 * (`restrictedRequest`) is evaluated for every authenticated route
 * (`internal/api/middleware.go`), and it applies to the whole ACCOUNT as soon as
 * one membership is restricted (`access.Store.HasRestrictedMembership`). Any
 * other route answers 404 for that session.
 *
 * This list mirrors that allowlist so the frontend can be tested against it:
 * the shell tests record every request a restricted session makes and fail on
 * anything that does not match. `lib/__tests__/restricted-endpoints.test.ts`
 * reads the Go file and fails when the two drift apart, so a route added to
 * (or dropped from) the allowlist must be added here in the same change.
 *
 * Patterns use Go 1.22 `http.ServeMux` syntax, exactly as they appear in the Go
 * source: `{name}` matches one non-empty path segment.
 */
export const RESTRICTED_FE_ENDPOINTS = [
  "GET /api/v1/chats/{chatId}/project-input-options",
  "GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files",
  "GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download",
  "POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files",
  "DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}",
  "GET /api/v1/chats/{chatId}/restricted-context",
  "POST /api/v1/chats/{chatId}/restricted-memory",
  "DELETE /api/v1/chats/{chatId}/restricted-memory/{entryId}",
  "POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight",
  "GET /api/v1/chats/{chatId}/restricted-files",
  "GET /api/v1/chats/{chatId}/restricted-files/{fileId}/download",
  "GET /api/v1/agents",
  "GET /api/v1/agents/{agentId}",
  "GET /api/v1/workspaces",
  "GET /api/v1/auth/sessions",
  "POST /api/v1/auth/sessions/{id}/revoke",
  "GET /api/v1/auth/cli-token/validate",
  "GET /api/v1/auth/cli-tokens",
  "DELETE /api/v1/auth/cli-tokens/{tokenId}",
  "POST /api/v1/users/me/password",
  "GET /api/v1/agents/{agentId}/chats",
  "GET /api/v1/workspaces/{workspaceId}/restricted-pages",
  "GET /api/v1/workspaces/{workspaceId}/restricted-routines",
  "GET /api/v1/pages/{slug}/application/actions/{pendingId}",
  "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run",
  "GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}",
  "GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs",
  "POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}",
  "POST /api/v1/pages/{slug}/application/actions/{panelId}/{actionId}",
  "GET /api/v1/agents/{agentId}/run-profile",
  "POST /api/v1/agents/{agentId}/restricted-cli-chats",
  "GET /api/v1/chats/{chatId}/execution-profile",
  "POST /api/v1/chats/{chatId}/restricted-run",
  "POST /api/v1/chats/{chatId}/restricted-cli-run",
  "GET /api/v1/chats/{chatId}/restricted-attempts",
  "POST /api/v1/agents/{agentId}/chats",
  "GET /api/v1/chats/{chatId}/messages",
] as const

/**
 * The NextAuth-compatible session routes. They are mounted outside the
 * authenticated middleware (`internal/api/router_auth.go`), so the restricted
 * allowlist never sees them: logging in, reading the session, rotating the
 * access cookie and logging out work for every account.
 */
export const PUBLIC_AUTH_ENDPOINTS = [
  "GET /api/auth/csrf",
  "GET /api/auth/providers",
  "GET /api/auth/session",
  "POST /api/auth/callback/credentials",
  "POST /api/auth/token/refresh",
  "GET /api/auth/signin",
  "POST /api/auth/signout",
  "GET /api/auth/error",
] as const

/** Splits "METHOD /path" into its parts. */
function split(pattern: string): { method: string; segments: string[] } {
  const space = pattern.indexOf(" ")
  return { method: pattern.slice(0, space), segments: pattern.slice(space + 1).split("/") }
}

const isWildcard = (segment: string) => /^\{[^}]+\}$/.test(segment)

function pathOf(url: string): string {
  try {
    return new URL(url, "http://local.invalid").pathname
  } catch {
    return url.split("?")[0]
  }
}

/** True when `pattern` (Go mux syntax) matches this method and path. */
export function patternMatches(pattern: string, method: string, path: string): boolean {
  const p = split(pattern)
  if (p.method !== method.toUpperCase()) return false
  const parts = path.split("/")
  if (parts.length !== p.segments.length) return false
  return p.segments.every((segment, i) => (isWildcard(segment) ? parts[i] !== "" : segment === parts[i]))
}

/**
 * Go's mux sends a request to the MOST SPECIFIC matching pattern, and the
 * allowlist switches on that pattern (`r.Pattern`). So
 * `GET /api/v1/agents/crews-status` is not covered by the allowed
 * `GET /api/v1/agents/{agentId}`: a literal route registered for that path wins
 * and is denied. Returns true when `a` is more specific than `b` (a literal
 * segment beats a wildcard at the first position where they differ).
 */
function moreSpecific(a: string, b: string): boolean {
  const sa = split(a).segments
  const sb = split(b).segments
  for (let i = 0; i < sa.length; i++) {
    const wa = isWildcard(sa[i])
    const wb = isWildcard(sb[i])
    if (wa !== wb) return !wa
  }
  return false
}

/**
 * True when a restricted session may send this request: it reaches a pattern on
 * the restricted allowlist, or one of the public session routes. Query strings
 * and an absolute origin are ignored; only method and path are compared, as the
 * Go mux does.
 *
 * `registered` is every route pattern the server registers. With it, a request
 * whose path is claimed by a more specific, non-allowlisted route is refused,
 * exactly as the server refuses it. Tests pass the list parsed from the Go
 * source (`lib/__tests__/restricted-route-oracle.ts`).
 */
export function isRestrictedAllowedRequest(
  method: string | undefined,
  url: string,
  registered: readonly string[] = [],
): boolean {
  const verb = (method ?? "GET").toUpperCase()
  const path = pathOf(url)
  const allowed: readonly string[] = [...RESTRICTED_FE_ENDPOINTS, ...PUBLIC_AUTH_ENDPOINTS]
  const hit = allowed.find((pattern) => patternMatches(pattern, verb, path))
  if (!hit) return false
  return !registered.some(
    (pattern) => !allowed.includes(pattern) && patternMatches(pattern, verb, path) && moreSpecific(pattern, hit),
  )
}
