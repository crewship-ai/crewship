// Native Word/Sheet walkthrough (dev3): create from the Artifacts library,
// type without fighting focus, read the save labels, go offline, see who
// edits, request and grant the handoff, lose write access, recover a local
// draft, publish a preview; phone layout for Word and Sheet.
import { request as pwRequest, test, type Page } from "@playwright/test"
import { loginStorageState } from "../acceptance/prd/auth"
import { NativePage } from "../acceptance/prd/craft-lease/native"
import { clickCanvas, createFromLibrary, createNativeViaApi, headIncludes, saveStatus, toolbar, waitEditor } from "./native-helpers"
import { audit, focused, note, pause, shot, tabWalk, type Viewport } from "./audit"
import { browserContext, ORIGIN, ownerApi, ownerStorage, walkPeer, walkWorkspace } from "./session"
import { statusTimeline, step } from "./steps"

test("native Word and Sheet", async ({ browser }) => {
  test.skip(process.env.UX_TARGET !== "native")
  const api = await ownerApi()
  const ws = await walkWorkspace(api)
  const st = await api.get(`/api/v1/documents/status?workspace_id=${ws.id}`)
  note("N00-documents-status", await st.json())
  const peer = await walkPeer(api, ws)
  const peerState = await loginStorageState(ORIGIN, { email: peer.email, password: peer.password })
  const peerApi = await pwRequest.newContext({ baseURL: ORIGIN, storageState: peerState })

  const owner = await browserContext(browser, "desktop", await ownerStorage())
  const page = await owner.newPage()
  const stamp = Date.now().toString(36)
  let wordId = ""

  await step(page, "N01-word-create-desktop", async () => {
    const t0 = Date.now()
    let viaLibrary = true
    try {
      wordId = await createFromLibrary(page, ws.id, "Word document", `PRD UX walkthrough Word ${stamp}`, "desktop", "N01-word")
    } catch (err) {
      viaLibrary = false
      await shot(page, "N01-word-not-offered-in-new-dialog-desktop")
      const coming = await page.getByText(/^Coming:/).innerText().catch(() => "")
      note("N01-word-library-refusal", { error: String(err).split("\n")[0], coming })
      await page.keyboard.press("Escape")
      wordId = await createNativeViaApi(api, ws.id, "wordcraft", `PRD UX walkthrough Word ${stamp}`)
      await page.goto(`/artifacts?workspace_id=${ws.id}`)
      await page.getByRole("tab", { name: /All artifacts/ }).click().catch(() => {})
      await pause(2500)
      await shot(page, "N01-word-in-library-desktop")
      const listed = await page.getByText(`PRD UX walkthrough Word ${stamp}`).first().isVisible().catch(() => false)
      note("N01-word-listed-in-library", listed)
      await page.goto(`/artifacts/${wordId}?workspace_id=${ws.id}`)
    }
    note("N01-word-via-library", viaLibrary)
    const ms = await waitEditor(page)
    const tl = await statusTimeline(page, 6000, saveStatus)
    const a = await audit(page, "N01-word-opened-desktop", "desktop")
    return { createToEditorMs: Date.now() - t0, editorReadyMs: ms, timeline: tl, toolbar: await toolbar(page).innerText().catch(() => ""), focus: await focused(page), unnamed: a.unnamed.map((c) => c.html) }
  })

  await step(page, "N02-word-type-without-click-desktop", async () => {
    // A person who just pressed "Create and edit" starts typing straight away.
    await page.keyboard.type("Ahoj UX bez kliknuti", { delay: 30 })
    const tl = await statusTimeline(page, 10_000, saveStatus)
    await shot(page, "N02-word-typed-without-click-desktop")
    const head = await headIncludes(api, ws.id, wordId, "Ahoj UX bez kliknuti")
    return { focusBeforeTyping: await focused(page), timeline: tl, landedOnServer: head }
  })

  await step(page, "N03-word-type-after-click-desktop", async () => {
    const take = page.getByRole("button", { name: "Převzít úpravy", exact: true })
    const takeVisible = await take.first().isVisible().catch(() => false)
    if (takeVisible) await take.first().click()
    await pause(1200)
    await clickCanvas(page, "wordcraft")
    await pause(400)
    await page.keyboard.press("Control+End")
    await page.keyboard.type("Ahoj UX po kliknuti", { delay: 30 })
    const during = await page.locator(saveStatus).first().innerText().catch(() => "")
    await shot(page, "N03-word-typing-desktop")
    const tl = await statusTimeline(page, 15_000, saveStatus)
    await shot(page, "N03-word-saved-desktop")
    const head = await headIncludes(api, ws.id, wordId, "Ahoj UX po kliknuti")
    return { neededTakeButton: takeVisible, statusWhileTyping: during, timeline: tl, landedOnServer: head }
  })

  await step(page, "N04-word-keyboard-desktop", async () => {
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
    await page.locator("main").first().focus().catch(() => {})
    const stops = await tabWalk(page, "N04-word-desktop", 40)
    return { reachedIframe: stops.some((s) => s.inIframe), stops: stops.map((s) => `${s.tag}:${s.name}:${s.visible ? "ring" : "NO-RING"}`) }
  })

  await step(page, "N05-word-offline-desktop", async () => {
    await clickCanvas(page, "wordcraft")
    await page.keyboard.press("Control+End")
    await owner.setOffline(true)
    await page.keyboard.type(" offline veta", { delay: 30 })
    const off = await statusTimeline(page, 20_000, saveStatus)
    const alerts = await page.getByRole("alert").allInnerTexts()
    await shot(page, "N05-word-offline-desktop")
    await owner.setOffline(false)
    const on = await statusTimeline(page, 30_000, saveStatus)
    await shot(page, "N05-word-reconnected-desktop")
    const head = await headIncludes(api, ws.id, wordId, "offline veta")
    return { offline: off, alertsOffline: alerts, online: on, alertsOnline: await page.getByRole("alert").allInnerTexts(), landedOnServer: head }
  })

  const peerCtx = await browserContext(browser, "desktop", peerState)
  const peerPage = await peerCtx.newPage()
  await step(peerPage, "N06-word-peer-sees-holder-desktop", async () => {
    await new NativePage(ws.id, wordId, "wordcraft").grant(api, peer.userId, "write")
    await pause(1500)
    await peerPage.goto(`/artifacts/${wordId}?workspace_id=${ws.id}`)
    await waitEditor(peerPage)
    await pause(2000)
    const bar = await toolbar(peerPage).innerText()
    await audit(peerPage, "N06-word-peer-sees-holder-desktop", "desktop")
    const ask = peerPage.getByRole("button", { name: "Požádat o předání" })
    await ask.click()
    await pause(1500)
    await shot(peerPage, "N06-word-peer-requested-desktop")
    return { bar, afterRequest: await toolbar(peerPage).innerText(), statusAfterRequest: await peerPage.locator(saveStatus).allInnerTexts(), feedbackLive: await peerPage.locator('[aria-live], [role="status"], [role="alert"]').allInnerTexts() }
  })

  await step(page, "N07-word-owner-sees-request-desktop", async () => {
    const give = page.getByRole("button", { name: /^Předat uživateli/ })
    const t0 = Date.now()
    await give.waitFor({ timeout: 30_000 })
    const shownAfterMs = Date.now() - t0
    await shot(page, "N07-word-owner-sees-request-desktop")
    const label = await give.innerText()
    const toast = await page.locator('[data-sonner-toast], [role="region"][aria-label^="Notifications"] li').allInnerTexts()
    await give.click()
    await pause(2000)
    await shot(page, "N07-word-owner-after-handoff-desktop")
    return { shownAfterMs, label, toast, ownerBarAfter: await toolbar(page).innerText(), ownerStatusAfter: await page.locator(saveStatus).allInnerTexts() }
  })

  await step(peerPage, "N08-word-peer-after-handoff-desktop", async () => {
    const tl = await statusTimeline(peerPage, 12_000, saveStatus)
    const bar = await toolbar(peerPage).innerText()
    const take = peerPage.getByRole("button", { name: "Převzít úpravy", exact: true })
    const needsTake = await take.first().isVisible().catch(() => false)
    await shot(peerPage, "N08-word-peer-after-handoff-desktop")
    if (needsTake) await take.first().click()
    await pause(1500)
    await clickCanvas(peerPage, "wordcraft")
    await peerPage.keyboard.press("Control+End")
    await peerPage.keyboard.type(" veta kolegyne", { delay: 30 })
    const tl2 = await statusTimeline(peerPage, 12_000, saveStatus)
    await shot(peerPage, "N08-word-peer-typed-desktop")
    return { timeline: tl, bar, needsSecondTakeClick: needsTake, afterTyping: tl2, landed: await headIncludes(api, ws.id, wordId, "veta kolegyne") }
  })

  await step(peerPage, "N09-word-peer-revoked-desktop", async () => {
    await clickCanvas(peerPage, "wordcraft")
    await peerPage.keyboard.press("Control+End")
    await peerPage.keyboard.type(" po odebrani", { delay: 20 })
    await new NativePage(ws.id, wordId, "wordcraft").revoke(api, peer.userId, "write")
    const tl = await statusTimeline(peerPage, 25_000, saveStatus)
    const alerts = await peerPage.getByRole("alert").allInnerTexts()
    await audit(peerPage, "N09-word-peer-revoked-desktop", "desktop")
    return { timeline: tl, alerts, bar: await toolbar(peerPage).innerText().catch(() => "") }
  })
  await peerCtx.close()

  await step(page, "N10-word-local-draft-desktop", async () => {
    await page.reload()
    await waitEditor(page)
    await pause(1500)
    const take = page.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    if (await take.isVisible().catch(() => false)) await take.click()
    await page.locator(saveStatus).filter({ hasText: /Připraveno k úpravám|Uloženo/ }).first().waitFor({ timeout: 60_000 })
    await pause(1000)
    await clickCanvas(page, "wordcraft")
    await page.keyboard.press("Control+End")
    await owner.setOffline(true)
    await page.keyboard.type(" koncept jen v prohlizeci", { delay: 30 })
    await pause(4000)
    const before = await page.locator(saveStatus).first().innerText()
    await shot(page, "N10-word-draft-before-close-desktop")
    await page.close()
    await owner.setOffline(false)
    const again = await owner.newPage()
    await again.goto(`/artifacts/${wordId}?workspace_id=${ws.id}`)
    await waitEditor(again)
    await pause(4000)
    const rows = again.getByText(/^Místní koncept · verze/)
    const offered = await rows.count()
    await audit(again, "N10-word-draft-offer-desktop", "desktop")
    let compare: unknown = null
    if (offered > 0) {
      const btn = rows.first().locator("..").getByRole("button", { name: "Porovnat", exact: true })
      await btn.focus()
      await again.keyboard.press("Enter")
      await pause(2500)
      await shot(again, "N10-word-draft-compare-desktop")
      const caption = await again.getByText(/^Serverová verze \d+ a /).innerText().catch(() => "")
      const restoreButtons = await again.getByRole("button").filter({ hasText: /Obnovit|Použít|Restore|Vrátit/ }).allInnerTexts()
      await again.keyboard.press("Escape")
      const closesOnEscape = !(await again.getByRole("button", { name: "Zavřít porovnání" }).isVisible().catch(() => false))
      if (!closesOnEscape) await again.getByRole("button", { name: "Zavřít porovnání" }).click()
      compare = { caption, restoreButtons, closesOnEscape, focusAfterClose: await focused(again) }
    }
    ;(globalThis as { __page?: Page }).__page = again
    return { statusBeforeClose: before, offered, compare, statusAfterReopen: await again.locator(saveStatus).allInnerTexts(), alerts: await again.getByRole("alert").allInnerTexts() }
  })
  const p2 = (globalThis as { __page?: Page }).__page ?? (await owner.newPage())

  await step(p2, "N11-word-versions-share-desktop", async () => {
    const buttons = await p2.locator("main button").evaluateAll((bs) => bs.map((b) => (b.getAttribute("aria-label") || b.textContent || "").replace(/\s+/g, " ").trim()).filter(Boolean))
    const tabs = await p2.getByRole("tab").allInnerTexts()
    await shot(p2, "N11-word-detail-header-desktop")
    let versions: unknown = null
    const vbtn = p2.getByRole("button", { name: /Versions|Historie|History|Verze/ }).or(p2.getByRole("tab", { name: /Versions|History|Verze|Historie/ })).first()
    if (await vbtn.isVisible().catch(() => false)) {
      await vbtn.click()
      await pause(2000)
      await audit(p2, "N11-word-versions-desktop", "desktop")
      versions = { opened: true, text: (await p2.locator("main").innerText()).slice(0, 600) }
    }
    const publish = p2.getByRole("button", { name: "Publikovat náhled" })
    let published: unknown = null
    if (await publish.isVisible().catch(() => false)) {
      await publish.click()
      await pause(4000)
      published = { text: await p2.getByText(/Sdílený náhled · verze/).innerText().catch(() => ""), alerts: await p2.getByRole("alert").allInnerTexts() }
      await shot(p2, "N11-word-published-preview-desktop")
    }
    const share = p2.getByRole("button", { name: /^Share/ }).first()
    let shareDialog: unknown = null
    if (await share.isVisible().catch(() => false)) {
      await share.click()
      const d = p2.getByRole("dialog")
      await d.waitFor({ timeout: 10_000 })
      const a = await audit(p2, "N11-word-share-dialog-desktop", "desktop")
      shareDialog = { text: (await d.innerText()).slice(0, 800), unnamed: a.unnamed.map((c) => c.html), mentionsVersion: /verz|version/i.test(await d.innerText()) }
      await p2.keyboard.press("Escape")
      shareDialog = { ...(shareDialog as object), focusAfterEscape: await focused(p2) }
    }
    return { buttons, tabs, versions, published, shareDialog }
  })
  await owner.close()

  // Phone: the same Word, then a Sheet.
  const phone = await browserContext(browser, "mobile", await ownerStorage())
  const mp = await phone.newPage()
  await step(mp, "N12-word-mobile", async () => {
    await mp.goto(`/artifacts/${wordId}?workspace_id=${ws.id}`)
    const ms = await waitEditor(mp)
    await pause(2500)
    const a = await audit(mp, "N12-word-mobile", "mobile")
    const take = mp.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    let typed: unknown = null
    if (await take.isVisible().catch(() => false)) {
      const box = await take.boundingBox()
      await take.tap()
      await pause(2000)
      await clickCanvas(mp, "wordcraft").catch(() => {})
      await mp.keyboard.type(" z telefonu", { delay: 40 })
      const tl = await statusTimeline(mp, 12_000, saveStatus)
      await shot(mp, "N12-word-mobile-typed")
      typed = { takeBox: box, timeline: tl, landed: await headIncludes(api, ws.id, wordId, "z telefonu") }
      await mp.getByRole("button", { name: "Uvolnit úpravy" }).tap().catch(() => {})
    }
    return { editorReadyMs: ms, small: a.small.map((c) => `${c.name || c.html.slice(0, 60)} ${c.w}x${c.h}`), overflow: a.horizontalOverflow, typed }
  })

  let sheetId = ""
  const dp = await (await browserContext(browser, "desktop", await ownerStorage())).newPage()
  await step(dp, "N13-sheet-desktop", async () => {
    sheetId = await createFromLibrary(dp, ws.id, "Sheet", `PRD UX walkthrough Sheet ${stamp}`, "desktop", "N13-sheet")
    const ms = await waitEditor(dp)
    const tl0 = await statusTimeline(dp, 5000, saveStatus)
    await dp.keyboard.type("42", { delay: 40 })
    await dp.keyboard.press("Enter")
    const tlNoClick = await statusTimeline(dp, 8000, saveStatus)
    const landedNoClick = await headIncludes(api, ws.id, sheetId, "42")
    const take = dp.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    if (await take.isVisible().catch(() => false)) await take.click()
    await pause(1200)
    await clickCanvas(dp, "gridcraft", "cell")
    await dp.keyboard.type("4242", { delay: 40 })
    await dp.keyboard.press("Enter")
    const tl = await statusTimeline(dp, 15_000, saveStatus)
    const a = await audit(dp, "N13-sheet-desktop", "desktop")
    return { editorReadyMs: ms, initial: tl0, typedWithoutClick: { timeline: tlNoClick, landed: landedNoClick }, afterClick: { timeline: tl, landed: await headIncludes(api, ws.id, sheetId, "4242") }, unnamed: a.unnamed.map((c) => c.html) }
  })
  await step(mp, "N14-sheet-mobile", async () => {
    await mp.goto(`/artifacts/${sheetId}?workspace_id=${ws.id}`)
    const ms = await waitEditor(mp)
    await pause(2500)
    const a = await audit(mp, "N14-sheet-mobile", "mobile")
    return { editorReadyMs: ms, small: a.small.map((c) => `${c.name || c.html.slice(0, 60)} ${c.w}x${c.h}`), overflow: a.horizontalOverflow }
  })
  await step(mp, "N15-library-mobile", async () => {
    await mp.goto(`/artifacts?workspace_id=${ws.id}`)
    await pause(3000)
    const a = await audit(mp, "N15-library-with-word-sheet-mobile", "mobile")
    return { small: a.small.map((c) => `${c.name || c.html.slice(0, 60)} ${c.w}x${c.h}`) }
  })
  note("N99-ids", { wordId, sheetId })
  await phone.close()
  await peerApi.dispose()
  await api.dispose()
})
