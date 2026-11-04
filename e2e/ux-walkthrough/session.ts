// Login and a dedicated "PRD UX walkthrough …" workspace per origin. The
// workspace id is remembered in UX_OUT so reruns reuse it instead of creating
// more. Accounts come from PRD_OWNER_* / PRD_MEMBER_* (sourced by the caller).
import { request as pwRequest, type APIRequestContext, type Browser, type BrowserContext } from "@playwright/test"
import * as fs from "node:fs"
import * as path from "node:path"
import { loginStorageState, provisionMember, type StorageState } from "../acceptance/prd/auth"
import { OUT, VIEWPORTS, type Viewport } from "./audit"

export const ORIGIN = (process.env.UX_ORIGIN ?? "").replace(/\/$/, "")
if (!ORIGIN) throw new Error("UX_ORIGIN is required")
const HOST = new URL(ORIGIN).hostname.split(".")[0]
const STATE = path.join(OUT, `state-${HOST}.json`)

interface Saved {
  workspace?: { id: string; slug: string }
  peer?: { userId: string; email: string; password: string }
  [k: string]: unknown
}

export function readSaved(): Saved {
  try {
    return JSON.parse(fs.readFileSync(STATE, "utf8")) as Saved
  } catch {
    return {}
  }
}
export function writeSaved(s: Saved): void {
  // The peer password is synthetic (example.test) and only for this QA workspace; keep the file owner-only.
  fs.writeFileSync(STATE, JSON.stringify(s, null, 2), { mode: 0o600 })
}

function owner() {
  const email = process.env.PRD_OWNER_EMAIL, password = process.env.PRD_OWNER_PASSWORD
  if (!email || !password) throw new Error("PRD_OWNER_EMAIL/PASSWORD not set")
  return { email, password }
}

let ownerState: StorageState | null = null
export async function ownerStorage(): Promise<StorageState> {
  ownerState ??= await loginStorageState(ORIGIN, owner())
  return ownerState
}

export async function ownerApi(): Promise<APIRequestContext> {
  return pwRequest.newContext({ baseURL: ORIGIN, storageState: await ownerStorage() })
}

/** The walkthrough's own workspace on this origin. */
export async function walkWorkspace(api: APIRequestContext): Promise<{ id: string; slug: string }> {
  const saved = readSaved()
  if (saved.workspace) {
    const r = await api.get(`/api/v1/workspaces/${saved.workspace.id}?workspace_id=${saved.workspace.id}`)
    if (r.ok()) return saved.workspace
  }
  const stamp = new Date().toISOString().slice(0, 16).replace(/[-:T]/g, "")
  const slug = `prd-ux-walk-${HOST}-${stamp}`.slice(0, 40)
  const res = await api.post("/api/v1/workspaces", { data: { name: `PRD UX walkthrough ${HOST} ${stamp}`, slug } })
  if (res.status() !== 201) throw new Error(`create workspace: ${res.status()} ${(await res.text()).slice(0, 200)}`)
  const b = (await res.json()) as { id: string; slug?: string }
  saved.workspace = { id: b.id, slug: b.slug ?? slug }
  writeSaved(saved)
  return saved.workspace
}

/** A synthetic second person (MEMBER) in the walkthrough workspace, provisioned once. */
export async function walkPeer(api: APIRequestContext, ws: { id: string; slug: string }) {
  const saved = readSaved()
  if (saved.peer) return saved.peer
  const p = await provisionMember(api, ORIGIN, ws, "MEMBER", "UX Kolegyně")
  saved.peer = { userId: p.userId, ...p.account }
  writeSaved(saved)
  return saved.peer
}

export async function browserContext(browser: Browser, vp: Viewport, storage: StorageState): Promise<BrowserContext> {
  const mobile = vp === "mobile"
  return browser.newContext({
    baseURL: ORIGIN,
    storageState: storage,
    viewport: VIEWPORTS[vp],
    deviceScaleFactor: mobile ? 2 : 1,
    isMobile: mobile,
    hasTouch: mobile,
    locale: "cs-CZ",
  })
}

/**
 * A separate login for each browser context. Two contexts sharing one cookie
 * jar trip the server's session-rotation guard ("Signed out on this device").
 */
export async function freshOwnerStorage(): Promise<StorageState> {
  return loginStorageState(ORIGIN, owner())
}
