// Shared steps of the client's path through a Sheet: a new chat, a workbook
// through the paperclip, the Sheet beside the chat, editing, the stored file.
// The server is a throwaway one started by scripts/ci/sheet-client-e2e.sh;
// CRAFT_GATE_* name it, its demo account and its agent.
import { expect, test, type APIRequestContext, type Browser, type BrowserContext, type Frame, type Locator, type Page } from "@playwright/test"
import { createHash } from "node:crypto"
import { writeFile } from "node:fs/promises"
import { apiAs, loginStorageState, type StorageState } from "../acceptance/prd/auth"
import * as N from "../acceptance/prd/outage-lostack/native"
import { storedWorkbook, type FileHead } from "../craft-packages/workbook-file"
import { XLSX } from "./workbooks"

export const ORIGIN = process.env.CRAFT_GATE_ORIGIN ?? ""
export const WORKSPACE = process.env.CRAFT_GATE_WORKSPACE ?? ""
export const AGENT = process.env.CRAFT_GATE_AGENT ?? ""
const ACCOUNT = { email: process.env.CRAFT_GATE_EMAIL ?? "", password: process.env.CRAFT_GATE_PASSWORD ?? "" }
export const LONG = 4 * 60_000

export const sha = (b: Buffer) => createHash("sha256").update(b).digest("hex")

/**
 * A fresh sign-in for every browser or API context. One shared sign-in let two
 * contexts refresh the same refresh token: the second refresh is a replay, the
 * server revokes the session and the page signs out mid-test.
 */
export async function ownerState(): Promise<StorageState> {
  expect(ORIGIN && WORKSPACE && ACCOUNT.email && ACCOUNT.password, "CRAFT_GATE_ORIGIN, _WORKSPACE, _EMAIL and _PASSWORD are required").toBeTruthy()
  return loginStorageState(ORIGIN, ACCOUNT)
}

/** Console lines of the editor and host that name native_ or gridcraft, with time and page, for diagnostics. */
export const consoleLog: Array<{ at: number; url: string; type: string; text: string }> = []

export async function clientContext(browser: Browser, options: Parameters<Browser["newContext"]>[0] = {}): Promise<BrowserContext> {
  const ctx = await browser.newContext({ baseURL: ORIGIN, storageState: await ownerState(), locale: "en-US", timezoneId: "Europe/Prague", viewport: { width: 1440, height: 900 }, acceptDownloads: true, ...options })
  // Diagnostics: every window message the host page receives (its type only, never its content),
  // so a click in the grid can be followed to the editor's edit attempt. A message over a
  // MessageChannel port does not pass here; the report says so.
  await ctx.addInitScript(() => {
    const w = window as unknown as { __craftMsgs?: Array<{ at: number; type: string; origin: string }> }
    w.__craftMsgs = []
    window.addEventListener("message", e => {
      try {
        const d = e.data as Record<string, unknown> | string | null
        const type = d && typeof d === "object" ? String(d.type ?? d.kind ?? d.event ?? Object.keys(d).slice(0, 4).join(",")) : typeof d
        w.__craftMsgs!.push({ at: Date.now(), type: type.slice(0, 80), origin: e.origin })
        if (w.__craftMsgs!.length > 500) w.__craftMsgs!.splice(0, 100)
      } catch { /* a message we cannot read */ }
    }, true)
    // The same over MessageChannel ports, in every frame: what this frame sends and what it
    // receives (type, kind and access fields only), so a lost gridcraft:edit-attempt can be placed.
    type PortLog = Array<{ at: number; dir: "sent" | "received"; type: string; kind?: string; access?: string }>
    const p = window as unknown as { __craftPort?: PortLog }
    p.__craftPort = []
    const note = (dir: "sent" | "received", d: unknown) => {
      try {
        if (!d || typeof d !== "object") return
        const r = d as Record<string, unknown>
        const type = String(r.type ?? "")
        if (!/^(gridcraft|wordcraft|craft|native)[:_-]/.test(type)) return
        p.__craftPort!.push({ at: Date.now(), dir, type: type.slice(0, 60), kind: r.kind === undefined ? undefined : String(r.kind).slice(0, 30), access: r.access === undefined && r.readOnly === undefined ? undefined : String(r.access ?? `readOnly=${String(r.readOnly)}`).slice(0, 30) })
        if (p.__craftPort!.length > 800) p.__craftPort!.splice(0, 200)
      } catch { /* not readable */ }
    }
    const proto = MessagePort.prototype
    const send = proto.postMessage
    proto.postMessage = function (this: MessagePort, message: unknown, ...rest: unknown[]) { note("sent", message); return (send as (...a: unknown[]) => void).call(this, message, ...rest) } as typeof proto.postMessage
    const add = proto.addEventListener
    proto.addEventListener = function (this: MessagePort, type: string, listener: unknown, ...rest: unknown[]) {
      if (type === "message" && typeof listener === "function") {
        const fn = listener as (e: MessageEvent) => void
        return (add as (...a: unknown[]) => void).call(this, type, function (this: unknown, e: MessageEvent) { note("received", e.data); return fn.call(this, e) }, ...rest)
      }
      return (add as (...a: unknown[]) => void).call(this, type, listener, ...rest)
    } as typeof proto.addEventListener
    const on = Object.getOwnPropertyDescriptor(proto, "onmessage")
    if (on?.set && on.get) Object.defineProperty(proto, "onmessage", {
      configurable: true, get: on.get,
      set(this: MessagePort, fn: ((e: MessageEvent) => void) | null) { on.set!.call(this, fn ? (e: MessageEvent) => { note("received", e.data); fn.call(this, e) } : null) },
    })
  })
  ctx.on("console", msg => {
    const text = msg.text()
    if (/native_|gridcraft|edit-attempt|editAttempt/i.test(text)) consoleLog.push({ at: Date.now(), url: msg.page()?.url() ?? "", type: msg.type(), text: text.slice(0, 300) })
  })
  return ctx
}

