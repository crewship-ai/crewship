// Native Word/Sheet helpers for the outage-lostack scenario: page creation from
// the product's canonical blanks, a fresh read admission for the server head,
// save/reconcile instrumentation, editor driving, and a local CONNECT proxy
// that cuts the browser's network for real (every tunnel, including the
// editor's lease event stream) instead of mocking replies.
import type { APIRequestContext, BrowserContext, Frame, Page, Request } from "@playwright/test"
import { expect } from "@playwright/test"
import { createHash } from "node:crypto"
import * as net from "node:net"
import { emptyGridCraftNativeDocument } from "../../../../lib/mooring/gridcraft-empty"
import { readFileSync } from "node:fs"

// The product's canonical blank Word (what lib/mooring/wordcraft-native-create.ts
// sends); read as a file because the test loader needs import attributes for JSON.
function createEmptyNativeWord(): Uint8Array {
  const raw = readFileSync(new URL("../../../../lib/mooring/wordcraft-native-empty.json", import.meta.url), "utf8")
  return new TextEncoder().encode(JSON.stringify(JSON.parse(raw)))
}

export type NativeApp = "wordcraft" | "gridcraft"
export const APP_LABEL: Record<NativeApp, string> = { wordcraft: "Word", gridcraft: "Sheet" }

export const sha256 = (b: Buffer | Uint8Array | string) => "sha256:" + createHash("sha256").update(b).digest("hex")
export const pause = (ms: number) => new Promise((r) => setTimeout(r, ms))

const q = (ws: string) => "?workspace_id=" + encodeURIComponent(ws)
const craft = (pageId: string, ws: string, path: string) => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/${path}${q(ws)}`

export interface NativeRef {
  pageId: string
  workspaceId: string
  initVersion: string
}

/** Creates an empty page and initializes it as a native document; releases the init lease. */
export async function createNative(api: APIRequestContext, ws: string, app: NativeApp, name: string): Promise<NativeRef> {
  const slug = `prd-outage-${app}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
  const c = await api.post(`/api/v1/pages${q(ws)}`, { data: { name, slug, panels: [], allow_empty: true, created_by_test: true } })
  expect(c.status(), `create page: ${(await c.text()).slice(0, 300)}`).toBe(201)
  const page = (await c.json()) as { id: string }
  const bytes = Buffer.from(app === "wordcraft" ? createEmptyNativeWord() : emptyGridCraftNativeDocument())
  const init = await api.post(craft(page.id, ws, "initialize"), { data: { application_id: app, bytes: bytes.toString("base64") }, timeout: 120_000 })
  expect(init.status(), `initialize ${app}: ${(await init.text()).slice(0, 300)}`).toBe(201)
  const i = (await init.json()) as { generation: string; session_token: string; version: string }
  const rel = await api.post(craft(page.id, ws, "lease/release"), { data: { generation: i.generation }, headers: { "X-Craft-Session": i.session_token } })
  expect(rel.status(), "release init lease").toBe(200)
  return { pageId: page.id, workspaceId: ws, initVersion: i.version }
}

/**
 * Whether this origin edits native documents: initializes a probe page and asks
 * its availability (an uninitialized page always reports native editing off).
 */
export async function nativeEnabled(api: APIRequestContext, ws: string): Promise<{ enabled: boolean; detail: string }> {
  const slug = `prd-outage-probe-${Date.now().toString(36)}`
  const c = await api.post(`/api/v1/pages${q(ws)}`, { data: { name: "PRD outage probe", slug, panels: [], allow_empty: true, created_by_test: true } })
  if (c.status() !== 201) return { enabled: false, detail: `create page ${c.status()}` }
  const id = ((await c.json()) as { id: string }).id
  const bytes = Buffer.from(createEmptyNativeWord())
  const init = await api.post(craft(id, ws, "initialize"), { data: { application_id: "wordcraft", bytes: bytes.toString("base64") }, timeout: 120_000 })
  if (init.status() !== 201) return { enabled: false, detail: `initialize ${init.status()} ${(await init.text()).slice(0, 120)}` }
  const i = (await init.json()) as { generation: string; session_token: string }
  await api.post(craft(id, ws, "lease/release"), { data: { generation: i.generation }, headers: { "X-Craft-Session": i.session_token } })
  const a = await api.get(craft(id, ws, "availability"))
  const body = (await a.json().catch(() => ({}))) as { enabled?: boolean }
  return { enabled: a.status() === 200 && body.enabled === true, detail: `availability ${a.status()} ${JSON.stringify(body)}` }
}

