// Native Word/Sheet (WordCraft/GridCraft) helpers of the drafts-crash
// scenario. They drive the real editor host page; the editor UI is Czech.
import { expect, type APIRequestContext, type Frame, type Page } from "@playwright/test"
import { createHash } from "node:crypto"
import * as fs from "node:fs"
import { fileURLToPath } from "node:url"
import { emptyGridCraftNativeDocument } from "../../../../lib/mooring/gridcraft-empty"
import { pause } from "./crash"
import { trackPage } from "../created"

export type NativeApp = "wordcraft" | "gridcraft"

const sha256 = (data: Buffer | string) => "sha256:" + createHash("sha256").update(data).digest("hex")
const q = (ws: string) => `workspace_id=${encodeURIComponent(ws)}`
const craft = (pageId: string) => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/`

function emptyBytes(app: NativeApp): Buffer {
  if (app === "gridcraft") return Buffer.from(emptyGridCraftNativeDocument())
  // Same bytes as lib/mooring/wordcraft-native-create.ts (JSON.stringify of the canonical blank).
  const file = fileURLToPath(new URL("../../../../lib/mooring/wordcraft-native-empty.json", import.meta.url))
  return Buffer.from(JSON.stringify(JSON.parse(fs.readFileSync(file, "utf8"))))
}

/** Which native editors the origin offers in a workspace. */
export async function nativeAvailability(api: APIRequestContext, ws: string) {
  const res = await api.get(`/api/v1/documents/status?${q(ws)}`)
  if (!res.ok()) return { status: res.status(), wordcraft: false, gridcraft: false }
  const v = (await res.json()) as { wordcraft_native?: boolean; gridcraft_native?: boolean }
  // `enabled` is the separate document service; native craft editors are
  // advertised on their own.
  return { status: res.status(), wordcraft: v.wordcraft_native === true, gridcraft: v.gridcraft_native === true }
}

/** Creates a test page with an empty native document and leaves its lease free. */
export async function createNative(api: APIRequestContext, ws: string, app: NativeApp, tag: string): Promise<{ pageId: string; version: string }> {
  const slug = `prd-drafts-${app}-${tag}-${Date.now().toString(36)}`
  const c = await api.post(`/api/v1/pages?${q(ws)}`, { data: { name: `PRD drafts crash ${app} ${tag}`, slug, panels: [], allow_empty: true, created_by_test: true } })
  expect(c.status(), `create page: ${(await c.text()).slice(0, 300)}`).toBe(201)
  const pageId = ((await c.json()) as { id: string }).id
  trackPage(ws, pageId)
  const init = await api.post(`${craft(pageId)}initialize?${q(ws)}`, { data: { application_id: app, bytes: emptyBytes(app).toString("base64") }, timeout: 120_000 })
  expect(init.status(), `initialize ${app}: ${(await init.text()).slice(0, 300)}`).toBe(201)
  const i = (await init.json()) as { version: string; generation: string; session_token: string }
  const rel = await api.post(`${craft(pageId)}lease/release?${q(ws)}`, { data: { generation: i.generation }, headers: { "X-Craft-Session": i.session_token } })
  expect(rel.status(), "release the initialization lease").toBe(200)
  return { pageId, version: i.version }
}

/** Reads the native head with a fresh read session (no lease). */
export async function readNative(api: APIRequestContext, ws: string, pageId: string) {
  const s = await api.post(`${craft(pageId)}sessions?${q(ws)}`, { data: {} })
  expect(s.status(), "open a read session").toBe(201)
  const token = ((await s.json()) as { session_token: string }).session_token
  const r = await api.get(`${craft(pageId)}document?${q(ws)}`, { headers: { "X-Craft-Session": token, "Cache-Control": "no-cache" }, timeout: 120_000 })
  expect(r.status(), "read the native document").toBe(200)
  const d = (await r.json()) as { version: string; bytes: string }
  const raw = Buffer.from(d.bytes, "base64")
  return { version: d.version, raw: raw.toString("utf8"), hash: sha256(raw) }
}

export const editorURL = (pageId: string, ws: string) => `/pages/craft/editor?${new URLSearchParams({ workspace_id: ws, page_id: pageId })}`
const FRAME_TITLE: Record<NativeApp, string> = { wordcraft: "WordCraft editor", gridcraft: "GridCraft editor" }
const CLICK: Record<NativeApp, { x: number; y: number }> = { wordcraft: { x: 678, y: 324 }, gridcraft: { x: 54, y: 240 } }

export async function openEditor(page: Page, pageId: string, ws: string): Promise<void> {
  await page.goto(editorURL(pageId, ws))
  await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
}

export async function frameOf(page: Page, app: NativeApp): Promise<Frame> {
  const handle = await page.locator(`iframe[title="${FRAME_TITLE[app]}"]`).elementHandle({ timeout: 90_000 })
  const frame = await handle!.contentFrame()
  await frame!.locator("canvas").first().waitFor({ timeout: 90_000 })
  return frame!
}

async function barrier(page: Page, app: NativeApp) {
  const f = await frameOf(page, app)
  await f.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
}

export const takeButton = (page: Page) => page.getByRole("button", { name: "Převzít úpravy", exact: true })

export async function acquire(page: Page, app: NativeApp): Promise<Frame> {
  await takeButton(page).click()
  await page.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor({ timeout: 60_000 })
  const f = await frameOf(page, app)
  await pause(800)
  await f.locator("canvas").first().click({ position: CLICK[app] })
  await barrier(page, app)
  return f
}

export async function release(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Uvolnit úpravy", exact: true }).click()
  await page.getByRole("status").filter({ hasText: "Pouze čtení" }).first().waitFor({ timeout: 60_000 })
}

/** Types `text` as a new paragraph (Word) or into cell A{row} of the first sheet (Sheet). */
export async function typeMarker(page: Page, f: Frame, app: NativeApp, text: string, row: number): Promise<void> {
  const kb = page.keyboard
  if (app === "wordcraft") {
    await kb.press("Control+End")
    await kb.press("Enter")
    await kb.type(text)
  } else {
    for (let k = 0; k < 3; k++) {
      await kb.press("Control+PageUp")
      await barrier(page, app)
    }
    // The name box: select it, type the reference, jump there.
    await f.locator("canvas").first().click({ position: { x: 68, y: 178 } })
    await kb.press("Control+a")
    await kb.type(`A${row}`)
    await kb.press("Enter")
    await barrier(page, app)
    await kb.type(text)
    await kb.press("Enter")
  }
  await barrier(page, app).catch(() => {})
}

/** Waits for "Uloženo · verze N" and returns N. */
export async function waitSaved(page: Page, timeout = 30_000): Promise<string> {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    const s = await page.getByRole("status").first().innerText().catch(() => "")
    const m = /^Uloženo · verze (\d+)/.exec(s)
    if (m) return m[1]
    await pause(100)
  }
  throw new Error("the editor never reported a saved version")
}

export const recoveryRows = (page: Page) => page.getByText(/^Místní koncept · verze/)

/** Opens each offered draft's comparison and returns what the two sides contain. */
export async function compareDrafts(page: Page, markers: string[]) {
  const rows = await recoveryRows(page).allInnerTexts()
  const out: Array<{ row: string; caption: string; draftHash: string; serverHash: string; draft: Record<string, boolean>; server: Record<string, boolean> }> = []
  for (let i = 0; i < rows.length; i++) {
    await recoveryRows(page).nth(i).locator("..").getByRole("button", { name: "Porovnat", exact: true }).click()
    const serverBox = page.locator('textarea[aria-label="Serverová verze"]')
    await serverBox.waitFor({ timeout: 30_000 })
    const server = await serverBox.inputValue()
    const draft = await page.locator('textarea[aria-label="Místní koncept"]').inputValue()
    const has = (text: string) => Object.fromEntries(markers.map((m) => [m, text.includes(m)]))
    out.push({ row: rows[i], caption: await page.getByText(/^Serverová verze \d+ a /).innerText(), draftHash: sha256(draft), serverHash: sha256(server), draft: has(draft), server: has(server) })
    await page.getByRole("button", { name: "Zavřít porovnání", exact: true }).click()
  }
  return out
}

/** Status, alerts and the lease/recovery buttons visible on the editor host. */
export async function uiState(page: Page) {
  const buttons = await page.locator("button").evaluateAll((bs) => bs.map((b) => (b.textContent ?? "").trim()).filter((t) => t && t.length < 60)).catch(() => [] as string[])
  return {
    status: await page.getByRole("status").first().innerText().catch(() => "?"),
    alerts: (await page.getByRole("alert").allInnerTexts().catch(() => [] as string[])).filter(Boolean),
    recoveries: await recoveryRows(page).allInnerTexts().catch(() => [] as string[]),
    buttons: buttons.filter((t) => /Převzít|Uvolnit|Předat|Požádat|Ověřit|Porovnat|Kopie|Zavřít|Stáhnout/.test(t)),
  }
}
