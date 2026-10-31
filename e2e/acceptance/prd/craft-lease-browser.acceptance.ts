// PRD A: native Word editor lease, driven in two real browsers (the owner and a
// second person with a write grant) on one Word page. craft-lease.acceptance.ts
// proves the lease protocol over the editor API; these rows prove what the
// people see and what their tabs do around a change of custody:
//
// - browser-old-generation: after a handoff the former holder's tab is read-only
//   and says who edits; typing there sends no save and the document version
//   does not move (its old generation is also refused by the server).
// - browser-new-holder: the new holder types, the change saves, and the other
//   person's tab loads that saved version.
// - browser-old-draft: an unsaved local draft of the former holder survives the
//   change of custody. The UI handoff flushes before it transfers (an
//   unconfirmed save refuses the handoff), so the only way custody moves past
//   unsaved work is the holder losing its connection: its tab captures the
//   draft, the lease expires, the other person takes over. The draft stays
//   offered to its author (copy in the retired tab; recovery row once they take
//   the editor again) and is never written to the server or shown to the other
//   person.
// - browser-reconnect: going offline and back online, and a reload, never
//   make the former holder's tab a writer again.
//
// Full mode only: needs the deployed native bundles on the instance's public
// origin (the editor iframe is allowed only there by the CSP frame-src).
import type { APIRequestContext, BrowserContext, Page, Route } from "@playwright/test"
import { loginStorageState } from "./auth"
import { captureAdmission, isCraft, PEER_NAME, peerApi, requireNative, traceCraft, type CraftCall } from "./craft-lease/helpers"
import { NativePage, type Admission, type LeaseView } from "./craft-lease/native"
import { expect, prdRequirement, test, type PrdScenario } from "./fixtures"
import * as N from "./outage-lostack/native"

const REQ = {
  oldGeneration: prdRequirement(
    "A.craft.lease.browser-old-generation",
    "Word, two browsers: after a handoff the former holder's tab shows the new holder, is read-only, and typing there saves nothing; the version is unchanged",
  ),
  newHolder: prdRequirement(
    "A.craft.lease.browser-new-holder",
    "Word, two browsers: after a handoff the new holder types, the change saves, and the former holder's tab loads that saved version",
  ),
  oldDraft: prdRequirement(
    "A.craft.lease.browser-old-draft",
    "Word, two browsers: the former holder's unsaved local draft survives the change of custody, stays offered to its author, and is never saved or shown to the other person",
  ),
  reconnect: prdRequirement(
    "A.craft.lease.browser-reconnect",
    "Word, two browsers: offline/online and a reload never reactivate the former holder's tab as writer; it stays read-only and cannot save",
  ),
}

const APP = "wordcraft" as const
const CANVAS = { x: 678, y: 324 }
const tag = () => Math.random().toString(36).slice(2, 7)
const bar = (page: Page) => page.getByRole("toolbar", { name: "Úpravy dokumentu" })
const button = (scope: Page | ReturnType<typeof bar>, name: string) => scope.getByRole("button", { name, exact: true })

interface Party {
  ctx: BrowserContext
  page: Page
  ops: N.Op[]
  /** Every GET .../craft/document reply the tab received (version, whether it holds each marker). */
  reads: Array<{ at: string; version: string; text: string }>
}

interface Setup {
  doc: NativePage
  ref: N.NativeRef
  peerCtxApi: APIRequestContext
  observer: Admission
  owner: Party
  peer: Party
  ownerAdmission: { current(): Admission | null }
  gOwner: string
  log: Record<string, unknown>
}

function party(ctx: BrowserContext, page: Page, who: string, trail: CraftCall[]): Party {
  page.setDefaultTimeout(60_000)
  traceCraft(page, who, trail)
  const reads: Party["reads"] = []
  ctx.on("response", (r) => {
    if (r.request().method() !== "GET" || !new URL(r.url()).pathname.endsWith("/craft/document") || r.status() !== 200) return
    void r
      .json()
      .then((b: { version?: unknown; bytes?: string }) => {
        if (b.version !== undefined && typeof b.bytes === "string") reads.push({ at: new Date().toISOString(), version: String(b.version), text: Buffer.from(b.bytes, "base64").toString("utf8") })
      })
      .catch(() => {})
  })
  return { ctx, page, ops: N.instrument(ctx), reads }
}