/** What happened after a click in the grid: the take-over notice, the messages and console lines since. */
async function clickDiagnostics(page: Page, since: number) {
  const notices = await page.locator("[data-take-over-notice]").evaluateAll(els => els.map(e => e.getAttribute("data-take-over-notice") ?? ""))
  // data-edit-attempt: '' = editAttempt not called, 'taking', or 'ignored:<reason>'.
  const editAttempt0 = await page.locator("[data-take-over-notice]").evaluateAll(els => els.map(e => e.hasAttribute("data-edit-attempt") ? e.getAttribute("data-edit-attempt") ?? "" : "(no attribute)"))
  const messages = await page.evaluate(t => ((window as unknown as { __craftMsgs?: Array<{ at: number; type: string; origin: string }> }).__craftMsgs ?? []).filter(m => m.at >= t), since)
  const editAttempt = messages.filter(m => /edit-attempt|editAttempt|edit_attempt/i.test(m.type))
  // Port traffic since the click, per frame (the host page and the editor's frame).
  const port: Record<string, string[]> = {}
  for (const f of page.frames()) {
    const rows = await f.evaluate(t => ((window as unknown as { __craftPort?: Array<{ at: number; dir: string; type: string; kind?: string; access?: string }> }).__craftPort ?? []).filter(m => m.at >= t - 2_000), since).catch(() => [])
    if (rows.length) port[f === page.mainFrame() ? "host" : (new URL(f.url() || "about:blank").pathname.slice(0, 60) || "frame")] = rows.map(m => `${m.at - since}ms ${m.dir} ${m.type}${m.kind ? ` kind=${m.kind}` : ""}${m.access ? ` ${m.access}` : ""}`).slice(0, 40)
  }
  return { port, portEditAttemptSent: Object.values(port).flat().filter(l => / sent gridcraft:edit-attempt/.test(l)).length, portEditAttemptReceived: Object.values(port).flat().filter(l => / received gridcraft:edit-attempt/.test(l)).length, takeOverNotice: notices, editAttempt: editAttempt0, editAttemptMessages: editAttempt.length, messageTypesSinceClick: [...new Set(messages.map(m => `${m.type} (${m.origin})`))], console: consoleLog.filter(c => c.at >= since) }
}

