// What happens to an unsaved draft when access changes, in real Chromium against a
// real server, for Word, Sheet and Project. Delivery, the client's reaction and
// reconnect are timed apart, because a test that only sees the final state cannot
// tell them apart:
//
//   T0 the server committed the change (the owner's call returned)
//   T1 delivery: the browser learned it (the stream for a native editor, the
//      refused read for a Project)
//   T2 reaction: content hidden, and the draft's rows erased or still there
//   T3 reconnect: the browser went back online or back to the tab, and acted on
//      what it learned
//
// Run it with `pnpm acceptance:draft-revocation`, which starts everything this
// needs and fails on any skipped case: "Draft revocation proof" in
// docs/development/prd-acceptance.md. Never point it at a shared server: it
// rewrites the server's database and cuts its connections.
import { chromium, request as requests, type Browser, type BrowserContext, type Locator, type Page, type Route } from "@playwright/test"
import { execFileSync, spawn } from "node:child_process"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { acquire, compareDrafts, editorURL, frameOf, readNative, recoveryRows, takeButton, typeMarker, waitSaved } from "../prd/drafts-crash/native"
import { createProject, ensureArtifactProjects, pause, prepareSourceDraft, projectOffer, projectURL, readArtifact, shareProject } from "../prd/drafts-crash/crash"
import { peerApi, requireNative } from "../prd/craft-lease/helpers"
import { APPLICATIONS, NativePage, type NativeApplication } from "../prd/craft-lease/native"
import { expect, prdRequirement, test } from "../prd/fixtures"
import type { StorageState } from "../prd/auth"
import { compareImages, decodePng, measureImage } from "./pixels.mjs"
import { freeze, painted, recordTimeline, sinceReturn, thaw } from "./timeline"

const BOUND = {
  deliveryMs: 7_000, // the 5 s event interval plus delivery
  reactionMs: 3_000,
  reconnectMs: 60_000, // NATIVE_ADMISSION_DEADLINE_MS
  projectPollMs: 17_000, // the Project's 15 s poll plus delivery
  /** From a tab's return until a change made while it was away has taken effect. */
  returnMs: 5_000,
}
const REFUSED = /Právo upravovat bylo odebráno/
const LEVELS = ["read", "comment"] as const
/** Share of the area that is not background: below this it is blank or one colour. */
const MIN_CONTENT = 0.0005
/** Share of what was on screen before that may be gone: a caret, a selection. */
const MAX_LOST = 0.2
/** Past five minutes Chromium throttles hidden timers to once a minute. */
const HIDDEN_MS = Number(process.env.REVOCATION_HIDDEN_MS ?? 330_000)

/** Records every frame of the host's own metadata stream as it reaches the page. */
/** Logs the engines' messages, and can make a snapshot answer late, as a slow export would. */
const watchEngine = () => {
  if (window !== window.top) return
  const watch = { log: [] as Array<{ t: number; type: string; revision?: unknown }>, late: 0 }
  ;(window as unknown as { engineWatch: typeof watch }).engineWatch = watch
  const seen = (type: string, revision?: unknown) => watch.log.push({ t: Math.round(performance.now()), type, revision })
  const native = Object.getOwnPropertyDescriptor(MessagePort.prototype, "onmessage")!
  Object.defineProperty(MessagePort.prototype, "onmessage", { ...native, set(this: MessagePort, handler: ((event: MessageEvent) => void) | null) {
    native.set!.call(this, handler && ((event: MessageEvent) => {
      const type = (event.data as { type?: unknown } | null)?.type
      if (typeof type === "string") seen(type, (event.data as { revision?: unknown }).revision)
      if (watch.late && typeof type === "string" && /snapshot-result|native-exported/.test(type)) setTimeout(() => handler(event), watch.late)
      else handler(event)
    }))
  } })
  const post = MessagePort.prototype.postMessage
  MessagePort.prototype.postMessage = function (this: MessagePort, ...args: unknown[]) {
    const type = (args[0] as { type?: unknown } | null)?.type
    if (typeof type === "string") seen(`out ${type}`)
    return (post as (...values: unknown[]) => void).apply(this, args)
  }
}
const engineLog = (page: Page) => page.evaluate(() => (window as unknown as { engineWatch: { log: unknown[] } }).engineWatch.log)
const lateSnapshots = (page: Page, ms: number) => page.evaluate((value) => { (window as unknown as { engineWatch: { late: number } }).engineWatch.late = value }, ms)

const recordStream = () => {
  const state = { frames: [] as Array<{ at: number; event: string; data: string }> }
  ;(window as unknown as { __stream: typeof state }).__stream = state
  const original = window.fetch
  window.fetch = async (...args) => {
    const response = await original(...args)
    const url = String((args[0] as Request)?.url ?? args[0])
    if (!url.includes("/craft/events") || !response.body) return response
    const [mine, theirs] = response.body.tee()
    void (async () => {
      const reader = mine.getReader(), decoder = new TextDecoder()
      let buffer = ""
      for (;;) {
        const { done, value } = await reader.read()
        if (done) return
        buffer += decoder.decode(value, { stream: true })
        for (let end = buffer.indexOf("\n\n"); end >= 0; end = buffer.indexOf("\n\n")) {
          const block = buffer.slice(0, end)
          buffer = buffer.slice(end + 2)
          const event = /^event: (.+)$/m.exec(block)?.[1]
          if (event) state.frames.push({ at: Date.now(), event, data: /^data: (.+)$/m.exec(block)?.[1] ?? "" })
        }
      }
    })().catch(() => {})
    return new Response(theirs, { status: response.status, statusText: response.statusText, headers: response.headers })
  }
}

const retired = (page: Page) => page.evaluate(() => {
  const frames = (window as unknown as { __stream?: { frames: Array<{ at: number; event: string; data: string }> } }).__stream?.frames ?? []
  return frames.filter((f) => f.event === "session_retired").map((f) => ({ at: f.at, ...JSON.parse(f.data) as { draft?: string; reason?: string } }))
})

/** Rows of this Page in a draft store. Sealed: only their count is read. */
const storeRows = (page: Page, name: string, pageId: string) => page.evaluate(([database, id]) => new Promise<number>((resolve, reject) => {
  const open = indexedDB.open(database)
  open.onerror = () => reject(open.error)
  open.onsuccess = () => {
    const db = open.result
    if (!db.objectStoreNames.length) { db.close(); resolve(0); return }
    const all = db.transaction(db.objectStoreNames[0]).objectStore(db.objectStoreNames[0]).getAllKeys()
    all.onerror = () => { db.close(); reject(all.error) }
    all.onsuccess = () => { db.close(); resolve(all.result.filter((k) => String(k).includes(id)).length) }
  }
}), [name, pageId])
const draftRows = (page: Page, pageId: string) => storeRows(page, "crewship-document-drafts", pageId)
const projectRows = (page: Page, pageId: string) => storeRows(page, "crewship-artifact-drafts", pageId)

/** The rows' opaque ids and revisions: which row is which, never what it holds. */
const rowIds = (page: Page, pageId: string) => page.evaluate((id) => new Promise<string[]>((resolve) => {
  const open = indexedDB.open("crewship-document-drafts")
  open.onsuccess = () => {
    const db = open.result
    const all = db.transaction("drafts").objectStore("drafts").getAll()
    all.onsuccess = () => { db.close(); resolve(all.result.filter((r) => String(r.key).includes(id)).map((r) => `${String(r.key[1]).slice(-8)}/${String(r.revision).slice(-8)}/${r.draft ? "draft" : "empty"}`)) }
  }
}), pageId)

/** No editor frame on screen: gone, hidden, or fully transparent. */
const contentHidden = (page: Page) => page.evaluate(() => {
  const frame = document.querySelector<HTMLElement>('iframe[title$=" editor"]')
  if (!frame) return true
  const style = getComputedStyle(frame)
  return style.visibility === "hidden" || style.opacity === "0"
})

