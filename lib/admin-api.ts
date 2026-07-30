import { apiFetch } from "@/lib/api-fetch"

/**
 * Fetch an admin API route, workspace-scoped.
 *
 * Every route under `/api/v1/admin/**` sits behind RequireWorkspace, which
 * resolves `workspace_id` from the query string and returns
 * `400 workspace_id is required` before the handler runs. Forgetting it is
 * therefore not a subtle bug — the call always fails — but it is an *invisible*
 * one, because each caller handles the failure differently: the security posture
 * card showed "HTTP 400", the evaluator card silently fell back to read-only, and
 * the model picker rendered the raw server message inside a search dialog.
 *
 * This has now been the same mistake four times, in four cards, each fixed
 * individually. Appending the parameter by hand at every call site is the thing
 * that keeps failing; this makes it structural.
 *
 * Two behaviours worth knowing:
 *
 *  - A missing workspace is a REJECTED promise, not a request. A 400 from the
 *    server looks like a server problem and sends whoever is debugging it to the
 *    backend; a rejection here names the caller and the route.
 *  - An existing query string is preserved, and an existing `workspace_id` is
 *    left alone, so a caller that genuinely needs a different scope can still
 *    pass one explicitly.
 */
export function adminFetch(
  path: string,
  workspaceId: string | null | undefined,
  init?: RequestInit,
): Promise<Response> {
  if (!workspaceId) {
    return Promise.reject(
      new Error(
        `adminFetch: ${path} needs a workspace. Every /api/v1/admin route is ` +
          `workspace-scoped by middleware — wait for workspaceId before calling.`,
      ),
    )
  }
  return apiFetch(withWorkspace(path, workspaceId), init)
}

/**
 * Append `workspace_id` to a path, preserving anything already there.
 *
 * Exported for the handful of callers that need the URL itself (an EventSource,
 * a link, a test asserting the shape) rather than the fetch.
 */
export function withWorkspace(path: string, workspaceId: string): string {
  const [base, query = ""] = path.split("?", 2)
  const params = new URLSearchParams(query)
  if (!params.get("workspace_id")) {
    params.set("workspace_id", workspaceId)
  }
  return `${base}?${params.toString()}`
}