/** Takes the editor through the UI and returns the generation the server granted. */
async function acquireUi(page: Page): Promise<{ generation: string }> {
  const res = page.waitForResponse((r) => isCraft(r, "lease/acquire"), { timeout: 60_000 })
  await N.acquire(page, APP)
  const acquired = await res
  expect(acquired.status(), "lease/acquire from the editor").toBe(200)
  const generation = ((await acquired.json()) as LeaseView).generation
  expect(generation, "acquire granted a generation").toMatch(/^[1-9][0-9]*$/)
  return { generation: generation! }
}

/** Types like a person would: click into the page, new paragraph, text, Ctrl+S. The click may be refused by a frozen frame. */
async function typeAsPerson(page: Page, text: string): Promise<{ clicked: boolean }> {
  let clicked = true
  try {
    const f = await N.frameOf(page, APP)
    await f.locator("canvas").first().click({ position: CANVAS, timeout: 10_000 })
  } catch {
    clicked = false
  }
  await page.keyboard.press("Control+End")
  await page.keyboard.press("Enter")
  await page.keyboard.type(text)
  await page.keyboard.press("Control+s")
  return { clicked }
}

const savesSince = (ops: N.Op[], from: number) => ops.slice(from).filter((o) => o.kind === "save" || o.kind === "reconcile")

/**
 * Two people on one new Word page: the owner takes the editor and saves a
 * marker; the peer (write grant) asks for the editor; the owner hands it over
 * in the UI; the peer binds it. Returns both tabs, still open.
 */
async function handedOver(prd: PrdScenario, request: APIRequestContext, cleanup: Array<() => Promise<void>>, trail: CraftCall[], m: { owner: string }): Promise<Setup> {
  prd.requireFull("needs the deployed native editor bundles on the public origin")
  const ws = await prd.qaWorkspace()
  const doc = await NativePage.create(request, ws.id, APP, "PRD lease browser Word")
  await requireNative(prd, request, doc)
  const { peer: peerPerson, state, api: peerCtxApi } = await peerApi(prd, cleanup)
  await doc.grant(request, peerPerson.userId, "write")
  const ref: N.NativeRef = { pageId: doc.pageId, workspaceId: ws.id, initVersion: "1" }
  const log: Record<string, unknown> = { application: APP, mode: prd.mode, origin: prd.origin, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId }

  // A fresh owner login: contexts that share a refresh family revoke each other.
  const ownerCtx = await prd.browserContext(await loginStorageState(prd.origin, prd.requireAccount("owner")))
  const owner = party(ownerCtx, await ownerCtx.newPage(), "owner", trail)
  const ownerAdmission = captureAdmission(owner.page)
  const peerCtx = await prd.browserContext(state)
  const peer = party(peerCtx, await peerCtx.newPage(), "peer", trail)
  await N.openEditor(owner.page, ref)
  await N.openEditor(peer.page, ref)
  await expect(bar(owner.page)).toContainText("Volné", { timeout: 60_000 })

  const { generation: gOwner } = await acquireUi(owner.page)
  await N.typeMarker(owner.page, await N.frameOf(owner.page, APP), APP, m.owner, "A1")
  await owner.page.keyboard.press("Control+s")
  const ownerSaved = await N.waitSaved(owner.page)
  expect(ownerSaved, "the holder's change saves").toBeTruthy()
  await expect(bar(peer.page)).toContainText(/Upravuje /, { timeout: 30_000 })

  await button(bar(peer.page), "Požádat o předání").click()
  const handTo = button(bar(owner.page), `Předat uživateli ${PEER_NAME}`)
  await expect(handTo, "the holder sees the handoff request").toBeVisible({ timeout: 30_000 })
  const handoffRes = owner.page.waitForResponse((r) => isCraft(r, "lease/handoff"), { timeout: 30_000 })
  const refused = owner.page.getByRole("alert").filter({ hasText: "Výsledek akce nebyl potvrzen" })
  await handTo.click()
  const outcome = await Promise.race([
    handoffRes.then((r) => r.status()),
    refused.waitFor({ timeout: 30_000 }).then(async () => `refused in the UI: ${(await refused.innerText()).trim()}`),
  ]).catch((e: Error) => `no lease/handoff request: ${e.message.split("\n")[0]}`)
  // Known product defect (#4693): after the holder's edit is saved, the
  // pre-transfer flush sends nothing, gets no receipt, and the host refuses the
  // handoff without asking the server.
  expect(outcome, "the holder hands the editor over after saving an edit (fails on #4693: an unchanged editor's handoff is refused)").toBe(200)
  await expect(bar(owner.page)).toContainText(`Upravuje ${PEER_NAME}`, { timeout: 30_000 })
  await expect(button(bar(peer.page), "Uvolnit úpravy")).toBeVisible({ timeout: 30_000 })
  await acquireUi(peer.page)

  const observer = await doc.session(peerCtxApi)
  const lease = await doc.lease(peerCtxApi, observer)
  expect(lease.holder?.actor_id, "the server names the peer as holder").toBe(`user:${peerPerson.userId}`)
  expect(lease.generation).not.toBe(gOwner)
  log.handoff = { ownerSaved, gOwner, gPeer: lease.generation, holder: lease.holder?.display_name }
  return { doc, ref, peerCtxApi, observer, owner, peer, ownerAdmission, gOwner, log }
}