/** Waits for a condition and returns when it first held. */
async function until(what: string, check: () => Promise<boolean>, timeoutMs: number): Promise<number> {
  const end = Date.now() + timeoutMs
  for (;;) {
    if (await check().catch(() => false)) return Date.now()
    if (Date.now() > end) throw new Error(`${what}: not within ${timeoutMs} ms`)
    await pause(50)
  }
}

interface Fixture { context: BrowserContext; page: Page; doc: NativePage; peer: { userId: string; account: { email: string; password: string } }; workspace: string; state: StorageState; slug?: string; marker: string; releaseSaves: () => Promise<void> }
type Prd = Parameters<Parameters<typeof test>[2]>[0]["prd"]
type Api = Parameters<Parameters<typeof test>[2]>[0]["request"]

/** How a case gets its browser context for a person's session. */
type Contexts = (state: StorageState) => Promise<BrowserContext>
const VIEWPORT = { width: 1440, height: 1000 }
const contextsOf = (prd: Prd, browser: Browser): Contexts => (state) => browser.newContext({ baseURL: prd.origin, storageState: state, viewport: VIEWPORT })

/** A headed full Chromium, started by hand and attached with `noDefaults`, is the
 * only browser here whose background tab really hides. The headless shell never
 * does, and every page Playwright launches itself runs with focus emulation, which
 * keeps it visible behind any other tab. Background throttling stays on, because
 * that is what these cases measure. Needs a display: the runner provides Xvfb. */
function fullChromium(prd: Prd, cleanup: Array<() => Promise<void>>): Contexts {
  const executable = process.env.REVOCATION_CHROME
  expect(executable && process.env.DISPLAY, "a hidden tab needs REVOCATION_CHROME and a display").toBeTruthy()
  return async (state) => {
    const profile = fs.mkdtempSync(path.join(os.tmpdir(), "draft-revocation-profile-"))
    const chrome = spawn(executable!, [
      "--remote-debugging-port=0", `--user-data-dir=${profile}`, "--no-first-run", "--no-default-browser-check", "--no-sandbox", "--window-size=1460,1150",
      "--enable-unsafe-swiftshader", "--disable-features=ThrottleDisplayNoneAndVisibilityHiddenCrossOriginIframes", "about:blank",
    ], { stdio: ["ignore", "ignore", "pipe"] })
    const stopped = new Promise((resolve) => chrome.once("exit", resolve))
    cleanup.push(async () => { chrome.kill(); await stopped; fs.rmSync(profile, { recursive: true, force: true }) })
    const endpoint = await new Promise<string>((resolve, reject) => {
      let output = ""
      const timer = setTimeout(() => reject(new Error("full Chromium did not start")), 30_000)
      chrome.stderr.on("data", (chunk: Buffer) => {
        output += chunk.toString()
        const found = /DevTools listening on (ws:\S+)/.exec(output)?.[1]
        if (found) { clearTimeout(timer); resolve(found) }
      })
    })
    const browser = await chromium.connectOverCDP(endpoint, { noDefaults: true })
    const context = browser.contexts()[0]
    await context.addCookies(state.cookies)
    await context.addInitScript(recordTimeline)
    return context
  }
}

/** Hides `page` behind a new tab and returns that tab. Fails rather than skips:
 * a passing run must have seen a hidden tab. */
async function hide(f: Fixture): Promise<Page> {
  const front = await f.context.newPage()
  await front.goto("about:blank")
  await front.bringToFront()
  await until("the tab hides", async () => (await f.page.evaluate(() => document.visibilityState)) === "hidden", 10_000)
  return front
}

/** Opens a tab of the size every case assumes. An attached browser has neither a
 * viewport nor a base URL of its own, so URLs here are absolute. */
async function newTab(context: BrowserContext): Promise<Page> {
  const page = await context.newPage()
  page.setDefaultTimeout(60_000)
  await page.setViewportSize(VIEWPORT)
  return page
}

/** Opens the editor in a tab of `context` and records its stream. */
async function open(context: BrowserContext, origin: string, doc: NativePage, workspace: string): Promise<Page> {
  const page = await newTab(context)
  await page.goto(new URL(editorURL(doc.pageId, workspace), origin).href)
  await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
  return page
}

/** A person with `levels` holds the editor and has typed work no save can confirm. */
async function holding(application: NativeApplication, prd: Prd, request: Api, cleanup: Array<() => Promise<void>>, levels: Array<"read" | "comment" | "write">, contexts: Contexts): Promise<Fixture> {
  const ws = await prd.qaWorkspace()
  const doc = await NativePage.create(request, ws.id, application, "PRD draft revocation")
  await requireNative(prd, request, doc)
  const { peer, state } = await peerApi(prd, cleanup)
  for (const level of levels) await doc.grant(request, peer.userId, level)
  const context = await contexts(state)
  cleanup.push(() => context.close())
  await context.addInitScript(recordStream)
  await context.addInitScript(watchEngine)
  // A held save is what leaves the draft unconfirmed; nothing else changes.
  const held: Route[] = []
  await context.route("**/craft/document/save*", (route) => { held.push(route) })
  const releaseSaves = async () => {
    await context.unroute("**/craft/document/save*")
    for (const route of held.splice(0)) await route.continue().catch(() => {})
  }
  const page = await open(context, prd.origin, doc, ws.id)
  const frame = await acquire(page, application)
  const marker = `DRAFT${Date.now().toString(36)}`
  await typeMarker(page, frame, application, marker, 3)
  await until("the draft reached the journal", async () => (await draftRows(page, doc.pageId)) > 0, 30_000)
  await frameOf(page, application)
  return { context, page, doc, peer, workspace: ws.id, state, marker, releaseSaves }
}

/** The same for a Project: a shared Project, its source edited and not saved. */
async function holdingProject(prd: Prd, request: Api, cleanup: Array<() => Promise<void>>, levels: Array<"read" | "comment" | "write">, contexts: Contexts): Promise<Fixture & { slug: string; marker: string }> {
  const ws = await prd.qaWorkspace()
  await ensureArtifactProjects(request, ws.id, !prd.env.workspaceId)
  const project = await createProject(request, ws.id, "revocation")
  expect(await shareProject(request, ws.id, project.pageId, 1), "share the Project").toBeLessThan(300)
  // Grants go through the Page, whatever the Page holds.
  const doc = new NativePage(ws.id, project.pageId, "wordcraft")
  const { peer, state } = await peerApi(prd, cleanup)
  for (const level of levels) await doc.grant(request, peer.userId, level)
  const context = await contexts(state)
  cleanup.push(() => context.close())
  const page = await newTab(context)
  await page.goto(new URL(projectURL(project.slug, ws.id), prd.origin).href, { waitUntil: "domcontentloaded" })
  await page.getByTitle("Artifact preview").waitFor({ timeout: 60_000 })
  const marker = `DRAFT${Date.now().toString(36)}`
  await (await prepareSourceDraft(page, marker))()
  await until("the draft reached the browser", async () => (await projectRows(page, project.pageId)) > 0, 30_000)
  return { context, page, doc, peer, workspace: ws.id, state, slug: project.slug, marker, releaseSaves: async () => {} }
}

/** A second tab of the same person on the Project of `f`. */
async function openProject(f: Fixture): Promise<Page> {
  const page = await newTab(f.context)
  await page.goto(f.page.url(), { waitUntil: "domcontentloaded" })
  await page.getByTitle("Artifact preview").waitFor({ timeout: 60_000 })
  await f.page.bringToFront()
  return page
}

async function signOut(page: Page): Promise<boolean> {
  await page.bringToFront()
  await page.getByRole("button", { name: "User menu" }).first().click()
  await page.getByRole("menuitem", { name: /Log out/ }).or(page.getByRole("button", { name: /Log out/ })).first().click()
  const confirm = page.getByRole("button", { name: "Sign out", exact: true })
  const asked = await confirm.waitFor({ timeout: 5_000 }).then(() => true, () => false)
  if (asked) await confirm.click()
  return asked
}

