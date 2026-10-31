// Isolated people for the outage-lostack scenario.
//
// The owner logs in once per test only to create a disposable QA workspace and
// provision a new example.test person in it; that person does all the editing.
// Why not the shared owner session from global setup: refresh tokens rotate on
// save, and a deployment's owner account is used by other sessions at the same
// time, so a long editing test on the shared owner session can be signed out
// ("session_revoked") halfway through. A person nobody else knows cannot be.
import type { APIRequestContext, Browser, BrowserContext, BrowserContextOptions, Page } from "@playwright/test"
import { apiAs, createQaWorkspace, deleteQaWorkspace, loginStorageState, provisionMember, type QaWorkspace } from "../auth"
import type { PrdScenario } from "../fixtures"

export interface Actor {
  /** Owner API (own login): workspace administration only. */
  owner: APIRequestContext
  /** The provisioned person's API. */
  api: APIRequestContext
  context: BrowserContext
  page: Page
  ws: QaWorkspace
  userId: string
}

// MANAGER: the least role the default creation policy lets create Projects (MEMBER gets 403).
const ROLE = "MANAGER"
const cleanups: Array<() => Promise<unknown>> = []

export async function isolatedActor(prd: PrdScenario, browser: Browser, title: string, options: BrowserContextOptions = {}): Promise<Actor> {
  const ownerAccount = prd.env.accounts.owner
  if (!ownerAccount) throw new Error("owner account missing")
  const owner = await apiAs(prd.origin, await loginStorageState(prd.origin, ownerAccount))
  cleanups.push(() => owner.dispose())
  const ws = await createQaWorkspace(owner, title)
  if (process.env.PRD_KEEP_WORKSPACES !== "1") {
    // Runs before owner.dispose (cleanups run newest first).
    cleanups.push(async () => {
      const status = await deleteQaWorkspace(owner, ws)
      if (status >= 300) console.warn(`[prd] QA workspace ${ws.id} not deleted (HTTP ${status})`)
    })
  }
  const person = await provisionMember(owner, prd.origin, ws, ROLE, "PRD outage person")
  const state = await loginStorageState(prd.origin, person.account)
  const api = await apiAs(prd.origin, state)
  cleanups.push(() => api.dispose())
  const context = await browser.newContext({ baseURL: prd.origin, storageState: state, ...options })
  cleanups.push(() => context.close())
  const page = await context.newPage()
  return { owner, api, context, page, ws, userId: person.userId }
}

/** Call from test.afterEach: closes contexts and deletes the QA workspaces. */
export async function disposeActors(): Promise<void> {
  for (const c of cleanups.splice(0).reverse()) {
    try {
      await c()
    } catch (err) {
      console.warn(`[prd] cleanup failed: ${String(err)}`)
    }
  }
}
