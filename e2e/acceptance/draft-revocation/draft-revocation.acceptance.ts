// What happens to an unsaved native draft when access changes, in real Chromium
// against a real server. Delivery, the client's reaction and reconnect are timed
// apart, because a test that only sees the final state cannot tell them apart:
//
//   T0 the server committed the change (the owner's call returned)
//   T1 delivery: the browser read session_retired from the stream
//   T2 reaction: content hidden, and the draft's rows erased or still there
//   T3 reconnect: the browser went back online and acted on what it learned
//
// Needs a server this run may touch. REVOCATION_DB names its database, and is
// how the expiry case ends a session: a real expiry would take the session's
// whole lifetime. Never point it at a shared server. The offline case needs
// PRD_ORIGIN behind revocation-proxy.mjs: REVOCATION_PROXY_CONTROL cuts the
// connections, and REVOCATION_DIRECT reaches the server around the proxy.
//
// Targeted pre-RC proof, not a row of the PRD suite: it cannot run against a
// deployed origin. See "Draft revocation proof" in docs/development/prd-acceptance.md.
import { chromium, request as requests, type Browser, type BrowserContext, type Page } from "@playwright/test"
import { execFileSync } from "node:child_process"
import { acquire, editorURL, frameOf, recoveryRows, takeButton, typeMarker } from "../prd/drafts-crash/native"
import { pause } from "../prd/drafts-crash/crash"
import { peerApi, requireNative } from "../prd/craft-lease/helpers"
import { NativePage } from "../prd/craft-lease/native"
import { expect, prdRequirement, test } from "../prd/fixtures"
import type { StorageState } from "../prd/auth"

const application = "wordcraft" as const
const BOUND = {
  deliveryMs: 7_000, // the 5 s event interval plus delivery
  reactionMs: 3_000,
  reconnectMs: 60_000, // NATIVE_ADMISSION_DEADLINE_MS
}
const REFUSED = /Právo upravovat bylo odebráno/

/** Records every frame of the host's own metadata stream as it reaches the page. */
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

/** Rows of this Page in the browser's draft journal. Sealed: only their count is read. */
const draftRows = (page: Page, pageId: string) => page.evaluate((id) => new Promise<number>((resolve, reject) => {
  const open = indexedDB.open("crewship-document-drafts")
  open.onerror = () => reject(open.error)
  open.onsuccess = () => {
    const db = open.result
    if (!db.objectStoreNames.length) { db.close(); resolve(0); return }
    const all = db.transaction(db.objectStoreNames[0]).objectStore(db.objectStoreNames[0]).getAllKeys()
    all.onerror = () => { db.close(); reject(all.error) }
    all.onsuccess = () => { db.close(); resolve(all.result.filter((k) => String(k).includes(id)).length) }
  }
}), pageId)

/** The rows' opaque ids and revisions: which row is which, never what it holds. */
const rowIds = (page: Page, pageId: string) => page.evaluate((id) => new Promise<string[]>((resolve) => {
  const open = indexedDB.open("crewship-document-drafts")
  open.onsuccess = () => {
    const db = open.result
    const all = db.transaction("drafts").objectStore("drafts").getAll()
    all.onsuccess = () => { db.close(); resolve(all.result.filter((r) => String(r.key).includes(id)).map((r) => `${String(r.key[1]).slice(-8)}/${String(r.revision).slice(-8)}/${r.draft ? "draft" : "empty"}`)) }
  }
}), pageId)