/** The former holder's tab: who it says edits, its status, and which write controls it offers. */
async function formerHolderView(page: Page) {
  return {
    bar: (await bar(page).innerText().catch(() => "?")).replace(/\s+/g, " ").trim(),
    status: await N.status(page),
    release: await button(bar(page), "Uvolnit úpravy").count(),
    take: await button(page, "Převzít úpravy").count(),
    handTo: await bar(page).getByRole("button", { name: /^Předat uživateli / }).count(),
    alerts: (await page.getByRole("alert").allInnerTexts().catch(() => [] as string[])).filter(Boolean),
    at: new Date().toISOString(),
  }
}

function expectNotWriter(view: Awaited<ReturnType<typeof formerHolderView>>, when: string) {
  expect(view.bar, `${when}: the former holder's tab names the new holder`).toContain(`Upravuje ${PEER_NAME}`)
  expect(view.release, `${when}: no "Uvolnit úpravy" in the former holder's tab`).toBe(0)
  expect(view.handTo, `${when}: no "Předat uživateli …" in the former holder's tab`).toBe(0)
  expect(view.take, `${when}: no "Převzít úpravy" in the former holder's tab`).toBe(0)
  expect(view.status, `${when}: the former holder's tab is not writable`).not.toMatch(/Připraveno k úpravám|Uloženo|Ukládám|Neuložený koncept/)
}

async function finish(prd: PrdScenario, log: Record<string, unknown>, trail: CraftCall[], shots: Record<string, Buffer>, passed: boolean) {
  log.passed = passed
  await prd.evidence(passed ? "result.json" : "failure-result.json", log).catch(() => {})
  for (const [name, png] of Object.entries(shots)) await prd.evidence(name, png).catch(() => {})
  if (trail.length) await prd.evidence(passed ? "craft-calls.json" : "failure-craft-calls.json", trail).catch(() => {})
}

async function shot(shots: Record<string, Buffer>, name: string, page: Page | undefined) {
  if (!page) return
  const png = await page.screenshot({ fullPage: true }).catch(() => undefined)
  if (png) shots[name] = png
}

