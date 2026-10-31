import { randomUUID } from "node:crypto"
import type { BrowserContext, Page } from "@playwright/test"
export type DraftKeyFixtureStore = Map<string, { human: string; workspace: string; page: string; key: string }>
const contexts = new WeakMap<BrowserContext, DraftKeyFixtureStore>()
/** HTTP fixture only. The real Go route separately verifies encryption and ACLs. */
export async function installLocalDraftKeyFixture(page: Page, options?: { human?: () => string; readable?: () => boolean; envelopes?: DraftKeyFixtureStore }) {
  let envelopes = options?.envelopes ?? contexts.get(page.context())
  if (!envelopes) { envelopes = new Map(); contexts.set(page.context(), envelopes) }
  const server = envelopes
  await page.route("**/api/v1/pages/*/draft-key?*", async route => {
    const input = route.request().postDataJSON(), url = new URL(route.request().url())
    const target = decodeURIComponent(url.pathname.split("/pages/@")[1]?.split("/")[0] ?? "")
    let status = 200, result: Record<string, unknown>
    if (options?.human && input.human !== options.human()) { status = 403; result = { code: "draft_identity_mismatch" } }
    else if (options?.readable && !options.readable()) { status = 403; result = { code: "draft_read_revoked" } }
    else if (input.operation === "wrap") {
      const envelope = `v1:fixture-${randomUUID()}`
      server.set(envelope, { human: input.human, workspace: url.searchParams.get("workspace_id")!, page: target, key: input.key }); result = { envelope }
    } else {
      const stored = server.get(input.envelope)
      if (!stored || stored.human !== input.human || stored.workspace !== url.searchParams.get("workspace_id") || stored.page !== target) { status = 400; result = {} }
      else result = { key: stored.key }
    }
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(result) })
  })
}