const contentHidden = (page: Page) => page.evaluate(() => {
  const frame = document.querySelector<HTMLElement>('iframe[title="WordCraft editor"]')
  return !frame || getComputedStyle(frame).visibility === "hidden"
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

interface Fixture { browser: Browser; context: BrowserContext; page: Page; doc: NativePage; peer: { userId: string; account: { email: string; password: string } }; workspace: string; state: StorageState }

const launch = () => chromium.launch({ headless: true })

/** Opens the editor in a tab of `context` and records its stream. */
async function open(context: BrowserContext, doc: NativePage, workspace: string): Promise<Page> {
  const page = await context.newPage()
  page.setDefaultTimeout(60_000)
  await page.goto(editorURL(doc.pageId, workspace))
  await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
  return page
}

/** A person with `levels` holds the editor and has typed work no save can confirm. */
async function holding(prd: Parameters<Parameters<typeof test>[2]>[0]["prd"], request: Parameters<Parameters<typeof test>[2]>[0]["request"], cleanup: Array<() => Promise<void>>, levels: Array<"read" | "comment" | "write">, browser: Browser): Promise<Fixture> {
  const ws = await prd.qaWorkspace()
  const doc = await NativePage.create(request, ws.id, application, "PRD draft revocation")
  await requireNative(prd, request, doc)
  const { peer, state } = await peerApi(prd, cleanup)
  for (const level of levels) await doc.grant(request, peer.userId, level)
  const context = await browser.newContext({ baseURL: prd.origin, storageState: state, viewport: { width: 1440, height: 1000 } })
  cleanup.push(() => context.close())
  await context.addInitScript(recordStream)
  // A held save is what leaves the draft unconfirmed; nothing else changes.
  await context.route("**/craft/document/save*", () => {})
  const page = await open(context, doc, ws.id)
  const frame = await acquire(page, application)
  await typeMarker(page, frame, application, `DRAFT${Date.now().toString(36)}`, 3)
  await until("the draft reached the journal", async () => (await draftRows(page, doc.pageId)) > 0, 30_000)
  await frameOf(page, application)
  return { browser, context, page, doc, peer, workspace: ws.id, state }
}

test.describe("native draft on access change", () => {
  test.setTimeout(12 * 60_000)
  let browser: Browser
  const cleanup: Array<() => Promise<void>> = []
  test.beforeAll(async () => { browser = await launch() })
  test.afterEach(async () => { for (const c of cleanup.splice(0).reverse()) await c().catch(() => {}) })
  test.afterAll(async () => { await browser.close() })

  test("read lost while the editor is open", { annotation: prdRequirement("A.craft.draft.read-loss-open", "Word: read removed on an open editor is delivered, hides the content and erases the draft") }, async ({ prd, request }) => {
    const f = await holding(prd, request, cleanup, ["write"], browser)
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

  for (const level of ["read", "comment"] as const) {
    test(`Edit reduced to ${level} keeps the draft`, { annotation: prdRequirement(`A.craft.draft.write-only-${level}`, `Word: Edit reduced to ${level} hides latest content and keeps the draft`) }, async ({ prd, request }) => {
      const f = await holding(prd, request, cleanup, [level, "write"], browser)
      const rows = await draftRows(f.page, f.doc.pageId)
      await f.doc.revoke(request, f.peer.userId, "write")
      const t0 = Date.now()
      const t1 = await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
      const frame = (await retired(f.page))[0]
      await until("the editor says Edit is gone", async () => (await f.page.getByRole("alert").filter({ hasText: REFUSED }).count()) > 0 || (await f.page.getByRole("status").filter({ hasText: /Pouze čtení/ }).count()) > 0, 30_000)
      // Long enough for any late proof to have acted.
      await pause(15_000)
      const after = await draftRows(f.page, f.doc.pageId)
      await prd.evidence("timing.json", { level, rows, after, frame, deliveryMs: t1 - t0, hidden: await contentHidden(f.page) })
      expect(frame).toMatchObject({ draft: "retain", reason: "write_revoked" })
      expect(after, "the draft is kept").toBeGreaterThan(0)
    })
  }

  test("Comment removed after admission stopped erases without a reload", { annotation: prdRequirement("A.craft.draft.comment-to-none", "Word: a draft kept after Edit became Comment is erased when Comment is removed too, with no reload") }, async ({ prd, request }) => {
    const f = await holding(prd, request, cleanup, ["comment", "write"], browser)
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

  test("read lost while offline is learned on reconnect", { annotation: prdRequirement("A.craft.draft.offline-reconnect", "Word: an offline browser keeps the draft until it returns, and then erases it") }, async ({ prd, request }) => {
    const control = process.env.REVOCATION_PROXY_CONTROL, direct = process.env.REVOCATION_DIRECT
    test.skip(!control || !direct, "needs revocation-proxy.mjs in front of the server")
    const f = await holding(prd, request, cleanup, ["write"], browser)
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

  test("session expiry keeps the draft", { annotation: prdRequirement("A.craft.draft.expiry-keeps", "Word: an expired session keeps the draft and the same person gets it back") }, async ({ prd, request }) => {
    const db = process.env.REVOCATION_DB
    test.skip(!db, "needs REVOCATION_DB: a throwaway server's database")
    const f = await holding(prd, request, cleanup, ["write"], browser)
    const rows = await draftRows(f.page, f.doc.pageId)
    execFileSync("sqlite3", [db!, `UPDATE user_sessions SET expires_at='2000-01-01T00:00:00Z' WHERE user_id='${f.peer.userId.replace(/[^a-zA-Z0-9_-]/g, "")}'`])
    const t0 = Date.now()
    const ended = await until("the editor stopped", async () => (await retired(f.page)).length > 0 || /\/login/.test(f.page.url()), 90_000)
    const frame = (await retired(f.page).catch(() => []))[0]
    await pause(5_000)
    const after = await draftRows(f.page, f.doc.pageId)
    await f.page.goto(`/login?redirect=${encodeURIComponent(editorURL(f.doc.pageId, f.workspace))}`)
    await f.page.locator("#email").fill(f.peer.account.email)
    await f.page.locator("#password").fill(f.peer.account.password)
    await f.page.locator('button[type="submit"]').first().click()
    // The journal opens with the editor, so a kept draft comes back on taking it.
    await takeButton(f.page).click({ timeout: 90_000 })
    const offered = await recoveryRows(f.page).first().waitFor({ timeout: 60_000 }).then(() => Date.now(), () => 0)
    await prd.evidence("timing.json", { rows, after, frame, endedMs: ended - t0, url: f.page.url().replace(/\?.*/, ""), offeredAfterLogin: offered > 0 })
    expect(frame?.draft ?? "retain", "the stream never says purge").toBe("retain")
    expect(after, "the draft survives expiry").toBeGreaterThan(0)
    expect(offered, "the same person gets it back").toBeGreaterThan(0)
  })

  for (const tabs of [1, 2]) {
    test(`sign-out erases with ${tabs} tab${tabs > 1 ? "s" : ""} open`, { annotation: prdRequirement(`A.craft.draft.sign-out-${tabs}`, `Word: a confirmed sign-out with ${tabs} tab(s) open erases the draft and stops every tab`) }, async ({ prd, request }) => {
      const f = await holding(prd, request, cleanup, ["write"], browser)
      const other = tabs > 1 ? await open(f.context, f.doc, f.workspace) : null
      await f.page.bringToFront()
      await pause(2_000)
      const rows = await draftRows(f.page, f.doc.pageId)
      const rowsBefore = await rowIds(f.page, f.doc.pageId)
      await f.page.getByRole("button", { name: "User menu" }).first().click()
      await f.page.getByRole("menuitem", { name: /Log out/ }).or(f.page.getByRole("button", { name: /Log out/ })).first().click()
      const confirm = f.page.getByRole("button", { name: "Sign out", exact: true })
      const asked = await confirm.waitFor({ timeout: 5_000 }).then(() => true, () => false)
      if (asked) await confirm.click()
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

  test("read lost with two tabs open erases once and stops both", { annotation: prdRequirement("A.craft.draft.multi-tab", "Word: read removed with two tabs open erases the draft and stops both tabs") }, async ({ prd, request }) => {
    const f = await holding(prd, request, cleanup, ["write"], browser)
    const other = await open(f.context, f.doc, f.workspace)
    await f.doc.revoke(request, f.peer.userId, "write")
    const t0 = Date.now()
    const first = await until("delivery to the editing tab", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 5_000)
    const second = await until("delivery to the other tab", async () => (await retired(other)).length > 0, BOUND.deliveryMs + 5_000)
    const erasedAt = await until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 30_000)
    const hiddenOther = await until("the other tab hides", () => contentHidden(other), 30_000)
    await prd.evidence("timing.json", { deliveryEditingMs: first - t0, deliveryOtherMs: second - t0, erasedMs: erasedAt - t0, hiddenOtherMs: hiddenOther - t0, framesEditing: await retired(f.page), framesOther: await retired(other) })
    expect((await retired(other))[0]).toMatchObject({ draft: "purge" })
  })

  test("a hidden tab reacts like a visible one", { annotation: prdRequirement("A.craft.draft.hidden-tab", "Word: read removed while the tab is hidden is delivered and acted on within the same bounds") }, async ({ prd, request }) => {
    const hiddenForMs = Number(process.env.REVOCATION_HIDDEN_MS ?? 330_000)
    // Only a headed full Chromium hides a background tab, so this case runs under
    // Xvfb. Playwright turns background throttling off by default, which would
    // hide what this case measures. Not yet measured: on the machine that wrote
    // this, full Chromium 1243 and 1248 under software rendering never finish the
    // editor's start, and the headless shell, which does, never hides a tab.
    test.skip(!process.env.REVOCATION_CHROME || !process.env.DISPLAY, "needs REVOCATION_CHROME and a display")
    const full = await chromium.launch({
      executablePath: process.env.REVOCATION_CHROME, headless: false, args: ["--enable-unsafe-swiftshader", "--disable-features=ThrottleDisplayNoneAndVisibilityHiddenCrossOriginIframes"],
      ignoreDefaultArgs: ["--disable-background-timer-throttling", "--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding"],
    })
    cleanup.push(() => full.close())
    const f = await holding(prd, request, cleanup, ["write"], full)
    const front = await f.context.newPage()
    await front.goto("about:blank")
    await front.bringToFront()
    await pause(1_000)
    const state = await f.page.evaluate(() => document.visibilityState)
    await prd.evidence("visibility.json", { state })
    test.skip(state !== "hidden", `the harness did not hide the tab: visibilityState is ${state}`)
    // Past five minutes Chromium throttles hidden timers to once a minute.
    await pause(hiddenForMs)
    await f.doc.revoke(request, f.peer.userId, "write")
    const t0 = Date.now()
    const t1 = await until("delivery", async () => (await retired(f.page)).length > 0, BOUND.deliveryMs + 60_000)
    const erasedAt = await until("draft erased", async () => (await draftRows(f.page, f.doc.pageId)) === 0, 90_000)
    await prd.evidence("timing.json", { state, hiddenForMs, deliveryMs: t1 - t0, erasedMs: erasedAt - t1, stillHidden: await f.page.evaluate(() => document.visibilityState) })
    expect((await retired(f.page))[0]).toMatchObject({ draft: "purge" })
  })
})