/** The same click on the Sheet's Artifacts page: does it take over editing there? Hands editing back after. */
async function clickOnArtifactsPage(page: Page): Promise<Record<string, unknown>> {
  await openMenu(page)
  const href = await page.getByRole("menuitem", { name: "Version history" }).getAttribute("href")
  await page.keyboard.press("Escape")
  const other = await page.context().newPage()
  try {
    await other.goto(href ?? "")
    const frame = await N.frameOf(other, "gridcraft")
    await expect(other.getByText(/Viewing|Read-only|Free/).first()).toBeVisible({ timeout: LONG }).catch(() => undefined)
    await other.waitForTimeout(2_000)
    const at = Date.now()
    await frame.locator("canvas").first().click({ position: { x: 300, y: 260 } })
    const taken = await other.locator('[data-take-over-notice="taking"], [data-take-over-notice="taken"]').or(other.getByText(/You are editing now\.|Taking over editing…/)).first()
      .waitFor({ timeout: 15_000 }).then(() => true, () => false)
    const result = { url: new URL(other.url()).pathname, clickTookOver: taken, ...(await clickDiagnostics(other, at)) }
    await other.screenshot({ path: test.info().outputPath("shots", "click-on-artifacts-page.png") })
    if (taken) {
      await other.getByRole("button", { name: "Hand back" }).first().click({ timeout: 30_000 }).catch(() => undefined)
      await other.getByText(/Read-only|Free/).first().waitFor({ timeout: 30_000 }).catch(() => undefined)
    }
    return result
  } finally {
    await other.close()
    await page.bringToFront()
  }
}

export async function ownerApi(): Promise<APIRequestContext> { return apiAs(ORIGIN, await ownerState()) }

export function requireAgent() {
  test.skip(!AGENT, "CRAFT_GATE_AGENT (an agent slug the account can chat with) is not set")
}

/** A new chat that has sent nothing yet, as a client starts. */
export async function newChat(page: Page) {
  await page.goto(`/chat/${encodeURIComponent(AGENT)}?new=1&workspace_id=${encodeURIComponent(WORKSPACE)}`)
  await expect(page.getByPlaceholder(/^Message /)).toBeVisible({ timeout: 60_000 })
}

/** Through the paperclip, as a person does. */
export async function attach(page: Page, name: string, buffer: Buffer) {
  const chooser = page.waitForEvent("filechooser")
  await page.getByRole("button", { name: "Attach files" }).click()
  await (await chooser).setFiles({ name, mimeType: XLSX, buffer })
}

export const panelOf = (page: Page) => page.getByRole("region", { name: "Sheet editor" })
export const stateOf = (page: Page) => panelOf(page).getByTestId("sheet-panel-state")
export const barOf = (page: Page) => panelOf(page).getByTestId("sheet-panel-bar")

/** The Sheet's page id, from the Version history link in the bar's menu. */
/**
 * Opens the bar's ⋯ menu as a person with a pointer does. When something lies over the
 * button (seen: Take over editing and Expand over it at 1440 px), that is
 * recorded as a soft failure with a screenshot, and the menu is opened from the
 * keyboard so the rest of the path is still tested.
 */
export async function openMenu(page: Page) {
  const button = panelOf(page).getByRole("button", { name: "More for this Sheet" })
  const clicked = await button.click({ timeout: 5_000 }).then(() => true, () => false)
  expect.soft(clicked, "the ⋯ menu of the Sheet bar can be clicked (nothing covers it)").toBe(true)
  if (!clicked) {
    await page.screenshot({ path: test.info().outputPath("shots", `menu-covered-${Date.now()}.png`) })
    test.info().annotations.push({ type: "finding", description: "the ⋯ menu (More for this Sheet) is covered in the bar beside the chat; opened from the keyboard instead" })
    await button.focus()
    await page.keyboard.press("Enter")
  }
  await expect(page.getByRole("menu")).toBeVisible({ timeout: 10_000 })
}

