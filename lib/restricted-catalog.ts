// A restricted member's catalog (private routines, Page actions) is in exactly
// one of these states, never two at once (#2877). "runtime_missing" is shown
// only on the backend's explicit machine code — a bare 404 stays opaque and
// reads as "unavailable", because access refusals deliberately look the same.
export type RestrictedCatalogState = "loading" | "ready" | "unavailable" | "runtime_missing"

export const RESTRICTED_RUNTIME_UNAVAILABLE = "restricted_runtime_unavailable"

export const RESTRICTED_RUNTIME_MISSING_MESSAGE = "Private execution isn't installed on this server — ask an administrator."

export async function restrictedCatalogFailure(response: Response): Promise<Exclude<RestrictedCatalogState, "loading" | "ready">> {
  if (response.status !== 503) return "unavailable"
  try {
    const body = await response.json() as { code?: unknown }
    return body?.code === RESTRICTED_RUNTIME_UNAVAILABLE ? "runtime_missing" : "unavailable"
  } catch {
    return "unavailable"
  }
}