function expireSession(db: string | undefined, userId: string) {
  expect(db, "needs REVOCATION_DB: a throwaway server's database").toBeTruthy()
  execFileSync("sqlite3", [db!, `UPDATE user_sessions SET expires_at='2000-01-01T00:00:00Z' WHERE user_id='${userId.replace(/[^a-zA-Z0-9_-]/g, "")}'`])
}

function proxy() {
  const control = process.env.REVOCATION_PROXY_CONTROL, direct = process.env.REVOCATION_DIRECT
  expect(control && direct, "needs revocation-proxy.mjs in front of the server").toBeTruthy()
  return { control: control!, direct: direct! }
}

async function signIn(page: Page, fixture: Fixture, target: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(target)}`)
  await page.locator("#email").fill(fixture.peer.account.email)
  await page.locator("#password").fill(fixture.peer.account.password)
  await page.locator('button[type="submit"]').first().click()
}

/** What the tab really painted: a transparent, blank or empty frame shows here,
 * and no style or text in the DOM can pass for it. The images stay in the evidence. */
async function rendered(prd: Prd, area: Locator, name: string) {
  const png = await area.screenshot({ timeout: 30_000 })
  await prd.evidence(`${name}.png`, png)
  return png
}
function expectSameContent(before: Buffer, after: Buffer) {
  const shot = decodePng(after), seen = measureImage(shot), compared = compareImages(decodePng(before), shot)
  expect(seen.contentShare, "the editor is not blank or one colour after the return").toBeGreaterThan(MIN_CONTENT)
  expect(compared.lost, "the editor kept the content seen before the return").toBeLessThan(MAX_LOST)
  return { contentShare: seen.contentShare, ...compared }
}
/** A return that is still waiting for its read, however long the test needs. */
async function holdReturnRead(page: Page, url: string) {
  let open = () => {}
  const gate = new Promise<void>((resolve) => { open = resolve })
  const handler = (route: Route) => route.request().method() === "GET" ? gate.then(() => route.continue()).catch(() => {}) : route.fallback()
  await page.route(url, handler)
  return async () => { open(); await page.unroute(url, handler) }
}
/** Goes on typing from the keyboard alone, and returns the server's head once it holds that. */
async function typeOn(f: Fixture, text: string, request: Api) {
  if (f.doc.application === "wordcraft") await typeMarker(f.page, await frameOf(f.page, f.doc.application), f.doc.application, text, 3)
  else { await f.page.keyboard.type(text); await f.page.keyboard.press("Enter") }
  await until("the new work reached the server", async () => (await readNative(request, f.workspace, f.doc.pageId)).raw.includes(text), 60_000)
  return readNative(request, f.workspace, f.doc.pageId)
}
/** Leaves `f.page` hidden and frozen, then brings it back. Returns when it came back. */
async function awayAndBack(f: Fixture) {
  await hide(f)
  const cdp = await f.context.newCDPSession(f.page)
  await freeze(cdp)
  await pause(2_000)
  await thaw(cdp)
  const returned = Date.now()
  await f.page.bringToFront()
  return returned
}

const cleanups = () => {
  let browser: Browser
  const cleanup: Array<() => Promise<void>> = []
  test.beforeAll(async () => { browser = await chromium.launch({ headless: true }) })
  test.afterEach(async () => { for (const c of cleanup.splice(0).reverse()) await c().catch(() => {}) })
  test.afterAll(async () => { await browser.close() })
  return { cleanup, browser: () => browser }
}

for (const { application, label } of APPLICATIONS) {
  const id = (name: string) => `A.craft.draft.${label.toLowerCase()}.${name}`
  test.describe(`${label} draft on access change`, () => {
    test.setTimeout(12 * 60_000)
    const { cleanup, browser } = cleanups()
    const hold = (prd: Prd, request: Api, levels: Array<"read" | "comment" | "write">, from = contextsOf(prd, browser())) => holding(application, prd, request, cleanup, levels, from)

    test(`${label}: read lost while the editor is open`, { annotation: prdRequirement(id("read-loss-open"), `${label}: read removed on an open editor is delivered, hides the content and erases the draft`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"])
      const rows = await draftRows(f.page, f.doc.pageId)
      await f.doc.revoke(request, f.peer.userId, "write")
      const t0 = Date.now()
      const t1 = await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
      const hiddenAt = await until("content hidden", () => contentHidden(f.page), 30_000)
      const erasedAt = await until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 30_000)
      const frame = (await retired(f.page))[0]
      await prd.evidence("timing.json", { rows, frame, deliveryMs: t1 - t0, hiddenMs: hiddenAt - t1, erasedMs: erasedAt - t1 })
      expect(frame).toMatchObject({ draft: "purge" })
      expect(t1 - t0, "delivery").toBeLessThan(BOUND.deliveryMs)
      expect(erasedAt - t1, "reaction").toBeLessThan(BOUND.reactionMs)
    })

    for (const level of LEVELS) {
      test(`${label}: Edit reduced to ${level} keeps the draft`, { annotation: prdRequirement(id(`write-only-${level}`), `${label}: Edit reduced to ${level} hides latest content and keeps the draft`) }, async ({ prd, request }) => {
        const f = await hold(prd, request, [level, "write"])
        const rows = await draftRows(f.page, f.doc.pageId)
        await f.doc.revoke(request, f.peer.userId, "write")
        const t0 = Date.now()
        const t1 = await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
        const frame = (await retired(f.page))[0]
        await until("the editor says Edit is gone", async () => (await f.page.getByRole("alert").filter({ hasText: REFUSED }).count()) > 0 || (await f.page.getByRole("status").filter({ hasText: /Pouze čtení/ }).count()) > 0, 30_000)
        // Long enough for any late proof to have acted.
        await pause(15_000)
        const after = await draftRows(f.page, f.doc.pageId)
        await prd.evidence("timing.json", { level, rows, after, frame, deliveryMs: t1 - t0, hidden: await contentHidden(f.page), offered: await recoveryRows(f.page).count() })
        expect(frame).toMatchObject({ draft: "retain", reason: "write_revoked" })
        expect(after, "the draft is kept").toBeGreaterThan(0)
        expect(await recoveryRows(f.page).count(), "a kept draft is offered to nobody").toBe(0)
        await f.page.reload({ waitUntil: "domcontentloaded" })
        await f.page.getByRole("status").filter({ hasText: /Pouze čtení/ }).first().waitFor({ timeout: 90_000 }).catch(() => {})
        await pause(3_000)
        expect(await recoveryRows(f.page).count(), "and nothing after a reload").toBe(0)
        expect(await draftRows(f.page, f.doc.pageId), "the draft survives the reload").toBeGreaterThan(0)
      })
    }

    test(`${label}: Comment removed after admission stopped erases without a reload`, { annotation: prdRequirement(id("comment-to-none"), `${label}: a draft kept after Edit became Comment is erased when Comment is removed too, with no reload`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["comment", "write"])
      await f.doc.revoke(request, f.peer.userId, "write")
      await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
      await until("the editor says Edit is gone", async () => (await f.page.getByRole("alert").filter({ hasText: REFUSED }).count()) > 0, 30_000)
      await pause(15_000)
      const kept = await draftRows(f.page, f.doc.pageId)
      await f.doc.revoke(request, f.peer.userId, "comment")
      const t0 = Date.now()
      const erased = await until("the kept draft is erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, BOUND.reconnectMs + 5_000)
      await prd.evidence("timing.json", { kept, erasedMs: erased - t0, frames: (await retired(f.page)).length })
      expect(kept, "Comment alone kept the draft").toBeGreaterThan(0)
    })

    test(`${label}: read lost while offline is learned on reconnect`, { annotation: prdRequirement(id("offline-reconnect"), `${label}: an offline browser keeps the draft until it returns, and then erases it`) }, async ({ prd, request }) => {
      const { control, direct } = proxy()
      const f = await hold(prd, request, ["write"])
      const owner = await requests.newContext({ baseURL: direct, storageState: await request.storageState() })
      cleanup.push(() => owner.dispose())
      const frames = (await retired(f.page)).length
      await fetch(`${control}/cut`, { method: "POST" })
      cleanup.push(async () => { await fetch(`${control}/restore`, { method: "POST" }) })
      await pause(2_000)
      await f.doc.revoke(owner, f.peer.userId, "write")
      const t0 = Date.now()
      // Longer than the event interval, the reauthorization and its deadline.
      await pause(70_000)
      const whileOffline = { retired: (await retired(f.page)).length - frames, rows: await draftRows(f.page, f.doc.pageId), hidden: await contentHidden(f.page) }
      await fetch(`${control}/restore`, { method: "POST" })
      const back = Date.now()
      const erasedAt = await until("draft erased after reconnect", async () => (await draftRows(f.page, f.doc.pageId)) === 0, BOUND.reconnectMs + 30_000).catch(() => 0)
      await prd.evidence("timing.json", { offlineMs: back - t0, whileOffline, reconnectToErasedMs: erasedAt ? erasedAt - back : null, afterReconnect: await retired(f.page) })
      expect(whileOffline.retired, "nothing reached the browser while offline").toBe(0)
      expect(whileOffline.rows, "the draft stays while offline").toBeGreaterThan(0)
      expect(whileOffline.hidden, "content is hidden once read cannot be confirmed").toBe(true)
      expect(erasedAt - back, "reconnect").toBeLessThan(BOUND.reconnectMs)
      expect(erasedAt, "erased after reconnect").toBeGreaterThan(0)
    })

    test(`${label}: session expiry keeps the draft`, { annotation: prdRequirement(id("expiry-keeps"), `${label}: an expired session keeps the draft and the same person gets it back`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"])
      const rows = await draftRows(f.page, f.doc.pageId)
      const takeOver: string[] = []
      f.page.on("console", (message) => { if (message.text().startsWith("native_acquire_failed")) takeOver.push(message.text()) })
      expireSession(process.env.REVOCATION_DB, f.peer.userId)
      const t0 = Date.now()
      const ended = await until("the editor stopped", async () => (await retired(f.page)).length > 0 || /\/login/.test(f.page.url()), 90_000)
      const frame = (await retired(f.page).catch(() => []))[0]
      await pause(5_000)
      const after = await draftRows(f.page, f.doc.pageId)
      await signIn(f.page, f, editorURL(f.doc.pageId, f.workspace))
      // The journal opens with the editor, so a kept draft comes back on taking it.
      await takeButton(f.page).click({ timeout: 90_000 })
      const offered = await recoveryRows(f.page).first().waitFor({ timeout: 60_000 }).then(() => Date.now(), () => 0)
      await prd.evidence("timing.json", { rows, after, frame, endedMs: ended - t0, url: f.page.url().replace(/\?.*/, ""), offeredAfterLogin: offered > 0, takeOver })
      expect(frame?.draft ?? "retain", "the stream never says purge").toBe("retain")
      expect(after, "the draft survives expiry").toBeGreaterThan(0)
      expect(offered, "the same person gets it back").toBeGreaterThan(0)
    })

    for (const tabs of [1, 2]) {
      test(`${label}: sign-out erases with ${tabs} tab${tabs > 1 ? "s" : ""} open`, { annotation: prdRequirement(id(`sign-out-${tabs}`), `${label}: a confirmed sign-out with ${tabs} tab(s) open erases the draft and stops every tab`) }, async ({ prd, request }) => {
        const f = await hold(prd, request, ["write"])
        const other = tabs > 1 ? await open(f.context, prd.origin, f.doc, f.workspace) : null
        await pause(2_000)
        const rows = await draftRows(f.page, f.doc.pageId)
        const rowsBefore = await rowIds(f.page, f.doc.pageId)
        const asked = await signOut(f.page)
        const t0 = Date.now()
        await expect.poll(() => f.page.url(), { timeout: 30_000 }).toMatch(/\/login/)
        const signedOutAt = Date.now()
        const [erasedAt, otherStopped] = await Promise.all([
          until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 20_000).catch(() => 0),
          other ? until("the other tab stopped showing content", async () => /\/login/.test(other.url()) || await contentHidden(other), 30_000).then((at) => at - t0, () => -1) : null,
        ])
        const left = await draftRows(f.page, f.doc.pageId)
        await prd.evidence(`timing-${test.info().repeatEachIndex}.json`, { tabs, rows, asked, rowsBefore, rowsAfter: await rowIds(f.page, f.doc.pageId), loginMs: signedOutAt - t0, erasedMs: erasedAt ? erasedAt - t0 : null, left, otherStopped, otherUrl: other?.url().replace(/\?.*/, "") })
        expect(asked, "the product asks first").toBe(true)
        expect(left, "no row survives a confirmed sign-out").toBe(0)
        if (other) expect(otherStopped, "the other tab").toBeGreaterThanOrEqual(0)
      })
    }

    test(`${label}: sign-out erases when the holder never hears of it`, { annotation: prdRequirement(id("sign-out-no-broadcast"), `${label}: a holder that leaves on the ended session, without any sign-out message, leaves no draft`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"])
      const other = await open(f.context, prd.origin, f.doc, f.workspace)
      await pause(2_000)
      const rows = await draftRows(other, f.doc.pageId)
      // The durable intent has to do this alone.
      await other.evaluate(() => { BroadcastChannel.prototype.postMessage = () => {} })
      await signOut(other)
      await expect.poll(() => other.url(), { timeout: 30_000 }).toMatch(/\/login/)
      // One stream interval for the holder to learn and retire on its own.
      await pause(15_000)
      const left = await draftRows(other, f.doc.pageId)
      await prd.evidence("timing.json", { rows, left, holderUrl: f.page.url().replace(/\?.*/, "") })
      expect(rows, "there was a draft").toBeGreaterThan(0)
      expect(left, "no row survives a confirmed sign-out").toBe(0)
    })

    test(`${label}: sign-out erases when writing its intent fails`, { annotation: prdRequirement(id("sign-out-intent-failed"), `${label}: a sign-out whose intent write fails still leaves no draft from a holder that writes after it`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"])
      const other = await open(f.context, prd.origin, f.doc, f.workspace)
      await pause(2_000)
      const rows = await draftRows(other, f.doc.pageId)
      await other.bringToFront()
      await other.getByRole("button", { name: "User menu" }).first().click()
      await other.getByRole("menuitem", { name: /Log out/ }).or(other.getByRole("button", { name: /Log out/ })).first().click()
      const confirm = other.getByRole("button", { name: "Sign out", exact: true })
      await confirm.waitFor({ timeout: 5_000 })
      // Drafts were captured for this dialog. The next open of their store in this
      // tab is the intent's: refuse exactly that one, as a storage hiccup would.
      await other.evaluate(() => {
        const refused = { count: 0 }
        ;(window as unknown as { __refused: typeof refused }).__refused = refused
        const open = IDBFactory.prototype.open
        IDBFactory.prototype.open = function (name: string, version?: number) {
          if (name === "crewship-document-drafts" && !refused.count) { refused.count++; throw new DOMException("refused by the test", "UnknownError") }
          return open.call(this, name, version)
        }
      })
      await confirm.click()
      await expect.poll(() => other.url(), { timeout: 30_000 }).toMatch(/\/login/)
      await expect.poll(() => f.page.url(), { timeout: 30_000 }).toMatch(/\/login/)
      // Long enough for the holder's final write and the next sweep.
      await pause(5_000)
      const left = await draftRows(other, f.doc.pageId)
      await prd.evidence("timing.json", { rows, left, rowsLeft: await rowIds(other, f.doc.pageId) })
      expect(rows, "there was a draft").toBeGreaterThan(0)
      expect(left, "no row survives a confirmed sign-out").toBe(0)
    })

    for (const holder of ["open", "closed"] as const) {
      test(`${label}: sign-out from the other tab erases with the holder tab ${holder}`, { annotation: prdRequirement(id(`sign-out-holder-${holder}`), `${label}: sign-out confirmed in a tab without the draft erases it, holder tab ${holder}`) }, async ({ prd, request }) => {
        const f = await hold(prd, request, ["write"])
        const other = await open(f.context, prd.origin, f.doc, f.workspace)
        await pause(2_000)
        const rows = await draftRows(other, f.doc.pageId)
        if (holder === "closed") await f.page.close({ runBeforeUnload: false })
        await signOut(other)
        const t0 = Date.now()
        await expect.poll(() => other.url(), { timeout: 30_000 }).toMatch(/\/login/)
        if (holder === "open") await expect.poll(() => f.page.url(), { timeout: 30_000 }).toMatch(/\/login/)
        const erasedAt = await until("draft erased", async () => (await draftRows(other, f.doc.pageId)) === 0, 20_000).catch(() => 0)
        // Long enough for a holder still suspending to have written.
        await pause(5_000)
        const left = await draftRows(other, f.doc.pageId)
        await prd.evidence(`timing-${test.info().repeatEachIndex}.json`, { holder, rows, erasedMs: erasedAt ? erasedAt - t0 : null, left })
        expect(rows, "there was a draft").toBeGreaterThan(0)
        expect(left, "no row survives a confirmed sign-out").toBe(0)
      })
    }

    test(`${label}: read lost with two tabs open erases once and stops both`, { annotation: prdRequirement(id("multi-tab"), `${label}: read removed with two tabs open erases the draft and stops both tabs`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"])
      const other = await open(f.context, prd.origin, f.doc, f.workspace)
      await f.doc.revoke(request, f.peer.userId, "write")
      const t0 = Date.now()
      const first = await until("delivery to the editing tab", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
      const second = await until("delivery to the other tab", async () => (await retired(other)).length > 0, BOUND.deliveryMs + 5_000)
      const erasedAt = await until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 30_000)
      const hiddenOther = await until("the other tab hides", () => contentHidden(other), 30_000)
      await prd.evidence("timing.json", { deliveryEditingMs: first - t0, deliveryOtherMs: second - t0, erasedMs: erasedAt - t0, hiddenOtherMs: hiddenOther - t0, framesEditing: await retired(f.page), framesOther: await retired(other) })
      expect((await retired(other))[0]).toMatchObject({ draft: "purge" })
    })

    test(`${label}: a hidden tab reacts like a visible one`, { annotation: prdRequirement(id("hidden-tab"), `${label}: read removed while the tab is hidden is delivered and acted on within the same bounds`) }, async ({ prd, request }) => {
      const hiddenForMs = application === "wordcraft" ? HIDDEN_MS : 60_000
      const f = await hold(prd, request, ["write"], fullChromium(prd, cleanup))
      await hide(f)
      await pause(hiddenForMs)
      await f.doc.revoke(request, f.peer.userId, "write")
      const t0 = Date.now()
      const t1 = await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 60_000)
      const erasedAt = await until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 90_000)
      const state = await f.page.evaluate(() => document.visibilityState)
      await prd.evidence("timing.json", { hiddenForMs, deliveryMs: t1 - t0, erasedMs: erasedAt - t1, stillHidden: state })
      expect((await retired(f.page))[0]).toMatchObject({ draft: "purge" })
      expect(state, "the tab stayed hidden throughout").toBe("hidden")
    })

    // On return the tab cannot have heard anything for as long as it was frozen, so
    // this is the opponent's sentence: what is on screen before the answer.
    test(`${label}: a frozen tab shows nothing of the draft on return after read was lost`, { annotation: prdRequirement(id("return-read-lost"), `${label}: read removed while a tab was frozen: no painted frame shows the editor after its return, and the draft is erased`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"], fullChromium(prd, cleanup))
      const rows = await draftRows(f.page, f.doc.pageId)
      await hide(f)
      const cdp = await f.context.newCDPSession(f.page)
      await freeze(cdp)
      await f.doc.revoke(request, f.peer.userId, "write")
      const t0 = Date.now()
      await pause(2_000)
      await thaw(cdp)
      const returned = Date.now()
      await f.page.bringToFront()
      const erasedAt = await until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, BOUND.returnMs + 10_000)
      await pause(500)
      const { back, steps } = await sinceReturn(f.page, returned - 2_000)
      await prd.evidence("timeline.json", { rows, revokedBeforeReturnMs: returned - t0, erasedAfterReturnMs: erasedAt - back, steps })
      expect(painted(steps).filter((s) => s.frame === "shown"), "a painted frame showed the editor").toEqual([])
      expect(erasedAt - back, "return to erased").toBeLessThan(BOUND.returnMs)
    })

    test(`${label}: a frozen tab shows nothing of the draft on return after Edit was lost`, { annotation: prdRequirement(id("return-write-lost"), `${label}: Edit reduced to Read while a tab was frozen: no painted frame shows the editor or an offer after its return, and the draft stays`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["read", "write"], fullChromium(prd, cleanup))
      const rows = await draftRows(f.page, f.doc.pageId)
      await hide(f)
      const cdp = await f.context.newCDPSession(f.page)
      await freeze(cdp)
      await f.doc.revoke(request, f.peer.userId, "write")
      await pause(2_000)
      await thaw(cdp)
      const returned = Date.now()
      await f.page.bringToFront()
      await until("the editor says Edit is gone", async () => (await f.page.getByRole("alert").filter({ hasText: REFUSED }).count()) > 0 || (await f.page.getByRole("status").filter({ hasText: /Pouze čtení/ }).count()) > 0, 30_000)
      await pause(3_000)
      const after = await draftRows(f.page, f.doc.pageId)
      const { steps } = await sinceReturn(f.page, returned - 2_000)
      await prd.evidence("timeline.json", { rows, after, steps })
      const editable = painted(steps).filter((s) => s.frame === "shown" && !s.status.includes("Pouze čtení"))
      expect(editable, "a painted frame showed the held editor").toEqual([])
      expect(steps.filter((s) => s.offer), "a kept draft is offered to nobody").toEqual([])
      expect(after, "the draft is kept").toBeGreaterThan(0)
    })

    test(`${label}: a tab that lost nothing comes back as it was`, { annotation: prdRequirement(id("return-unchanged"), `${label}: with read intact a returning tab shows its editor again once read is confirmed, keeps its focus and keeps its work`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"], fullChromium(prd, cleanup))
      const rows = await draftRows(f.page, f.doc.pageId)
      const focused = () => f.page.evaluate(() => document.activeElement?.tagName ?? "")
      const before = await focused()
      const area = f.page.locator('iframe[title$=" editor"]')
      const shown = await rendered(prd, area, "before")
      const returned = await awayAndBack(f)
      await until("the editor is shown again", async () => !(await contentHidden(f.page)), BOUND.returnMs)
      await pause(1_000)
      const { steps } = await sinceReturn(f.page, returned - 2_000)
      const hiddenFor = steps.findLast((s) => s.frame === "transparent")?.ms ?? null
      const pixels = expectSameContent(shown, await rendered(prd, area, "after"))
      const kept = await draftRows(f.page, f.doc.pageId)
      await prd.evidence("timeline.json", { rows, hiddenFor, focusBefore: before, focusAfter: await focused(), pixels, steps })
      expect(painted(steps).at(-1)?.frame, "the last painted frame shows the editor").toBe("shown")
      expect(await focused(), "focus stays in the editor").toBe(before)
      expect(await f.page.getByRole("status").filter({ hasText: /Neuložený koncept|Připraveno k úpravám|Ukládám/ }).count(), "the editor is still writable").toBeGreaterThan(0)
      expect(kept, "the draft is kept").toBeGreaterThan(0)
      expect(await contentHidden(f.page), "the editor is shown").toBe(false)
      // The work goes on from the keyboard alone: no click, no Ověřit výsledek.
      const next = `NEXT${Date.now().toString(36)}`
      await f.releaseSaves()
      const head = await typeOn(f, next, request)
      const version = await waitSaved(f.page, 60_000)
      await prd.evidence("continued.json", { version: head.version, shownVersion: version, marker: head.raw.includes(f.marker), next: head.raw.includes(next), verify: await f.page.getByRole("button", { name: "Ověřit výsledek", exact: true }).count() })
      expect(head.raw, "the work from before the return reached the server").toContain(f.marker)
      expect(head.raw, "the work typed after the return reached the server").toContain(next)
      expect(await f.page.getByRole("button", { name: "Ověřit výsledek", exact: true }).count(), "nothing had to be confirmed by hand").toBe(0)
    })

    test(`${label}: typing cut off by a return goes on after it`, { annotation: prdRequirement(id("return-mid-typing"), `${label}: text still being typed when the tab leaves is there after a slow return, and the keyboard goes on from it`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"], fullChromium(prd, cleanup))
      await f.releaseSaves()
      const half = `HALF${Date.now().toString(36)}`
      await f.page.keyboard.type(half)
      await pause(500)
      // A slow check: no read-only flip may end the cell being typed.
      const release = await holdReturnRead(f.page, "**/craft/document?*")
      await awayAndBack(f)
      await pause(1_500)
      await release()
      await until("the editor is shown again", async () => !(await contentHidden(f.page)), BOUND.returnMs + 15_000)
      await pause(1_000)
      const tail = `TAIL${Date.now().toString(36)}`
      await f.page.keyboard.type(tail)
      await f.page.keyboard.press("Enter")
      await rendered(prd, f.page.locator('iframe[title$=" editor"]'), "typed")
      await until("the whole line reached the server", async () => (await readNative(request, f.workspace, f.doc.pageId)).raw.includes(half + tail), 60_000)
    })

    test(`${label}: input during a pending check never replaces the draft when Edit is lost`, { annotation: prdRequirement(id("return-pending-input"), `${label}: destructive input while the read of a return is pending stays out of the draft when Edit is then lost, and the draft comes back with Edit`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["read", "write"], fullChromium(prd, cleanup))
      const release = await holdReturnRead(f.page, "**/craft/document?*")
      // The last words go straight before leaving: the draft has to hold them too.
      const tail = `TAIL${Date.now().toString(36)}`
      await f.page.keyboard.type(tail)
      await f.page.keyboard.press("Enter")
      await f.page.keyboard.press("Control+a")
      const returned = await awayAndBack(f)
      // At once, inside the first moments of the check, on the only copy of the work.
      await f.page.keyboard.press(application === "wordcraft" ? "Backspace" : "Delete")
      const typedAfterMs = Date.now() - returned
      await until("the editor is off screen", () => contentHidden(f.page), BOUND.returnMs)
      await f.doc.revoke(request, f.peer.userId, "write")
      await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
      await pause(3_000)
      await release()
      await pause(1_000)
      const rows = await draftRows(f.page, f.doc.pageId)
      const log = (await engineLog(f.page) as unknown[]).slice(-16)
      await f.doc.grant(request, f.peer.userId, "write")
      await f.page.reload({ waitUntil: "domcontentloaded" })
      await f.page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
      await acquire(f.page, application)
      await recoveryRows(f.page).first().waitFor({ timeout: 30_000 })
      const drafts = await compareDrafts(f.page, [f.marker, tail])
      await prd.evidence("kept.json", { typedAfterMs, log, rows, drafts: drafts.map((d) => ({ row: d.row, draft: d.draft })) })
      expect(rows, "the draft is kept").toBeGreaterThan(0)
      expect(drafts.some((d) => d.draft[f.marker] && d.draft[tail]), "the row from before the return stands, with the last words").toBe(true)
    })

    test(`${label}: input ahead of a capture's answer leaves the capture standing, with no earlier row`, { annotation: prdRequirement(id("return-capture-order"), `${label}: with no row yet, a key heard before the answer of a capture taken earlier does not discard that capture, and the work is in the row after write loss and reload`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["read", "write"], fullChromium(prd, cleanup))
      // Saved, so its row is gone; then new work that only the engine holds.
      await f.releaseSaves()
      await until("the row left with the save", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 30_000)
      await f.context.route("**/craft/document/save*", () => {})
      await lateSnapshots(f.page, 7_000)
      const release = await holdReturnRead(f.page, "**/craft/document?*")
      const tail = `TAIL${Date.now().toString(36)}`
      await f.page.keyboard.type(tail)
      await f.page.keyboard.press("Enter")
      await f.page.keyboard.press("Control+a")
      await pause(600)
      await awayAndBack(f)
      await f.page.keyboard.press(application === "wordcraft" ? "Backspace" : "Delete")
      const rowsAtKey = await draftRows(f.page, f.doc.pageId)
      await until("the answer reached the row", async () => (await draftRows(f.page, f.doc.pageId)) > 0, 15_000).catch(() => 0)
      await f.doc.revoke(request, f.peer.userId, "write")
      await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
      await pause(1_000)
      await release()
      await pause(1_000)
      const log = await engineLog(f.page)
      await f.doc.grant(request, f.peer.userId, "write")
      await f.page.reload({ waitUntil: "domcontentloaded" })
      await f.page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
      await acquire(f.page, application)
      await recoveryRows(f.page).first().waitFor({ timeout: 30_000 })
      const drafts = await compareDrafts(f.page, [tail])
      await prd.evidence("order.json", { rowsAtKey, drafts: drafts.map((d) => d.draft), log: (log as Array<{ type: string }>).slice(-14) })
      expect(rowsAtKey, "no earlier row").toBe(0)
      expect(drafts.some((d) => d.draft[tail]), "the original text is in the row").toBe(true)
    })

    test(`${label}: input during a check that confirms is kept`, { annotation: prdRequirement(id("return-pending-kept"), `${label}: typing during a slow check goes on after a confirmed read and reaches the server, and the page says what it is doing`) }, async ({ prd, request }) => {
      const f = await hold(prd, request, ["write"], fullChromium(prd, cleanup))
      const release = await holdReturnRead(f.page, "**/craft/document?*")
      await awayAndBack(f)
      await until("the editor is off screen", () => contentHidden(f.page), BOUND.returnMs)
      const during = `DURING${Date.now().toString(36)}`
      await f.page.keyboard.type(during)
      await f.page.keyboard.press("Enter")
      await pause(1_500)
      const notice = await f.page.getByText("Ověřuji přístup…").count()
      await release()
      await until("the editor is shown again", async () => !(await contentHidden(f.page)), BOUND.returnMs + 15_000)
      await rendered(prd, f.page.locator('iframe[title$=" editor"]'), "after-check")
      await f.releaseSaves()
      const head = await typeOn(f, `NEXT${Date.now().toString(36)}`, request)
      await prd.evidence("kept.json", { notice, marker: head.raw.includes(f.marker), during: head.raw.includes(during), version: head.version })
      expect(notice, "a slow check says what it is doing").toBe(1)
      expect(head.raw, "typing during the check is kept").toContain(during)
      expect(head.raw, "the work from before the return is saved").toContain(f.marker)
    })
  })
}