export async function pageIdOf(page: Page): Promise<string> {
  await openMenu(page)
  const href = await page.getByRole("menuitem", { name: "Version history" }).getAttribute("href")
  await page.keyboard.press("Escape")
  const id = decodeURIComponent(new URL(href ?? "", ORIGIN).pathname.split("/")[2] ?? "")
  expect(id, "page id of the Sheet beside the chat").toBeTruthy()
  return id
}

/** Waits until the Sheet beside the chat shows a version, and says how long it took from `since`. */
export async function waitViewing(page: Page, since: number): Promise<number> {
  await expect(panelOf(page)).toBeVisible({ timeout: LONG })
  await expect(stateOf(page)).toHaveText(/(Viewing|Saved) · version \d+/, { timeout: LONG })
  return Date.now() - since
}

export async function gridFrame(page: Page): Promise<Frame> { return N.frameOf(page, "gridcraft") }

/**
 * Takes editing with a click in the grid, as a client does. A workbook with parts
 * the editor keeps as they are first asks; the client edits anyway.
 */
export async function startEditing(page: Page): Promise<Frame> {
  const frame = await gridFrame(page)
  const ask = page.getByRole("alertdialog", { name: "Before you edit this workbook" })
  const editing = page.getByText(/You are editing now\.|Taking over editing…/).first()
  const taken = page.locator('[data-take-over-notice="taking"], [data-take-over-notice="taken"]')
  const started = ask.or(editing).or(taken).first()
  const clickedAt = Date.now()
  await frame.locator("canvas").first().click({ position: { x: 300, y: 260 } })
  const byClick = await started.waitFor({ timeout: 15_000 }).then(() => true, () => false)
  // A click in a cell should take over editing (the same action as the button).
  expect.soft(byClick, "a click in the read-only grid takes over editing").toBe(true)
  if (!byClick) {
    await shot(page, "click-did-not-take-over")
    // Diagnostics for the owner of the code: (a) the notice, (b) the edit attempt, (c) the Artifacts page.
    const panel = await clickDiagnostics(page, clickedAt)
    // The same click again after 10 s settled: does the panel take over once it has finished opening?
    await page.waitForTimeout(10_000)
    const againAt = Date.now()
    await (await gridFrame(page)).locator("canvas").first().click({ position: { x: 300, y: 260 } })
    const again = await started.waitFor({ timeout: 15_000 }).then(() => true, () => false)
    const later = { clickTookOver: again, ...(await clickDiagnostics(page, againAt)) }
    const artifacts = await clickOnArtifactsPage(page).catch(e => ({ error: String(e).slice(0, 300) }))
    const diag = { besideTheChat: panel, besideTheChatAfter10s: later, artifactsPage: artifacts, note: "port: MessageChannel traffic per frame since the click (ms from the click); messageTypesSinceClick: window messages only" }
    await writeFile(test.info().outputPath("click-diagnostics.json"), JSON.stringify(diag, null, 2))
    test.info().annotations.push({ type: "click diagnostics", description: JSON.stringify(diag).slice(0, 6000) })
    // The Artifacts page was another tab: this one gets the keyboard back first.
    await page.bringToFront()
    if (!later.clickTookOver) await panelOf(page).getByRole("button", { name: "Take over editing" }).click()
  }
  await expect(started, "taking over editing starts").toBeVisible({ timeout: 60_000 }).catch(() => undefined)
  if (await ask.isVisible()) {
    await ask.getByRole("button", { name: "Edit anyway" }).click()
  }
  await expect(stateOf(page)).toHaveText(/Editing|Saved · version \d+|Not saved yet/, { timeout: 60_000 })
  // Taking over may mount the editor afresh (a new frame): never type into the old one.
  void frame
  return gridFrame(page)
}

