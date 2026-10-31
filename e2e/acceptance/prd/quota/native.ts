// Native Word/Sheet (WordCraft/GridCraft) helpers of the quota scenario. They
// use the same HTTP API and editor host page as a person; the editor UI is
// Czech. Session tokens stay in memory and never reach evidence.
import { expect, type APIRequestContext, type Frame, type Page } from "@playwright/test"
import * as fs from "node:fs"
import { emptyGridCraftNativeDocument } from "../../../../lib/mooring/gridcraft-empty"

export type NativeApp = "wordcraft" | "gridcraft"
export const NATIVE_APPS: ReadonlyArray<{ app: NativeApp; label: "Word" | "Sheet"; frameTitle: string }> = [
  { app: "wordcraft", label: "Word", frameTitle: "WordCraft editor" },
  { app: "gridcraft", label: "Sheet", frameTitle: "GridCraft editor" },
]

export interface NativeQuota {
  usedBytes: string
  limitBytes: string
  warning: boolean
  retention: string
}

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const q = (ws: string) => `workspace_id=${encodeURIComponent(ws)}`
const craft = (pageId: string) => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/`

// lib/mooring/wordcraft-native-create.ts imports this JSON; Playwright's ESM
// loader needs an import attribute for it, so read the same file instead.
const WORD_EMPTY = new URL("../../../../lib/mooring/wordcraft-native-empty.json", import.meta.url)

/** The same blank documents the "New Doc" dialog initializes with. */
function emptyBytes(app: NativeApp): Buffer {
  if (app === "gridcraft") return Buffer.from(emptyGridCraftNativeDocument())
  return Buffer.from(JSON.stringify(JSON.parse(fs.readFileSync(WORD_EMPTY, "utf8"))))
}

/** Which native editors the origin offers in a workspace. */
export async function nativeAvailability(api: APIRequestContext, ws: string) {
  const res = await api.get(`/api/v1/documents/status?${q(ws)}`)
  if (!res.ok()) return { status: res.status(), wordcraft: false, gridcraft: false }
  const v = (await res.json()) as { wordcraft_native?: boolean; gridcraft_native?: boolean }
  return { status: res.status(), wordcraft: v.wordcraft_native === true, gridcraft: v.gridcraft_native === true }
}

/** Creates a test page holding an empty native document; its editing lease is left free. */
export async function createNative(api: APIRequestContext, ws: string, app: NativeApp): Promise<{ pageId: string; version: string }> {
  const slug = `prd-quota-${app}-${Date.now().toString(36)}`
  const c = await api.post(`/api/v1/pages?${q(ws)}`, { data: { name: `PRD quota ${app}`, slug, panels: [], allow_empty: true, created_by_test: true } })
  expect(c.status(), `create page: ${(await c.text()).slice(0, 300)}`).toBe(201)
  const pageId = ((await c.json()) as { id: string }).id
  const init = await api.post(`${craft(pageId)}initialize?${q(ws)}`, { data: { application_id: app, bytes: emptyBytes(app).toString("base64") }, timeout: 120_000 })
  expect(init.status(), `initialize ${app}: ${(await init.text()).slice(0, 300)}`).toBe(201)
  const i = (await init.json()) as { version: string; generation: string; session_token: string }
  const rel = await api.post(`${craft(pageId)}lease/release?${q(ws)}`, { data: { generation: i.generation }, headers: { "X-Craft-Session": i.session_token } })
  expect(rel.status(), "release the initialization lease").toBe(200)
  return { pageId, version: i.version }
}

/** Reads the native head and its history quota with a fresh read session. */
export async function readNative(api: APIRequestContext, ws: string, pageId: string): Promise<{ version: string; quota: NativeQuota | null }> {
  const s = await api.post(`${craft(pageId)}sessions?${q(ws)}`, { data: {} })
  expect(s.status(), "open a read session").toBe(201)
  const token = ((await s.json()) as { session_token: string }).session_token
  const r = await api.get(`${craft(pageId)}document?${q(ws)}`, { headers: { "X-Craft-Session": token, "Cache-Control": "no-cache" }, timeout: 120_000 })
  expect(r.status(), "read the native document").toBe(200)
  const d = (await r.json()) as { version: string; quota?: NativeQuota }
  return { version: d.version, quota: d.quota ?? null }
}

export const editorURL = (pageId: string, ws: string) => `/pages/craft/editor?${new URLSearchParams({ workspace_id: ws, page_id: pageId })}`
const CLICK: Record<NativeApp, { x: number; y: number }> = { wordcraft: { x: 678, y: 324 }, gridcraft: { x: 54, y: 240 } }

export async function openEditor(page: Page, pageId: string, ws: string): Promise<void> {
  await page.goto(editorURL(pageId, ws))
  await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
}

export async function statusText(page: Page): Promise<string> {
  return (await page.getByRole("status").allInnerTexts()).map((s) => s.trim()).join(" | ")
}

async function frameOf(page: Page, frameTitle: string): Promise<Frame> {
  const handle = await page.locator(`iframe[title="${frameTitle}"]`).elementHandle({ timeout: 90_000 })
  const frame = await handle!.contentFrame()
  await frame!.locator("canvas").first().waitFor({ timeout: 90_000 })
  return frame!
}

async function barrier(f: Frame) {
  await f.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))).catch(() => {})
}

/** Takes the editing lease ("Převzít úpravy") and focuses the document. */
export async function acquire(page: Page, app: NativeApp, frameTitle: string): Promise<Frame> {
  await page.getByRole("button", { name: "Převzít úpravy", exact: true }).click()
  await page.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor({ timeout: 60_000 })
  const f = await frameOf(page, frameTitle)
  await pause(800)
  await f.locator("canvas").first().click({ position: CLICK[app] })
  await barrier(f)
  return f
}

/** Types `text` as a new paragraph (Word) or into the selected cell (Sheet). */
export async function typeMarker(page: Page, f: Frame, app: NativeApp, text: string): Promise<void> {
  const kb = page.keyboard
  if (app === "wordcraft") {
    await kb.press("Control+End")
    await kb.press("Enter")
  }
  await kb.type(text)
  if (app === "gridcraft") await kb.press("Enter")
  await barrier(f)
}

/**
 * Lets the next native saves answer HTTP 413 with the server's own body
 * (internal/api/craft_native_documents.go), as a full history does. Filling
 * the real 16 GiB budget is not a reasonable test. Returns a counter.
 */
export async function refuseSavesAsFull(page: Page) {
  const seen = { saves: 0 }
  await page.route(/\/craft\/document\/(save|reconcile)\?/, async (route) => {
    seen.saves++
    await route.fulfill({ status: 413, contentType: "application/json", body: JSON.stringify({ error: "native history quota reached; existing versions retained" }) })
  })
  return seen
}