export interface Head {
  version: string
  hash: string
  text: string
}

/** Reads the native head with a fresh read admission (no lease). */
export async function readHead(api: APIRequestContext, ref: NativeRef): Promise<Head> {
  const s = await api.post(craft(ref.pageId, ref.workspaceId, "sessions"), { data: {} })
  expect(s.status(), "read admission").toBe(201)
  const tok = ((await s.json()) as { session_token: string }).session_token
  const r = await api.get(craft(ref.pageId, ref.workspaceId, "document"), { headers: { "X-Craft-Session": tok, "Cache-Control": "no-cache" }, timeout: 120_000 })
  expect(r.status(), "read document").toBe(200)
  const d = (await r.json()) as { version: string; bytes: string }
  const raw = Buffer.from(d.bytes, "base64")
  return { version: String(d.version), hash: sha256(raw), text: raw.toString("utf8") }
}

export interface Op {
  n: number
  kind: "save" | "reconcile"
  sentAt: string
  baseVersion?: string
  contentHash?: string
  status?: number
  version?: string
  phase?: string
  failed?: string
}

/** Records every native save/reconcile request of a context and its reply or failure. */
export function instrument(ctx: BrowserContext): Op[] {
  const ops: Op[] = []
  const byReq = new WeakMap<Request, Op>()
  ctx.on("request", (req) => {
    const p = new URL(req.url()).pathname
    const kind = p.endsWith("/craft/document/save") ? "save" : p.endsWith("/craft/document/reconcile") ? "reconcile" : undefined
    if (req.method() !== "POST" || !kind) return
    let body: { baseVersion?: unknown; contentHash?: string } | undefined
    try {
      body = req.postDataJSON()
    } catch {}
    const op: Op = { n: ops.length + 1, kind, sentAt: new Date().toISOString(), baseVersion: body?.baseVersion === undefined ? undefined : String(body.baseVersion), contentHash: body?.contentHash }
    ops.push(op)
    byReq.set(req, op)
  })
  ctx.on("response", (res) => {
    const op = byReq.get(res.request())
    if (!op) return
    op.status = res.status()
    void res
      .json()
      .then((b: { version?: unknown; phase?: string }) => {
        op.version = b?.version === undefined ? undefined : String(b.version)
        op.phase = b?.phase
      })
      .catch(() => {})
  })
  ctx.on("requestfailed", (req) => {
    const op = byReq.get(req)
    if (op) op.failed = req.failure()?.errorText ?? "failed"
  })
  return ops
}

export const editorURL = (ref: NativeRef) => "/pages/craft/editor?" + new URLSearchParams({ workspace_id: ref.workspaceId, page_id: ref.pageId }).toString()
const FRAME_TITLE: Record<NativeApp, string> = { wordcraft: "WordCraft editor", gridcraft: "GridCraft editor" }
// Click targets inside the engine canvas at a 1440×1000 viewport (body text / cell A1 area).
const CLICK: Record<NativeApp, { x: number; y: number }> = { wordcraft: { x: 678, y: 324 }, gridcraft: { x: 54, y: 240 } }
export const VIEWPORT = { width: 1440, height: 1000 }

export const status = (page: Page) => page.getByRole("status").first().innerText().catch(() => "?")

export async function openEditor(page: Page, ref: NativeRef): Promise<void> {
  page.setDefaultTimeout(60_000)
  await page.goto(editorURL(ref))
  await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor()
}

export async function frameOf(page: Page, app: NativeApp): Promise<Frame> {
  const h = await page.locator(`iframe[title="${FRAME_TITLE[app]}"]`).elementHandle()
  const f = await h?.contentFrame()
  if (!f) throw new Error(`${FRAME_TITLE[app]} frame not found`)
  await f.locator("canvas").first().waitFor()
  return f
}

async function barrier(page: Page, app: NativeApp) {
  const f = await frameOf(page, app)
  await f.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
}

/** Takes the edit lease through the UI and focuses the document. */
export async function acquire(page: Page, app: NativeApp): Promise<Frame> {
  await page.getByRole("button", { name: "Převzít úpravy", exact: true }).click()
  await page.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor()
  const f = await frameOf(page, app)
  await pause(800)
  await f.locator("canvas").first().click({ position: CLICK[app] })
  await barrier(page, app)
  return f
}

export async function release(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Uvolnit úpravy", exact: true }).click()
  await page.getByRole("status").filter({ hasText: "Pouze čtení" }).first().waitFor()
  await pause(500)
}