/** Types a value into a cell of the FIRST sheet and commits it. */
export async function setCell(page: Page, _frame: Frame, address: string, value: string) {
  // The frame now, not the one from before a take-over.
  const f = await gridFrame(page)
  const focused = (tag: string) => f.waitForFunction(t => document.activeElement?.tagName === t, tag, { timeout: 30_000 })
  // To the first sheet.
  for (let k = 0; k < 3; k++) { await page.keyboard.press("Control+PageUp"); await page.waitForTimeout(200) }
  // The name box, then the formula bar: in the narrow panel beside the chat the
  // grid does not take the keyboard back after the name box, so the value goes
  // through the formula bar (same row, right of fx), which commits with Enter.
  await f.locator("canvas").first().click({ position: { x: 68, y: 178 } })
  await focused("INPUT")
  await page.keyboard.press("Control+a")
  await page.keyboard.type(address)
  await page.keyboard.press("Enter")
  await page.waitForTimeout(500)
  await f.locator("canvas").first().click({ position: { x: 300, y: 178 } })
  await focused("INPUT")
  await page.keyboard.press("Control+a")
  await page.keyboard.type(value)
  await page.keyboard.press("Enter")
  await page.waitForTimeout(500)
}

/** Waits for "Saved · version N" newer than `after`, and returns N. */
export async function waitSavedAfter(page: Page, after: string): Promise<string> {
  await expect.poll(async () => {
    const m = /Saved · version (\d+)/.exec(await stateOf(page).innerText())
    return m && BigInt(m[1]) > BigInt(after) ? m[1] : ""
  }, { timeout: LONG, intervals: [250, 500, 1000] }).not.toBe("")
  return /Saved · version (\d+)/.exec(await stateOf(page).innerText())![1]
}

export function head(api: APIRequestContext, pageId: string, version?: string): Promise<FileHead> {
  return storedWorkbook(api, WORKSPACE, pageId, version)
}

/** The versions the server holds for the page, newest first (numbers only). */
export async function versions(api: APIRequestContext, pageId: string): Promise<string[]> {
  const q = `?workspace_id=${encodeURIComponent(WORKSPACE)}`
  const base = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/`
  const s = await api.post(`${base}sessions${q}`, { data: {} })
  expect(s.status(), "read admission").toBe(201)
  const token = ((await s.json()) as { session_token: string }).session_token
  const r = await api.get(`${base}document/versions${q}`, { headers: { "X-Craft-Session": token } })
  expect(r.status(), "list versions").toBe(200)
  const body = (await r.json()) as { versions?: Array<{ version: string | number }> } | Array<{ version: string | number }>
  const list = Array.isArray(body) ? body : body.versions ?? []
  return list.map(v => String(v.version))
}

export async function deletePage(api: APIRequestContext, pageId: string) {
  if (!pageId) return
  await api.delete(`/api/v1/pages/${encodeURIComponent("@" + pageId)}?workspace_id=${encodeURIComponent(WORKSPACE)}`).catch(() => undefined)
}

/** One choice of the bar's ⋯ menu, as a download the client saves. */
export async function downloadFromMenu(page: Page, item: RegExp | string): Promise<{ name: string; bytes: Buffer }> {
  await openMenu(page)
  const file = page.waitForEvent("download", { timeout: LONG })
  await page.getByRole("menuitem", { name: item }).click()
  const done = await file
  const path = test.info().outputPath("downloads", `${Date.now()}-${done.suggestedFilename()}`)
  await done.saveAs(path)
  const { readFile } = await import("node:fs/promises")
  return { name: done.suggestedFilename(), bytes: await readFile(path) }
}

export async function shot(page: Page | Locator, name: string) {
  const path = test.info().outputPath("shots", `${name}.png`)
  await page.screenshot({ path })
  await test.info().attach(name, { path, contentType: "image/png" })
}

/** Timings go to the test's annotations and to timings.json beside its output. */
export async function recordTimings(timings: Record<string, number | string>) {
  for (const [k, v] of Object.entries(timings)) test.info().annotations.push({ type: `timing ${k}`, description: String(v) })
  await writeFile(test.info().outputPath("timings.json"), JSON.stringify(timings, null, 2))
}
