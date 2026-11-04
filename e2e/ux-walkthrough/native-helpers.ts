// Native Word/Sheet helpers shared by the walkthrough specs.
import type { Page } from "@playwright/test"
import { emptyDocument } from "../acceptance/prd/craft-lease/native"
import { readNative } from "../acceptance/prd/drafts-crash/native"
import { audit, pause } from "./audit"
import type { Viewport } from "./audit"
import type { ownerApi } from "./session"

export const FRAME = { wordcraft: "WordCraft editor", gridcraft: "GridCraft editor" } as const
export const toolbar = (p: Page) => p.getByRole("toolbar", { name: "Úpravy dokumentu" })
export const saveStatus = '[data-testid="native-editor"] [role="status"]'

export async function createFromLibrary(page: Page, ws: string, kind: "Word document" | "Sheet", name: string, vp: Viewport, tag: string): Promise<string> {
  await page.goto(`/artifacts?workspace_id=${ws}`)
  await page.getByRole("button", { name: "New artifact" }).first().click({ timeout: 30_000 })
  const dialog = page.getByRole("dialog")
  await dialog.getByRole("group", { name: "Kind" }).getByRole("button", { name: new RegExp(kind) }).click()
  await dialog.getByLabel("Name").fill(name)
  await pause(800)
  await audit(page, `${tag}-new-dialog-${vp}`, vp)
  await dialog.getByRole("button", { name: /Create and edit|Retry initialization/ }).click()
  await page.waitForURL(/\/artifacts\/[^/?]+/, { timeout: 120_000 })
  const id = new URL(page.url()).pathname.split("/").pop()!
  return id
}

/** API path for a native Word when the library does not offer it (same calls as the PRD acceptance helpers). */
export async function createNativeViaApi(api: Awaited<ReturnType<typeof ownerApi>>, ws: string, app: "wordcraft" | "gridcraft", name: string): Promise<string> {
  const q = `workspace_id=${encodeURIComponent(ws)}`
  const c = await api.post(`/api/v1/pages?${q}`, { data: { name, slug: `prd-ux-walk-${app}-${Date.now().toString(36)}`, panels: [], allow_empty: true } })
  if (c.status() !== 201) throw new Error(`create page ${c.status()}`)
  const pageId = ((await c.json()) as { id: string }).id
  const craft = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/`
  const init = await api.post(`${craft}initialize?${q}`, { data: { application_id: app, bytes: emptyDocument(app).toString("base64") }, timeout: 120_000 })
  if (init.status() !== 201) throw new Error(`initialize ${init.status()} ${(await init.text()).slice(0, 200)}`)
  const i = (await init.json()) as { generation: string; session_token: string }
  await api.post(`${craft}lease/release?${q}`, { data: { generation: i.generation }, headers: { "X-Craft-Session": i.session_token } })
  return pageId
}

export async function waitEditor(page: Page): Promise<number> {
  const t0 = Date.now()
  await page.getByTestId("native-editor").waitFor({ timeout: 120_000 })
  await page.locator(saveStatus).filter({ hasText: /Pouze čtení|Připraveno k úpravám|Uloženo|Neuložený/ }).first().waitFor({ timeout: 120_000 })
  return Date.now() - t0
}

export async function headIncludes(api: Awaited<ReturnType<typeof ownerApi>>, ws: string, pageId: string, text: string) {
  const head = await readNative(api, ws, pageId)
  return { version: head.version, includes: head.raw.includes(text) }
}

export async function clickCanvas(page: Page, app: keyof typeof FRAME, at: "center" | "cell" = "center") {
  const frame = page.frameLocator(`iframe[title="${FRAME[app]}"]`)
  const canvas = frame.locator("canvas").first()
  const box = await canvas.boundingBox()
  if (!box) throw new Error("canvas has no box")
  const pos = at === "center" ? { x: box.width / 2, y: Math.min(box.height / 2, 260) } : { x: 54, y: 240 }
  await canvas.click({ position: pos })
}

