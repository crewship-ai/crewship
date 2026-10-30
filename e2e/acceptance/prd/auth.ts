// Login and QA-workspace helpers. Credentials stay in memory or in the
// owner-only e2e cookie directory; they are never written to evidence.
import { request as playwrightRequest, type APIRequestContext } from "@playwright/test"
import { randomBytes, randomUUID } from "node:crypto"
import type { PrdAccount } from "./env"

/** Playwright storage state returned by a login (cookies only). */
export type StorageState = Awaited<ReturnType<APIRequestContext["storageState"]>>

/**
 * Logs in through the same credentials endpoint the e2e global setup uses and
 * returns the session cookies. Each call is one login; reuse the result, since
 * the endpoint is rate limited.
 */
export async function loginStorageState(origin: string, account: PrdAccount): Promise<StorageState> {
  const ctx = await playwrightRequest.newContext({ baseURL: origin })
  try {
    const csrf = await ctx.get("/api/auth/csrf")
    if (!csrf.ok()) throw new Error(`login: GET /api/auth/csrf answered ${csrf.status()}`)
    const { csrfToken } = (await csrf.json()) as { csrfToken?: string }
    const res = await ctx.post("/api/auth/callback/credentials", {
      data: { email: account.email, password: account.password, csrfToken, redirect: "false" },
    })
    const body = (await res.json().catch(() => ({}))) as { error?: string }
    if (!res.ok() || body.error) {
      // Never echo the account: the message may end up in a shared report.
      throw new Error(`login failed: ${res.status()} ${body.error ?? ""}`.trim())
    }
    const state = await ctx.storageState()
    if (state.cookies.length === 0) throw new Error("login returned no session cookie")
    return state
  } finally {
    await ctx.dispose()
  }
}

/** Authenticated API context for an account; dispose it when done. */
export async function apiAs(origin: string, state: StorageState): Promise<APIRequestContext> {
  return playwrightRequest.newContext({ baseURL: origin, storageState: state })
}

export interface QaWorkspace {
  id: string
  slug: string
}

function suffix(): string {
  return randomUUID().replaceAll("-", "").slice(0, 10)
}

function label(raw: string): string {
  return raw.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 24) || "scenario"
}

/**
 * Creates a disposable workspace owned by the caller. Scenarios work there,
 * never in a shared demo workspace (adding members to a shared workspace
 * retires other people's open editor sessions).
 */
export async function createQaWorkspace(owner: APIRequestContext, scenario: string): Promise<QaWorkspace> {
  const slug = `prd-qa-${label(scenario)}-${suffix()}`
  const res = await owner.post("/api/v1/workspaces", { data: { name: `PRD acceptance ${scenario}`, slug } })
  if (res.status() !== 201) throw new Error(`create QA workspace: ${res.status()} ${(await res.text()).slice(0, 300)}`)
  const body = (await res.json()) as { id: string; slug?: string }
  return { id: body.id, slug: body.slug ?? slug }
}

/** Deletes a QA workspace created by createQaWorkspace. Returns the HTTP status. */
export async function deleteQaWorkspace(owner: APIRequestContext, ws: QaWorkspace): Promise<number> {
  const res = await owner.delete(`/api/v1/workspaces/${encodeURIComponent(ws.id)}?workspace_id=${encodeURIComponent(ws.id)}`, {
    data: { confirm_slug: ws.slug },
  })
  return res.status()
}

export interface ProvisionedMember {
  userId: string
  account: PrdAccount
}

/**
 * Provisions a NEW synthetic person in a QA workspace and sets a random
 * password through the setup link. The account lives only in memory; the
 * public API cannot delete accounts, so each call leaves one synthetic user
 * with an example.test address behind (its memberships go with the workspace).
 */
export async function provisionMember(
  owner: APIRequestContext,
  origin: string,
  ws: QaWorkspace,
  role: "ADMIN" | "MANAGER" | "MEMBER" | "VIEWER",
  name = "PRD acceptance person",
): Promise<ProvisionedMember> {
  const email = `prd-qa-${suffix()}@example.test`
  const password = `${randomBytes(24).toString("base64url")}Aa9!`
  const res = await owner.post(`/api/v1/workspaces/${encodeURIComponent(ws.id)}/members/provision?workspace_id=${encodeURIComponent(ws.id)}`, {
    data: { email, role, full_name: name, create_only: true },
  })
  if (res.status() !== 201) throw new Error(`provision member: ${res.status()} ${(await res.text()).slice(0, 300)}`)
  const prov = (await res.json()) as { user_id: string; setup_url: string }
  const token = new URL(prov.setup_url, "http://placeholder.invalid").searchParams.get("token")
  if (!token) throw new Error("provision member: setup_url carries no token")
  // The setup link is redeemed anonymously, as the new person would.
  const anon = await playwrightRequest.newContext({ baseURL: origin })
  try {
    const reset = await anon.post("/api/v1/auth/reset", { data: { token, new_password: password } })
    if (reset.status() !== 200) throw new Error(`provision member: password setup answered ${reset.status()}`)
  } finally {
    await anon.dispose()
  }
  return { userId: prov.user_id, account: { email, password } }
}
