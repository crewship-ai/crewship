import type { APIRequestContext } from "@playwright/test"

export interface AppBuild {
  /** Git commit of the running server, or "unknown" when it cannot be read. */
  sha: string
  version: string | null
  dirty: boolean | null
  /** Why sha is "unknown"; null when it was read. */
  error: string | null
}

/**
 * Reads the deployed build from GET /api/v1/system/version (authenticated;
 * `commit` is stamped into the binary at build time).
 */
export async function readAppBuild(authed: APIRequestContext): Promise<AppBuild> {
  try {
    const res = await authed.get("/api/v1/system/version")
    if (!res.ok()) return { sha: "unknown", version: null, dirty: null, error: `GET /api/v1/system/version answered ${res.status()}` }
    const body = (await res.json()) as { commit?: string; current?: string; dirty?: boolean }
    const sha = typeof body.commit === "string" && body.commit ? body.commit : "unknown"
    return {
      sha,
      version: typeof body.current === "string" ? body.current : null,
      dirty: typeof body.dirty === "boolean" ? body.dirty : null,
      error: sha === "unknown" ? "version response has no commit" : null,
    }
  } catch (err) {
    return { sha: "unknown", version: null, dirty: null, error: String(err) }
  }
}