test.describe("Word lease in two browsers", () => {
  test(
    "after a handoff the former holder's tab does not write",
    { annotation: REQ.oldGeneration },
    async ({ prd, request }) => {
      const cleanup: Array<() => Promise<void>> = []
      const trail: CraftCall[] = []
      const shots: Record<string, Buffer> = {}
      const M = { owner: `OWNERsaved${tag()}`, old: `OLDtab${tag()}` }
      let log: Record<string, unknown> = { markers: M }
      let s: Setup | undefined
      let passed = false
      try {
        s = await handedOver(prd, request, cleanup, trail, M)
        log = { ...s.log, markers: M }
        await expect(s.owner.page.getByRole("status").filter({ hasText: "Pouze čtení" }).first(), "the former holder's status says read-only").toBeVisible({ timeout: 30_000 })
        const head = await N.readHead(request, s.ref)
        const opsAt = s.owner.ops.length
        const before = await formerHolderView(s.owner.page)
        log.beforeTyping = { view: before, headVersion: head.version }
        await shot(shots, "former-holder-after-handoff.png", s.owner.page)

        const typed = await typeAsPerson(s.owner.page, M.old)
        await N.pause(8_000)
        const after = await formerHolderView(s.owner.page)
        const headAfter = await N.readHead(request, s.ref)
        const sent = savesSince(s.owner.ops, opsAt)
        await shot(shots, "former-holder-after-typing.png", s.owner.page)

        // The old generation itself, from the former holder's own browser session.
        const admission = s.ownerAdmission.current()
        expect(admission, "the former holder's editor was admitted").toBeTruthy()
        const stale = await s.doc.save(s.owner.ctx.request, admission!, {
          generation: s.gOwner,
          baseVersion: headAfter.version,
          enginePin: (await s.doc.document(s.peerCtxApi, s.observer)).enginePin,
          bytes: Buffer.from(headAfter.text, "utf8"),
          editorRevision: `prd-browser-stale-${Date.now()}`,
        })
        const headFinal = await N.readHead(request, s.ref)
        log.afterTyping = { typed, view: after, savesSent: sent, headVersion: headAfter.version, headHasOldTabText: headAfter.text.includes(M.old) }
        log.staleGenerationSave = { status: stale.status(), headVersion: headFinal.version }

        expectNotWriter(before, "after the handoff")
        expectNotWriter(after, "after typing in the former holder's tab")
        expect(sent, "typing in the former holder's tab sends no save or reconcile").toEqual([])
        expect(headAfter.version, "the document version is unchanged by the former holder's tab").toBe(head.version)
        expect(headAfter.text.includes(M.old), "the former holder's typing is not in the document").toBe(false)
        expect(stale.status(), "a save under the former holder's generation is refused").toBe(409)
        expect(headFinal.version, "the refused save left the version unchanged").toBe(head.version)
        passed = true
      } finally {
        if (!passed) await shot(shots, "failure-former-holder.png", s?.owner.page)
        await finish(prd, log, trail, shots, passed)
        for (const c of cleanup.reverse()) await c().catch(() => {})
      }
    },
  )

  test(
    "after a handoff the new holder saves and the former holder sees it",
    { annotation: REQ.newHolder },
    async ({ prd, request }) => {
      const cleanup: Array<() => Promise<void>> = []
      const trail: CraftCall[] = []
      const shots: Record<string, Buffer> = {}
      const M = { owner: `OWNERsaved${tag()}`, peer: `PEERnew${tag()}` }
      let log: Record<string, unknown> = { markers: M }
      let s: Setup | undefined
      let passed = false
      try {
        s = await handedOver(prd, request, cleanup, trail, M)
        log = { ...s.log, markers: M }
        const head = await N.readHead(request, s.ref)
        const ownerOpsAt = s.owner.ops.length
        await N.typeMarker(s.peer.page, await N.frameOf(s.peer.page, APP), APP, M.peer, "A2")
        await s.peer.page.keyboard.press("Control+s")
        const saved = await N.waitSaved(s.peer.page)
        const headAfter = await N.readHead(request, s.ref)
        log.peerSaved = { label: saved ?? null, headBefore: head.version, headAfter: headAfter.version, headHasPeerText: headAfter.text.includes(M.peer), headHasOwnerText: headAfter.text.includes(M.owner) }
        await shot(shots, "new-holder-saved.png", s.peer.page)
        expect(saved, "the new holder's editor reports the save").toBe(headAfter.version)
        expect(BigInt(headAfter.version), "the new holder's save is a new version").toBeGreaterThan(BigInt(head.version))
        expect(headAfter.text.includes(M.peer), "the new holder's text is in the saved document").toBe(true)
        expect(headAfter.text.includes(M.owner), "the former holder's saved text is kept").toBe(true)

        // The former holder's tab reloads that version on its own (lease heartbeat → reader refresh).
        await expect
          .poll(() => s!.owner.reads.some((r) => r.version === headAfter.version && r.text.includes(M.peer)), { timeout: 45_000, message: "the former holder's tab loads the new holder's saved version" })
          .toBe(true)
        await N.pause(1_500)
        await shot(shots, "former-holder-sees-new-version.png", s.owner.page)
        const ownerView = await formerHolderView(s.owner.page)
        log.formerHolder = { view: ownerView, reads: s.owner.reads.map((r) => ({ at: r.at, version: r.version, hasPeerText: r.text.includes(M.peer) })), savesSent: savesSince(s.owner.ops, ownerOpsAt) }
        expectNotWriter(ownerView, "after the new holder saved")
        expect(savesSince(s.owner.ops, ownerOpsAt), "the former holder's tab sends nothing while it follows").toEqual([])
        passed = true
      } finally {
        if (!passed) {
          await shot(shots, "failure-new-holder.png", s?.peer.page)
          await shot(shots, "failure-former-holder.png", s?.owner.page)
        }
        await finish(prd, log, trail, shots, passed)
        for (const c of cleanup.reverse()) await c().catch(() => {})
      }
    },
  )

  test(
    "the former holder's unsaved draft survives the change of custody",
    { annotation: REQ.oldDraft },
    async ({ prd, request, browser }) => {
      prd.requireFull("needs the deployed native editor bundles on the public origin")
      expect(prd.origin.startsWith("https://"), "the outage proxy cuts CONNECT tunnels; the origin must be https").toBe(true)
      const cleanup: Array<() => Promise<void>> = []
      const trail: CraftCall[] = []
      const shots: Record<string, Buffer> = {}
      const M = { saved: `OWNERsaved${tag()}`, draft: `OWNERdraft${tag()}`, peer: `PEERnew${tag()}` }
      const log: Record<string, unknown> = { application: APP, mode: prd.mode, origin: prd.origin, appSha: prd.appSha, markers: M }
      const proxy = await N.startProxy()
      cleanup.push(async () => proxy.close())
      let owner: Party | undefined
      let peer: Party | undefined
      let passed = false
      try {
        const ws = await prd.qaWorkspace()
        const doc = await NativePage.create(request, ws.id, APP, "PRD lease browser draft Word")
        await requireNative(prd, request, doc)
        const { peer: peerPerson, state, api: peerCtxApi } = await peerApi(prd, cleanup)
        await doc.grant(request, peerPerson.userId, "write")
        const ref: N.NativeRef = { pageId: doc.pageId, workspaceId: ws.id, initVersion: "1" }
        Object.assign(log, { workspaceId: ws.id, pageId: doc.pageId })

        // The owner's browser goes through a proxy that can cut every tunnel, the lease stream included.
        const ownerCtx = await browser.newContext({
          baseURL: prd.origin,
          storageState: await loginStorageState(prd.origin, prd.requireAccount("owner")),
          viewport: N.VIEWPORT,
          proxy: { server: proxy.server },
          acceptDownloads: true,
        })
        cleanup.push(() => ownerCtx.close())
        owner = party(ownerCtx, await ownerCtx.newPage(), "owner", trail)
        const peerCtx = await prd.browserContext(state)
        peer = party(peerCtx, await peerCtx.newPage(), "peer", trail)

        // Saves the owner's tab sends while `hold` is on never reach the server.
        let hold = false
        const held: Route[] = []
        await ownerCtx.route("**/craft/document/save**", async (route) => {
          if (!hold) return route.continue()
          held.push(route)
        })

        await N.openEditor(owner.page, ref)
        await N.openEditor(peer.page, ref)
        await acquireUi(owner.page)
        await N.typeMarker(owner.page, await N.frameOf(owner.page, APP), APP, M.saved, "A1")
        await owner.page.keyboard.press("Control+s")
        const savedVersion = await N.waitSaved(owner.page)
        expect(savedVersion, "the owner's first change saves").toBeTruthy()

        // Unsaved work: the autosave of this paragraph is held, then the connection drops.
        hold = true
        await N.typeMarker(owner.page, await N.frameOf(owner.page, APP), APP, M.draft, "A2")
        await expect.poll(() => held.length, { timeout: 20_000, message: "the editor tried to save the unsaved paragraph" }).toBeGreaterThan(0)
        const severed = proxy.down()
        const cutAt = Date.now()
        for (const r of held) await r.abort("internetdisconnected").catch(() => {})
        await expect
          .poll(async () => (await N.uiState(owner!.page)).recoveries.length, { timeout: 60_000, message: "the disconnected tab offers its captured local draft" })
          .toBeGreaterThan(0)
        const retired = await N.uiState(owner.page)
        log.disconnected = { severedTunnels: severed, heldSaves: held.length, ui: retired, retiredAfterMs: Date.now() - cutAt }
        await shot(shots, "former-holder-disconnected-draft.png", owner.page)

        // The draft as its author can take it from the retired tab ("Kopie konceptu").
        const download = owner.page.waitForEvent("download", { timeout: 20_000 })
        await button(owner.page, "Kopie konceptu").first().click()
        const file = await (await download).createReadStream()
        const chunks: Buffer[] = []
        for await (const c of file) chunks.push(Buffer.from(c as Buffer))
        const copied = Buffer.concat(chunks).toString("utf8")
        log.draftCopy = { bytes: copied.length, hasDraft: copied.includes(M.draft), hasSaved: copied.includes(M.saved) }
        expect(copied.includes(M.draft), "the copied local draft holds the unsaved paragraph").toBe(true)

        // Custody moves on: the lease expires without its stream and the peer takes the editor.
        const observer = await doc.session(peerCtxApi)
        await expect
          .poll(async () => (await doc.lease(peerCtxApi, observer)).holder === null, { timeout: 60_000, intervals: [1_000], message: "the disconnected holder's lease expires" })
          .toBe(true)
        await expect(bar(peer.page)).toContainText("Volné", { timeout: 30_000 })
        await acquireUi(peer.page)
        await N.typeMarker(peer.page, await N.frameOf(peer.page, APP), APP, M.peer, "A3")
        await peer.page.keyboard.press("Control+s")
        const peerSaved = await N.waitSaved(peer.page)
        const headPeer = await N.readHead(request, ref)
        const peerUi = await N.uiState(peer.page)
        const peerSeesDraft = (await peer.page.content()).includes(M.draft) || peer.reads.some((r) => r.text.includes(M.draft))
        log.peer = { saved: peerSaved ?? null, head: headPeer.version, headHasDraft: headPeer.text.includes(M.draft), headHasPeer: headPeer.text.includes(M.peer), ui: peerUi, seesDraft: peerSeesDraft }
        expect(peerSaved, "the new holder saves").toBe(headPeer.version)
        expect(headPeer.text.includes(M.draft), "the former holder's unsaved draft was never written to the server").toBe(false)
        expect(peerUi.recoveries, "the other person is offered no local draft").toEqual([])
        expect(peerSeesDraft, "the other person's tab never receives the former holder's draft").toBe(false)

        // Back online, the author reopens the page in the same browser profile.
        proxy.up()
        await owner.page.reload()
        await owner.page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
        await N.frameOf(owner.page, APP)
        await N.pause(3_000)
        const reopened = { view: await formerHolderView(owner.page), ui: await N.uiState(owner.page) }
        log.reopenedWhilePeerHolds = reopened
        await shot(shots, "former-holder-reopened-peer-holds.png", owner.page)
        expectNotWriter(reopened.view, "after reopening while the peer holds the editor")

        // The peer leaves (its lease expires without a stream); the author takes
        // the editor again and gets the draft offered, not applied. Leaving, not
        // "Uvolnit úpravy": releasing after a save is refused by #4693, which
        // browser-old-generation/new-holder/reconnect already report.
        await peer.page.close()
        await expect
          .poll(async () => (await doc.lease(peerCtxApi, observer)).holder === null, { timeout: 60_000, intervals: [1_000], message: "the peer's lease expires after its tab closed" })
          .toBe(true)
        await expect(bar(owner.page)).toContainText("Volné", { timeout: 30_000 })
        const opsAt = owner.ops.length
        await acquireUi(owner.page)
        await expect
          .poll(async () => (await N.uiState(owner!.page)).recoveries.length, { timeout: 30_000, message: "the author's local draft is offered again after taking the editor" })
          .toBeGreaterThan(0)
        const comparisons = await N.compareRecoveries(owner.page, { draft: M.draft, peer: M.peer })
        await N.pause(2_000)
        const headReacquired = await N.readHead(request, ref)
        await shot(shots, "author-draft-offered.png", owner.page)
        log.reacquired = { ui: await N.uiState(owner.page), comparisons, savesSent: savesSince(owner.ops, opsAt), head: headReacquired.version, headHasDraft: headReacquired.text.includes(M.draft) }
        expect(comparisons.some((c) => c.draftHas.draft && !c.equal), "a recovery row holds the unsaved paragraph and differs from the server").toBe(true)
        expect(savesSince(owner.ops, opsAt), "the draft is offered, not saved automatically").toEqual([])
        expect(headReacquired.version, "taking the editor again does not write the draft").toBe(headPeer.version)
        expect(headReacquired.text.includes(M.draft)).toBe(false)
        passed = true
      } finally {
        proxy.up()
        if (!passed) {
          await shot(shots, "failure-owner.png", owner?.page)
          await shot(shots, "failure-peer.png", peer?.page)
        }
        await finish(prd, log, trail, shots, passed)
        for (const c of cleanup.reverse()) await c().catch(() => {})
      }
    },
  )

  test(
    "offline, online and reload do not make the former holder a writer again",
    { annotation: REQ.reconnect },
    async ({ prd, request }) => {
      const cleanup: Array<() => Promise<void>> = []
      const trail: CraftCall[] = []
      const shots: Record<string, Buffer> = {}
      const M = { owner: `OWNERsaved${tag()}`, offline: `OLDoffline${tag()}`, reload: `OLDreload${tag()}`, peer: `PEERstill${tag()}` }
      let log: Record<string, unknown> = { markers: M }
      let s: Setup | undefined
      let passed = false
      try {
        s = await handedOver(prd, request, cleanup, trail, M)
        log = { ...s.log, markers: M }
        const head = await N.readHead(request, s.ref)
        const opsAt = s.owner.ops.length

        await s.owner.ctx.setOffline(true)
        await N.pause(10_000)
        const offline = await formerHolderView(s.owner.page)
        await s.owner.ctx.setOffline(false)
        await N.pause(10_000)
        const online = await formerHolderView(s.owner.page)
        await shot(shots, "former-holder-back-online.png", s.owner.page)
        const typedOnline = await typeAsPerson(s.owner.page, M.offline)
        await N.pause(6_000)
        const afterTyping = await formerHolderView(s.owner.page)

        await s.owner.page.reload()
        await s.owner.page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
        await N.frameOf(s.owner.page, APP)
        await expect(bar(s.owner.page)).toContainText(`Upravuje ${PEER_NAME}`, { timeout: 30_000 })
        const reloaded = await formerHolderView(s.owner.page)
        const typedReload = await typeAsPerson(s.owner.page, M.reload)
        await N.pause(6_000)
        const afterReloadTyping = await formerHolderView(s.owner.page)
        await shot(shots, "former-holder-after-reload.png", s.owner.page)
        const headOwner = await N.readHead(request, s.ref)
        const sent = savesSince(s.owner.ops, opsAt)
        const lease = await s.doc.lease(s.peerCtxApi, s.observer)

        // The peer is still the writer: one more save from the peer's tab goes through.
        await N.typeMarker(s.peer.page, await N.frameOf(s.peer.page, APP), APP, M.peer, "A2")
        await s.peer.page.keyboard.press("Control+s")
        const peerSaved = await N.waitSaved(s.peer.page)
        const headFinal = await N.readHead(request, s.ref)
        log.reconnect = {
          offline, online, typedOnline, afterTyping, reloaded, typedReload, afterReloadTyping,
          savesSent: sent,
          headBefore: head.version, headAfterOwner: headOwner.version,
          headHasOwnerTyping: { offline: headOwner.text.includes(M.offline), reload: headOwner.text.includes(M.reload) },
          leaseHolder: lease.holder, peerSaved: peerSaved ?? null, headFinal: headFinal.version, headFinalHasPeer: headFinal.text.includes(M.peer),
        }

        expectNotWriter(online, "back online")
        expectNotWriter(afterTyping, "typing after reconnecting")
        expectNotWriter(reloaded, "after a reload")
        expectNotWriter(afterReloadTyping, "typing after a reload")
        expect(sent, "the former holder's tab sends no save or reconcile across offline, online and reload").toEqual([])
        expect(headOwner.version, "the document version is unchanged by the former holder").toBe(head.version)
        expect(headOwner.text.includes(M.offline) || headOwner.text.includes(M.reload), "the former holder's typing is not in the document").toBe(false)
        expect(lease.holder?.display_name, "the peer still holds the editor").toBe(PEER_NAME)
        expect(peerSaved, "the peer still saves").toBe(headFinal.version)
        expect(headFinal.text.includes(M.peer)).toBe(true)
        passed = true
      } finally {
        if (!passed) await shot(shots, "failure-former-holder.png", s?.owner.page)
        await s?.owner.ctx.setOffline(false).catch(() => {})
        await finish(prd, log, trail, shots, passed)
        for (const c of cleanup.reverse()) await c().catch(() => {})
      }
    },
  )
})
