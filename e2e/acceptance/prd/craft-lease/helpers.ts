// Helpers shared by the native Word/Sheet scenarios (lease, lost read right,
// data round trip). Not a test file: Playwright only collects *.acceptance.ts.
import type { APIRequestContext, Page, Response } from "@playwright/test"
import { apiAs, loginStorageState } from "../auth"
import { expect, test, type PrdScenario } from "../fixtures"
import type { Admission, NativePage } from "./native"

export const PEER_NAME = "PRD Lease Peer"

/**
 * Whether the server under test has a native Word/Sheet editor profile,
 * decided before and independently of initialize(), which is one of the calls
 * A.craft.lease.fail-closed checks. PRD_CRAFT_NATIVE=on|off states it
 * explicitly; full mode always requires the profile; otherwise quick mode reads
 * the configuration-derived flag of GET /api/v1/documents/status.
 */
export async function nativeProfile(prd: PrdScenario, owner: APIRequestContext, doc: NativePage): Promise<boolean> {
  const declared = process.env.PRD_CRAFT_NATIVE?.trim().toLowerCase()
  if (declared === "on") return true
  if (declared === "off") return false
  if (declared) throw new Error(`PRD_CRAFT_NATIVE must be "on" or "off", got ${JSON.stringify(declared)}`)
  if (prd.mode === "full") return true
  return doc.profileAdvertised(owner)
}

/**
 * Initializes the document as native. Skips (quick) or fails (full) when the
 * server has no native editor profile; once a profile is known to exist,
 * initialize must succeed.
 */
export async function requireNative(prd: PrdScenario, owner: APIRequestContext, doc: NativePage): Promise<void> {
  const profile = await nativeProfile(prd, owner, doc)
  test.skip(!profile && prd.mode === "quick", "server runs without a native Word/Sheet editor profile (CREWSHIP_CRAFT_NATIVE_*)")
  expect(profile, "full mode needs an origin with native Word/Sheet editors enabled").toBe(true)
  expect(await doc.initialize(owner), "initialize on a server with a native editor profile").toBe(true)
  const availability = await doc.availability(owner)
  expect(availability.enabled && availability.can_edit, "the owner may edit the new native document").toBe(true)
}

export async function peerApi(prd: PrdScenario, cleanup: Array<() => Promise<void>>) {
  const peer = await prd.provision("MEMBER", PEER_NAME)
  const state = await loginStorageState(prd.origin, peer.account)
  const api = await apiAs(prd.origin, state)
  cleanup.push(() => api.dispose())
  return { peer, state, api }
}

export interface CraftCall {
  at: string
  who: string
  method: string
  path: string
  status: number | string
}

/** Records every native editor call of a page (method, path, status; no headers or bodies). */
export function traceCraft(page: Page, who: string, trail: CraftCall[]): void {
  const path = (url: string) => new URL(url).pathname.replace(/^\/api\/v1\/pages\/[^/]+\/craft\//, "craft/")
  const keep = (url: string) => new URL(url).pathname.includes("/craft/")
  page.on("response", (r) => {
    if (keep(r.url())) trail.push({ at: new Date().toISOString(), who, method: r.request().method(), path: path(r.url()), status: r.status() })
  })
  page.on("requestfailed", (r) => {
    if (keep(r.url())) trail.push({ at: new Date().toISOString(), who, method: r.method(), path: path(r.url()), status: `failed: ${r.failure()?.errorText ?? "?"}` })
  })
  page.on("requestfinished", (r) => {
    if (keep(r.url()) && path(r.url()) === "craft/events") trail.push({ at: new Date().toISOString(), who, method: r.method(), path: "craft/events", status: "stream ended" })
  })
}

export function isCraft(r: Response, path: string): boolean {
  return r.request().method() === "POST" && new URL(r.url()).pathname.endsWith(`/craft/${path}`)
}

/** Remembers the latest native admission the editor in `page` received (memory only). */
export function captureAdmission(page: Page): { current(): Admission | null } {
  let latest: Admission | null = null
  page.on("response", (r) => {
    if (!isCraft(r, "sessions") || r.status() !== 201) return
    void r
      .json()
      .then((b: { session_token?: string; session_id?: string }) => {
        if (b.session_token && b.session_id) latest = { token: b.session_token, sessionId: b.session_id }
      })
      .catch(() => {})
  })
  return { current: () => latest }
}