async function gotoCell(page: Page, f: Frame, app: NativeApp, cell: string) {
  const kb = page.keyboard
  for (let k = 0; k < 3; k++) {
    await kb.press("Control+PageUp")
    await barrier(page, app)
  }
  await f.locator("canvas").first().click({ position: { x: 68, y: 178 } }) // name box
  await kb.press("Control+a")
  await kb.type(cell)
  await kb.press("Enter")
  await barrier(page, app)
}

/** Types a unique marker: a new paragraph in Word, a cell value in Sheet. */
export async function typeMarker(page: Page, f: Frame, app: NativeApp, text: string, cell: string): Promise<void> {
  const kb = page.keyboard
  if (app === "wordcraft") {
    await kb.press("Control+End")
    await kb.press("Enter")
    await kb.type(text)
  } else {
    await gotoCell(page, f, app, cell)
    await kb.type(text)
    await kb.press("Enter")
  }
  await barrier(page, app).catch(() => {})
}

/** Waits for "Uloženo · verze N" and returns N. */
export async function waitSaved(page: Page, timeout = 30_000): Promise<string | undefined> {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    const m = (await status(page)).match(/^Uloženo · verze (\d+)/)
    if (m) return m[1]
    await pause(100)
  }
  return undefined
}

export async function uiState(page: Page) {
  const recoveries = await page.getByText(/^Místní koncept · verze/).allInnerTexts().catch(() => [] as string[])
  const al = (await page.getByRole("alert").allInnerTexts().catch(() => [] as string[])).map((s) => s.trim()).filter(Boolean)
  return { status: await status(page), alerts: al, recoveries, verify: (await page.getByRole("button", { name: "Ověřit výsledek", exact: true }).count()) > 0, at: new Date().toISOString() }
}

export interface Comparison {
  row: string
  caption: string
  equal: boolean
  draftHas: Record<string, boolean>
}

/** Opens "Porovnat" for every recovery row and records what the comparison shows. */
export async function compareRecoveries(page: Page, markers: Record<string, string>): Promise<Comparison[]> {
  const rows = page.getByText(/^Místní koncept · verze/)
  const out: Comparison[] = []
  const n = await rows.count()
  for (let i = 0; i < n; i++) {
    const row = rows.nth(i)
    const label = (await row.innerText()).trim()
    await row.locator("..").getByRole("button", { name: "Porovnat", exact: true }).click()
    const draftBox = page.locator('textarea[aria-label="Místní koncept"]')
    await draftBox.waitFor({ timeout: 20_000 })
    const draft = await draftBox.inputValue()
    const caption = (await page.getByText(/^Serverová verze \d+ a /).innerText()).trim()
    out.push({ row: label, caption, equal: /shodná data/.test(caption), draftHas: Object.fromEntries(Object.entries(markers).map(([k, v]) => [k, draft.includes(v)])) })
    await page.getByRole("button", { name: "Zavřít porovnání", exact: true }).click()
  }
  return out
}

export interface Proxy {
  server: string
  down(): number
  up(): void
  close(): void
}

/**
 * Local CONNECT proxy. `down()` destroys every open tunnel (including the
 * lease event stream) and refuses new ones until `up()`: a real outage for
 * the browser, while the test's own API context keeps its direct connection.
 */
export async function startProxy(): Promise<Proxy> {
  const socks = new Set<net.Socket>()
  let isDown = false
  const srv = net.createServer((c) => {
    if (isDown) return void c.destroy()
    socks.add(c)
    c.on("error", () => {})
    c.once("data", (buf) => {
      const m = buf.toString("latin1").match(/^CONNECT ([^:\s]+):(\d+) /)
      if (!m || isDown) return void c.destroy()
      const u = net.connect(Number(m[2]), m[1], () => {
        if (isDown) {
          u.destroy()
          c.destroy()
          return
        }
        c.write("HTTP/1.1 200 Connection Established\r\n\r\n")
        u.pipe(c)
        c.pipe(u)
      })
      socks.add(u)
      const rm = () => {
        socks.delete(c)
        socks.delete(u)
        c.destroy()
        u.destroy()
      }
      u.on("error", rm)
      c.on("close", rm)
      u.on("close", rm)
    })
  })
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", () => r()))
  const port = (srv.address() as net.AddressInfo).port
  return {
    server: `http://127.0.0.1:${port}`,
    down() {
      isDown = true
      const n = socks.size
      for (const s of socks) s.destroy()
      socks.clear()
      return n
    },
    up() {
      isDown = false
    },
    close() {
      srv.close()
    },
  }
}