test.describe("Project draft on access change", () => {
  test.setTimeout(12 * 60_000)
  const { cleanup, browser } = cleanups()
  const id = (name: string) => `A.craft.draft.project.${name}`
  const refusedRead = (page: Page) => page.waitForResponse((r) => /\/artifact(\?|$)/.test(r.url()) && r.request().method() === "GET" && (r.status() === 403 || r.status() === 404), { timeout: BOUND.projectPollMs + 5_000 })

  test("Project: read lost while the Project is open", { annotation: prdRequirement(id("read-loss-open"), "Project: read removed on an open source editor is delivered by the next read, hides the draft and erases it") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], contextsOf(prd, browser()))
    const rows = await projectRows(f.page, f.doc.pageId)
    const refused = refusedRead(f.page)
    await f.doc.revoke(request, f.peer.userId, "write")
    const t0 = Date.now()
    const status = (await refused).status()
    const t1 = Date.now()
    const erasedAt = await until("draft erased", async () => (await projectRows(f.page, f.doc.pageId)) === 0, 30_000)
    await pause(500)
    const said = await f.page.getByRole("alert").filter({ hasText: /Page read access was removed/ }).count()
    await prd.evidence("timing.json", { rows, status, deliveryMs: t1 - t0, erasedMs: erasedAt - t1, said })
    expect(t1 - t0, "delivery").toBeLessThan(BOUND.projectPollMs)
    expect(erasedAt - t1, "reaction").toBeLessThan(BOUND.reactionMs)
    expect(said, "the person is told").toBe(1)
    expect(await f.page.getByRole("textbox", { name: "Source file", exact: true }).count(), "the editor is gone").toBe(0)
  })

  for (const level of LEVELS) {
    test(`Project: Edit reduced to ${level} keeps the draft`, { annotation: prdRequirement(id(`write-only-${level}`), `Project: Edit reduced to ${level} keeps the draft sealed and offers it to nobody, before a reload and after one`) }, async ({ prd, request }) => {
      const f = await holdingProject(prd, request, cleanup, [level, "write"], contextsOf(prd, browser()))
      const rows = await projectRows(f.page, f.doc.pageId)
      await f.doc.revoke(request, f.peer.userId, "write")
      const t0 = Date.now()
      await until("the Project goes read-only", async () => (await f.page.getByText("Read only", { exact: true }).count()) > 0, BOUND.projectPollMs + 5_000)
      const t1 = Date.now()
      await pause(15_000)
      const after = await projectRows(f.page, f.doc.pageId)
      const sameTab = await f.page.getByRole("textbox", { name: "Source recovery draft" }).count()
      const marked = await f.page.getByText(f.marker).count()
      await f.page.reload({ waitUntil: "domcontentloaded" })
      await until("the Project reopened", async () => (await f.page.getByText("Read only", { exact: true }).count()) > 0, 60_000)
      const reopened = await projectOffer(f.page)
      await prd.evidence("timing.json", { level, rows, after, deliveryMs: t1 - t0, sameTabRecoveryField: sameTab, marked, reopened })
      expect(after, "the draft is kept").toBeGreaterThan(0)
      expect(sameTab, "the same tab offers it to nobody").toBe(0)
      expect(marked, "the same tab shows none of it").toBe(0)
      expect(reopened.restoreOffered, "after a reload a kept draft is offered to nobody").toBe(0)
      expect(await f.page.getByRole("textbox", { name: "Source recovery draft" }).count(), "after a reload nothing shows it").toBe(0)
    })
  }

  test("Project: read lost while offline is learned on reconnect", { annotation: prdRequirement(id("offline-reconnect"), "Project: an offline browser keeps the draft until it returns, and then erases it") }, async ({ prd, request }) => {
    const { control, direct } = proxy()
    const f = await holdingProject(prd, request, cleanup, ["write"], contextsOf(prd, browser()))
    const owner = await requests.newContext({ baseURL: direct, storageState: await request.storageState() })
    cleanup.push(() => owner.dispose())
    await fetch(`${control}/cut`, { method: "POST" })
    cleanup.push(async () => { await fetch(`${control}/restore`, { method: "POST" }) })
    await pause(2_000)
    await f.doc.revoke(owner, f.peer.userId, "write")
    const t0 = Date.now()
    // Past the 15 s poll, so the page has had its failed read.
    await pause(25_000)
    const whileOffline = { rows: await projectRows(f.page, f.doc.pageId), recovery: await f.page.getByRole("textbox", { name: "Source recovery draft" }).count(), editor: await f.page.getByRole("textbox", { name: "Source file", exact: true }).count() }
    await fetch(`${control}/restore`, { method: "POST" })
    const back = Date.now()
    const erasedAt = await until("draft erased after reconnect", async () => (await projectRows(f.page, f.doc.pageId)) === 0, BOUND.reconnectMs).catch(() => 0)
    await prd.evidence("timing.json", { offlineMs: back - t0, whileOffline, reconnectToErasedMs: erasedAt ? erasedAt - back : null })
    expect(whileOffline.rows, "the draft stays while offline").toBeGreaterThan(0)
    expect(erasedAt, "erased after reconnect").toBeGreaterThan(0)
    expect(erasedAt - back, "reconnect").toBeLessThan(BOUND.reconnectMs)
  })

  test("Project: session expiry keeps the draft", { annotation: prdRequirement(id("expiry-keeps"), "Project: an expired session keeps the draft and the same person gets it back") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], contextsOf(prd, browser()))
    const rows = await projectRows(f.page, f.doc.pageId)
    expireSession(process.env.REVOCATION_DB, f.peer.userId)
    const t0 = Date.now()
    const ended = await until("the page leaves for the login", async () => /\/login/.test(f.page.url()), 90_000)
    await pause(2_000)
    const after = await projectRows(f.page, f.doc.pageId)
    await signIn(f.page, f, `/pages/${encodeURIComponent(f.slug)}?artifact=1&edit=1&workspace_id=${encodeURIComponent(f.workspace)}`)
    const offered = await f.page.getByRole("button", { name: "Restore source draft", exact: true }).waitFor({ timeout: 60_000 }).then(() => Date.now(), () => 0)
    await prd.evidence("timing.json", { rows, after, endedMs: ended - t0, offeredAfterLogin: offered > 0 })
    expect(after, "the draft survives expiry").toBeGreaterThan(0)
    expect(offered, "the same person gets it back").toBeGreaterThan(0)
  })

  test("Project: sign-out erases the draft", { annotation: prdRequirement(id("sign-out"), "Project: a confirmed sign-out erases the draft") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], contextsOf(prd, browser()))
    const rows = await projectRows(f.page, f.doc.pageId)
    const asked = await signOut(f.page)
    const t0 = Date.now()
    await expect.poll(() => f.page.url(), { timeout: 30_000 }).toMatch(/\/login/)
    const erasedAt = await until("draft erased", async () => (await projectRows(f.page, f.doc.pageId)) === 0, 20_000).catch(() => 0)
    await prd.evidence("timing.json", { rows, asked, erasedMs: erasedAt ? erasedAt - t0 : null })
    expect(asked, "the product asks first").toBe(true)
    expect(erasedAt, "no row survives a confirmed sign-out").toBeGreaterThan(0)
  })

  test("Project: read lost with two tabs open erases once and stops both", { annotation: prdRequirement(id("multi-tab"), "Project: read removed with two tabs open erases the draft and ends both") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], contextsOf(prd, browser()))
    const other = await openProject(f)
    await f.doc.revoke(request, f.peer.userId, "write")
    const t0 = Date.now()
    const erasedAt = await until("draft erased", async () => (await projectRows(f.page, f.doc.pageId)) === 0, BOUND.projectPollMs + 15_000)
    await until("both tabs say so", async () => (await f.page.getByRole("alert").filter({ hasText: /Page read access was removed/ }).count()) === 1 && (await other.getByRole("alert").filter({ hasText: /Page read access was removed/ }).count()) === 1, BOUND.projectPollMs + 15_000)
    await prd.evidence("timing.json", { erasedMs: erasedAt - t0 })
    expect(await f.page.getByRole("textbox", { name: "Source file", exact: true }).count(), "the editor is gone").toBe(0)
  })

  test("Project: sign-out with two tabs erases the draft and stops both", { annotation: prdRequirement(id("sign-out-2"), "Project: a confirmed sign-out with two tabs open erases the draft and ends both") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], contextsOf(prd, browser()))
    const other = await openProject(f)
    expect(await signOut(other), "the product asks first").toBe(true)
    await expect.poll(() => other.url(), { timeout: 30_000 }).toMatch(/\/login/)
    await until("draft erased", async () => (await projectRows(other, f.doc.pageId)) === 0, 20_000)
    await until("the other tab left", async () => /\/login/.test(f.page.url()) || (await f.page.getByRole("textbox", { name: "Source file", exact: true }).count()) === 0, 30_000)
    await prd.evidence("timing.json", { url: f.page.url().replace(/\?.*/, "") })
  })

  test("Project: a hidden tab learns of lost read on its return", { annotation: prdRequirement(id("hidden-tab"), "Project: read removed while the tab is hidden and running is acted on at its return, with nothing shown first") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], fullChromium(prd, cleanup))
    await hide(f)
    await f.doc.revoke(request, f.peer.userId, "write")
    await pause(3_000)
    const returned = Date.now()
    await f.page.bringToFront()
    const erasedAt = await until("draft erased", async () => (await projectRows(f.page, f.doc.pageId)) === 0, BOUND.projectPollMs + 15_000)
    await pause(500)
    const { back, steps } = await sinceReturn(f.page, returned - 2_000)
    await prd.evidence("timeline.json", { erasedAfterReturnMs: erasedAt - back, steps })
    expect(painted(steps).filter((s) => s.source !== "none"), "a painted frame showed the draft").toEqual([])
  })

  test("Project: a frozen tab shows nothing of the draft on return after Edit was lost", { annotation: prdRequirement(id("return-write-lost"), "Project: Edit reduced to Read while a tab was frozen: no painted frame shows the draft after its return, and it stays sealed") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["read", "write"], fullChromium(prd, cleanup))
    const rows = await projectRows(f.page, f.doc.pageId)
    await hide(f)
    const cdp = await f.context.newCDPSession(f.page)
    await freeze(cdp)
    await f.doc.revoke(request, f.peer.userId, "write")
    await pause(2_000)
    await thaw(cdp)
    const returned = Date.now()
    await f.page.bringToFront()
    await until("the Project goes read-only", async () => (await f.page.getByText("Read only", { exact: true }).count()) > 0, BOUND.returnMs + 15_000)
    await pause(1_000)
    const { steps } = await sinceReturn(f.page, returned - 2_000)
    const after = await projectRows(f.page, f.doc.pageId)
    await prd.evidence("timeline.json", { rows, after, steps })
    expect(painted(steps).filter((s) => s.source !== "none"), "a painted frame showed the draft").toEqual([])
    expect(after, "the draft is kept").toBeGreaterThan(0)
  })

  test("Project: a frozen tab shows nothing of the draft on return after read was lost", { annotation: prdRequirement(id("return-read-lost"), "Project: read removed while a tab was frozen: no painted frame shows the draft after its return, and it is erased") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], fullChromium(prd, cleanup))
    const rows = await projectRows(f.page, f.doc.pageId)
    await hide(f)
    const cdp = await f.context.newCDPSession(f.page)
    await freeze(cdp)
    await f.doc.revoke(request, f.peer.userId, "write")
    await pause(2_000)
    await thaw(cdp)
    const returned = Date.now()
    await f.page.bringToFront()
    const erasedAt = await until("draft erased", async () => (await projectRows(f.page, f.doc.pageId)) === 0, BOUND.returnMs + 10_000)
    await pause(500)
    const { back, steps } = await sinceReturn(f.page, returned - 2_000)
    await prd.evidence("timeline.json", { rows, erasedAfterReturnMs: erasedAt - back, steps })
    expect(painted(steps).filter((s) => s.source !== "none"), "a painted frame showed the draft").toEqual([])
    expect(erasedAt - back, "return to erased").toBeLessThan(BOUND.returnMs)
  })

  test("Project: a tab that lost nothing comes back as it was", { annotation: prdRequirement(id("return-unchanged"), "Project: with read intact a returning tab shows its source editor again once read is confirmed, with the typed text") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], fullChromium(prd, cleanup))
    const selection = () => f.page.evaluate(() => { const el = document.activeElement as HTMLTextAreaElement; return [el.getAttribute("aria-label"), el.selectionStart, el.selectionEnd] })
    const box = f.page.getByRole("textbox", { name: "Source file", exact: true })
    await box.evaluate((el: HTMLTextAreaElement) => { el.focus(); el.setSelectionRange(2, 5) })
    const before = await selection()
    const shown = await rendered(prd, box, "before")
    const returned = await awayAndBack(f)
    await box.waitFor({ timeout: BOUND.returnMs })
    await pause(1_000)
    const { steps } = await sinceReturn(f.page, returned - 2_000)
    const pixels = expectSameContent(shown, await rendered(prd, box, "after"))
    await prd.evidence("timeline.json", { pixels, steps })
    expect(painted(steps).at(-1)?.source, "the last painted frame shows the editor").toBe("editor")
    expect(await box.inputValue(), "the typed text is back").toContain(f.marker)
    expect(await selection(), "focus and selection stay in the field").toEqual(before)
    // The work goes on from the keyboard, and saves the normal way.
    const next = `NEXT${Date.now().toString(36)}`
    await f.page.keyboard.press("Control+End")
    await f.page.keyboard.type(`<!-- ${next} -->`)
    const from = (await readArtifact(request, f.workspace, f.doc.pageId)).latest_version
    await f.page.getByRole("button", { name: "Save project", exact: true }).click()
    await box.waitFor({ state: "detached", timeout: 30_000 })
    const head = await readArtifact(request, f.workspace, f.doc.pageId)
    const source = (head.manifest as { files: Array<{ content: string }> }).files.map((file) => file.content).join("\n")
    await prd.evidence("continued.json", { from, version: head.latest_version, marker: source.includes(f.marker), next: source.includes(next) })
    expect(head.latest_version, "the save made a version").toBeGreaterThan(from)
    expect(source, "the work from before the return reached the server").toContain(f.marker)
    expect(source, "the work typed after the return reached the server").toContain(next)
  })

  test("Project: a check still waiting takes no typing", { annotation: prdRequirement(id("return-pending-input"), "Project: while the read of a return is pending the field takes no typing") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], fullChromium(prd, cleanup))
    const box = f.page.getByRole("textbox", { name: "Source file", exact: true })
    const before = await box.inputValue()
    await box.focus()
    const release = await holdReturnRead(f.page, "**/artifact?*")
    await awayAndBack(f)
    await pause(500)
    await f.page.keyboard.type("BLIND")
    await pause(1_000)
    const notice = await f.page.getByText("Checking access…").count()
    await release()
    await until("the field is shown again", () => box.evaluate((el) => !el.closest('[aria-hidden="true"]')), BOUND.returnMs + 15_000)
    expect(notice, "a slow check says what it is doing").toBe(1)
    expect(await box.inputValue(), "typing during the check changed nothing").toBe(before)
    expect(await box.evaluate((el) => document.activeElement === el), "focus comes back with the read").toBe(true)
  })

  test("Project: a check still waiting takes no keyboard press of a button", { annotation: prdRequirement(id("return-pending-button"), "Project: while the read of a return is pending a focused button cannot be pressed from the keyboard") }, async ({ prd, request }) => {
    const f = await holdingProject(prd, request, cleanup, ["write"], fullChromium(prd, cleanup))
    const from = (await readArtifact(request, f.workspace, f.doc.pageId)).latest_version
    await f.page.getByRole("button", { name: "Save project", exact: true }).focus()
    const release = await holdReturnRead(f.page, "**/artifact?*")
    await awayAndBack(f)
    await pause(500)
    await f.page.keyboard.press("Enter")
    await pause(2_000)
    await release()
    await pause(1_000)
    expect((await readArtifact(request, f.workspace, f.doc.pageId)).latest_version, "nothing was saved during the check").toBe(from)
  })
})
