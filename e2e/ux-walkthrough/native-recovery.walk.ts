// Second native pass on fresh Word documents: a clean handoff, and what a person
// can do after a save whose result is unconfirmed (offline), including reload.
import { request as pwRequest, test } from "@playwright/test"
import { loginStorageState } from "../acceptance/prd/auth"
import { NativePage } from "../acceptance/prd/craft-lease/native"
import { audit, focused, note, pause, shot } from "./audit"
import { clickCanvas, createNativeViaApi, headIncludes, saveStatus, toolbar, waitEditor } from "./native-helpers"
import { browserContext, ORIGIN, ownerApi, ownerStorage, walkPeer, walkWorkspace } from "./session"
import { statusTimeline, step } from "./steps"

test("native recovery and clean handoff", async ({ browser }) => {
  test.skip(process.env.UX_TARGET !== "native2")
  const api = await ownerApi()
  const ws = await walkWorkspace(api)
  const peer = await walkPeer(api, ws)
  const peerState = await loginStorageState(ORIGIN, { email: peer.email, password: peer.password })
  const stamp = Date.now().toString(36)
  const owner = await browserContext(browser, "desktop", await ownerStorage())
  const page = await owner.newPage()

  // R1: clean handoff on a fresh document.
  const h = await createNativeViaApi(api, ws.id, "wordcraft", `PRD UX walkthrough Word předání ${stamp}`)
  await new NativePage(ws.id, h, "wordcraft").grant(api, peer.userId, "write")
  await step(page, "R01-handoff-owner-takes-desktop", async () => {
    await page.goto(`/artifacts/${h}?workspace_id=${ws.id}`)
    await waitEditor(page)
    await pause(1500)
    await page.getByRole("button", { name: "Převzít úpravy", exact: true }).first().click()
    const tl = await statusTimeline(page, 5000, saveStatus)
    await clickCanvas(page, "wordcraft")
    await page.keyboard.type("Vlastnik pise", { delay: 30 })
    const tl2 = await statusTimeline(page, 8000, saveStatus)
    return { afterTake: tl, afterTyping: tl2, landed: await headIncludes(api, ws.id, h, "Vlastnik pise") }
  })
  const peerCtx = await browserContext(browser, "desktop", peerState)
  const pp = await peerCtx.newPage()
  await step(pp, "R02-handoff-peer-requests-desktop", async () => {
    await pp.goto(`/artifacts/${h}?workspace_id=${ws.id}`)
    await waitEditor(pp)
    await pause(2000)
    const before = await toolbar(pp).innerText()
    const ask = pp.getByRole("button", { name: "Požádat o předání" })
    await ask.focus()
    await pp.keyboard.press("Enter")
    await pause(2500)
    await audit(pp, "R02-handoff-peer-after-request-desktop", "desktop")
    return { before, after: await toolbar(pp).innerText(), askStillEnabled: await ask.isEnabled().catch(() => null), focus: await focused(pp), anyConfirmation: await pp.getByText(/Žádost|požádal|odeslán|čeká/i).allInnerTexts() }
  })
  await step(page, "R03-handoff-owner-gives-desktop", async () => {
    const give = page.getByRole("button", { name: /^Předat uživateli/ })
    await give.waitFor({ timeout: 30_000 })
    await audit(page, "R03-handoff-owner-sees-request-desktop", "desktop")
    await give.click()
    const tl = await statusTimeline(page, 8000, saveStatus)
    await shot(page, "R03-handoff-owner-after-give-desktop")
    return { ownerStatus: tl, ownerBar: await toolbar(page).innerText() }
  })
  await step(pp, "R04-handoff-peer-edits-desktop", async () => {
    const tl = await statusTimeline(pp, 8000, saveStatus)
    const bar = await toolbar(pp).innerText()
    const take = pp.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    const extraTake = await take.isVisible().catch(() => false)
    await shot(pp, "R04-handoff-peer-received-desktop")
    if (extraTake) await take.click()
    await pause(1500)
    await clickCanvas(pp, "wordcraft")
    await pp.keyboard.press("Control+End")
    await pp.keyboard.type(" kolegyne pise", { delay: 30 })
    const tl2 = await statusTimeline(pp, 10_000, saveStatus)
    await shot(pp, "R04-handoff-peer-typed-desktop")
    return { status: tl, bar, extraTakeClickNeeded: extraTake, afterTyping: tl2, landed: await headIncludes(api, ws.id, h, "kolegyne pise") }
  })
  await peerCtx.close()

  // R2: unconfirmed save after a network drop, then "Ověřit výsledek", then reload.
  const u = await createNativeViaApi(api, ws.id, "wordcraft", `PRD UX walkthrough Word offline ${stamp}`)
  await step(page, "R05-offline-verify-desktop", async () => {
    await page.goto(`/artifacts/${u}?workspace_id=${ws.id}`)
    await waitEditor(page)
    await pause(1500)
    await page.getByRole("button", { name: "Převzít úpravy", exact: true }).first().click()
    await pause(2000)
    await clickCanvas(page, "wordcraft")
    await page.keyboard.type("Pred vypadkem", { delay: 30 })
    const saved = await statusTimeline(page, 6000, saveStatus)
    await owner.setOffline(true)
    await page.keyboard.type(" behem vypadku", { delay: 30 })
    const off = await statusTimeline(page, 12_000, saveStatus)
    await shot(page, "R05-offline-unconfirmed-desktop")
    await owner.setOffline(false)
    const backOnline = await statusTimeline(page, 15_000, saveStatus)
    const verify = page.getByRole("button", { name: "Ověřit výsledek" })
    const hasVerify = await verify.isVisible().catch(() => false)
    let afterVerify: unknown = null
    if (hasVerify) {
      await verify.click()
      afterVerify = { timeline: await statusTimeline(page, 15_000, saveStatus), alerts: (await page.getByRole("alert").allInnerTexts()).filter(Boolean) }
      await shot(page, "R05-after-verify-desktop")
    }
    // A person keeps typing after the network returns.
    await clickCanvas(page, "wordcraft").catch(() => {})
    await page.keyboard.press("Control+End")
    await page.keyboard.type(" po navratu", { delay: 30 })
    const afterMore = await statusTimeline(page, 12_000, saveStatus)
    await shot(page, "R05-typing-after-return-desktop")
    return { saved, off, backOnline, hasVerify, afterVerify, afterMore,
      server: { before: await headIncludes(api, ws.id, u, "Pred vypadkem"), during: await headIncludes(api, ws.id, u, "behem vypadku"), after: await headIncludes(api, ws.id, u, "po navratu") } }
  })
  await step(page, "R06-reload-recovery-desktop", async () => {
    await page.reload()
    await waitEditor(page)
    await pause(4000)
    const rows = page.getByText(/^Místní koncept · verze/)
    const before = { rows: await rows.count(), status: await page.locator(saveStatus).first().innerText(), bar: await toolbar(page).innerText() }
    await audit(page, "R06-reload-before-take-desktop", "desktop")
    const take = page.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    if (await take.isVisible().catch(() => false)) await take.click()
    const tl = await statusTimeline(page, 12_000, saveStatus)
    const after = { rows: await rows.count(), alerts: (await page.getByRole("alert").allInnerTexts()).filter(Boolean) }
    await audit(page, "R06-reload-after-take-desktop", "desktop")
    let compare: unknown = null
    if (after.rows > 0) {
      const btn = rows.first().locator("..").getByRole("button", { name: "Porovnat", exact: true })
      await btn.focus()
      await page.keyboard.press("Enter")
      await pause(2500)
      await audit(page, "R06-draft-compare-desktop", "desktop")
      const caption = await page.getByText(/^Serverová verze \d+ a /).innerText().catch(() => "")
      const actions = await page.locator("main button").evaluateAll((bs) => bs.map((b) => (b.textContent ?? "").trim()).filter((t) => /koncept|Obnov|Použ|Porovn|Kopie|Zavřít/.test(t)))
      await page.keyboard.press("Escape")
      const escCloses = !(await page.getByRole("button", { name: "Zavřít porovnání" }).isVisible().catch(() => false))
      compare = { caption, actions, escCloses }
    }
    return { before, afterTake: tl, after, compare }
  })
  await step(page, "R07-versions-share-desktop", async () => {
    await page.goto(`/artifacts/${h}?workspace_id=${ws.id}`)
    await waitEditor(page)
    await pause(2500)
    const buttons = await page.locator("main button").evaluateAll((bs) => bs.map((b) => (b.getAttribute("aria-label") || b.textContent || "").replace(/\s+/g, " ").trim()).filter(Boolean).slice(0, 40))
    const access = page.getByRole("button", { name: "Access" }).or(page.getByRole("tab", { name: "Access" })).first()
    let accessText = ""
    if (await access.isVisible().catch(() => false)) {
      await access.click()
      await pause(2000)
      await audit(page, "R07-word-access-tab-desktop", "desktop")
      accessText = (await page.locator("main").innerText()).slice(0, 900)
      await page.getByRole("button", { name: "Content" }).or(page.getByRole("tab", { name: "Content" })).first().click().catch(() => {})
      await pause(1500)
    }
    const share = page.getByRole("button", { name: /^Share$/ }).first()
    await share.focus()
    await page.keyboard.press("Enter")
    const d = page.getByRole("dialog")
    await d.waitFor({ timeout: 10_000 })
    const a = await audit(page, "R07-word-share-dialog-desktop", "desktop")
    const dialogText = (await d.innerText()).slice(0, 900)
    await page.keyboard.press("Escape")
    await pause(500)
    const focusAfter = await focused(page)
    // "Publikovat náhled" is the only per-version share the native editor offers.
    const take = page.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    if (await take.isVisible().catch(() => false)) await take.click()
    await pause(2000)
    const publish = page.getByRole("button", { name: "Publikovat náhled" })
    let published: unknown = null
    if (await publish.isVisible().catch(() => false)) {
      await publish.click()
      await pause(4000)
      published = { text: await page.getByText(/Sdílený náhled · verze/).innerText().catch(() => ""), alerts: (await page.getByRole("alert").allInnerTexts()).filter(Boolean) }
      await shot(page, "R07-word-published-preview-desktop")
    }
    return { buttons, accessText, dialogText, mentionsVersion: /verz|version/i.test(dialogText), unnamed: a.unnamed.map((c) => c.html), focusAfterEscape: focusAfter, published }
  })
  note("R99-ids", { handoff: h, offline: u })
  await owner.close()
  await api.dispose()
})
