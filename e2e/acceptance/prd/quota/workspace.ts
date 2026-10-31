// A QA workspace shared by the tests of one describe block. The harness's
// prd.qaWorkspace() is per test; the Project quota tests share one filled
// history (256 MiB) instead of filling it once per requirement. Same rules as
// the harness: PRD_WORKSPACE_ID is reused and kept, a created workspace is
// deleted afterwards unless PRD_KEEP_WORKSPACES=1.
import { request as playwrightRequest, type APIRequestContext } from "@playwright/test"
import { createQaWorkspace, deleteQaWorkspace, loginStorageState, type QaWorkspace, type StorageState } from "../auth"
import { readPrdEnv, storageStatePath } from "../env"

/**
 * A fresh owner session with its own refresh chain. A browser context
 * refreshes (rotates) its session; a context opened later from the cookie jar
 * global setup stored can then find that session already rotated and land on
 * the login page. Each long or browser-driven step therefore logs in itself.
 */
export async function ownerLogin(): Promise<StorageState> {
  const env = readPrdEnv()
  if (!env.accounts.owner) throw new Error("owner account is not configured")
  return loginStorageState(env.origin, env.accounts.owner)
}

export class SharedWorkspace {
  api: APIRequestContext | null = null
  workspace: QaWorkspace | null = null
  private created = false

  async open(label: string): Promise<{ api: APIRequestContext; workspace: QaWorkspace }> {
    const env = readPrdEnv()
    const state = env.accounts.owner ? await ownerLogin() : storageStatePath(env, "owner")
    this.api = await playwrightRequest.newContext({ baseURL: env.origin, storageState: state })
    if (env.workspaceId) {
      const res = await this.api.get(`/api/v1/workspaces/${encodeURIComponent(env.workspaceId)}`)
      if (res.status() !== 200) throw new Error(`PRD_WORKSPACE_ID must be readable by the owner (HTTP ${res.status()})`)
      const body = (await res.json()) as { id: string; slug: string }
      this.workspace = { id: body.id, slug: body.slug }
    } else {
      this.workspace = await createQaWorkspace(this.api, label)
      this.created = true
    }
    return { api: this.api, workspace: this.workspace }
  }

  async close(): Promise<void> {
    try {
      if (this.api && this.workspace && this.created && process.env.PRD_KEEP_WORKSPACES !== "1") {
        // A fresh session: the one opened before the fill may have expired.
        const env = readPrdEnv()
        const api = await playwrightRequest.newContext({ baseURL: env.origin, storageState: await ownerLogin() })
        let status: number
        try {
          status = await deleteQaWorkspace(api, this.workspace)
        } finally {
          await api.dispose()
        }
        if (status >= 300) console.warn(`[prd] QA workspace ${this.workspace.id} not deleted (HTTP ${status})`)
      }
    } finally {
      await this.api?.dispose()
      this.api = null
    }
  }
}
