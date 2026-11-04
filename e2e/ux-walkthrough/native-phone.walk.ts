// Third native pass: Sheet from the library (desktop), then Word and Sheet on a
// phone. Every browser context has its own login.
import { test } from "@playwright/test"
import { audit, note, pause, shot } from "./audit"
import { clickCanvas, createFromLibrary, headIncludes, saveStatus, toolbar, waitEditor } from "./native-helpers"
import { browserContext, freshOwnerStorage, ownerApi, readSaved, walkWorkspace } from "./session"
import { statusTimeline, step } from "./steps"

test("native Sheet and phone", async ({ browser }) => {
  test.skip(process.env.UX_TARGET !== "native3")
  const api = await ownerApi()
  const ws = await walkWorkspace(api)
  const stamp = Date.now().toString(36)
  const desk = await browserContext(browser, "desktop", await freshOwnerStorage())
  const dp = await desk.newPage()
  const offlineDoc = process.env.UX_OFFLINE_DOC ?? (readSaved() as { offlineDoc?: string }).offlineDoc
  if (offlineDoc) await step(dp, "S00-offline-doc-later-desktop", async () => {
    // Minutes after the outage and reload: is the person still locked out by their own old tab, and is the draft offered?
    await dp.goto(`/artifacts/${offlineDoc}?workspace_id=${ws.id}`)
    await waitEditor(dp)
    await pause(5000)
    const rows = await dp.getByText(/^Místní koncept · verze/).count()
    const bar = await toolbar(dp).innerText()
    await audit(dp, "S00-offline-doc-later-desktop", "desktop")
    const take = dp.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    let after: unknown = null
    if (await take.isVisible().catch(() => false)) {
      await take.click()
      await pause(4000)
      after = { rows: await dp.getByText(/^Místní koncept · verze/).count(), status: await dp.locator(saveStatus).first().innerText(), bar: await toolbar(dp).innerText() }
      await shot(dp, "S00-offline-doc-after-take-desktop")
      await dp.getByRole("button", { name: "Uvolnit úpravy" }).click().catch(() => {})
    }
    return { rows, bar, status: await dp.locator(saveStatus).first().innerText(), after, server: await headIncludes(api, ws.id, offlineDoc, "behem vypadku") }
  })
  let sheetId = ""
  await step(dp, "S01-sheet-create-desktop", async () => {
    sheetId = await createFromLibrary(dp, ws.id, "Sheet", `PRD UX walkthrough Sheet ${stamp}`, "desktop", "S01-sheet")
    const ms = await waitEditor(dp)
    const initial = await statusTimeline(dp, 4000, saveStatus)
    const bar = await toolbar(dp).innerText()
    await audit(dp, "S01-sheet-opened-desktop", "desktop")
    // Straight after "Create and edit": type without clicking anything.
    await dp.keyboard.type("42", { delay: 40 })
    await dp.keyboard.press("Enter")
    const noClick = await statusTimeline(dp, 6000, saveStatus)
    return { editorReadyMs: ms, initial, bar, typedWithoutClick: { timeline: noClick, landed: await headIncludes(api, ws.id, sheetId, "42") } }
  })
  await step(dp, "S02-sheet-type-desktop", async () => {
    const take = dp.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
    const needed = await take.isVisible().catch(() => false)
    if (needed) await take.click()
    await pause(1500)
    await clickCanvas(dp, "gridcraft", "cell")
    await dp.keyboard.type("4242", { delay: 40 })
    await dp.keyboard.press("Enter")
    const tl = await statusTimeline(dp, 12_000, saveStatus)
    await shot(dp, "S02-sheet-typed-desktop")
    return { neededTake: needed, timeline: tl, landed: await headIncludes(api, ws.id, sheetId, "4242") }
  })
  await step(dp, "S03-sheet-release-desktop", async () => {
    await dp.getByRole("button", { name: "Uvolnit úpravy" }).click()
    const tl = await statusTimeline(dp, 5000, saveStatus)
    await shot(dp, "S03-sheet-released-desktop")
    return { timeline: tl, bar: await toolbar(dp).innerText() }
  })
  await desk.close()

  const phone = await browserContext(browser, "mobile", await freshOwnerStorage())
  const mp = await phone.newPage()
  const wordId = (readSaved() as { wordId?: string }).wordId ?? process.env.UX_WORD_ID ?? ""
  for (const [tag, id, app] of [["S04-word", wordId, "wordcraft"], ["S05-sheet", sheetId, "gridcraft"]] as const) {
    if (!id) { note(`${tag}-mobile-skipped`, "no id"); continue }
    await step(mp, `${tag}-mobile`, async () => {
      await mp.goto(`/artifacts/${id}?workspace_id=${ws.id}`)
      const ms = await waitEditor(mp)
      await pause(2500)
      const a = await audit(mp, `${tag}-mobile`, "mobile")
      const take = mp.getByRole("button", { name: "Převzít úpravy", exact: true }).first()
      let typed: unknown = null
      if (await take.isVisible().catch(() => false)) {
        const box = await take.boundingBox()
        await take.tap()
        await pause(2000)
        await mp.locator("iframe").first().scrollIntoViewIfNeeded().catch(() => {})
        await clickCanvas(mp, app, app === "gridcraft" ? "cell" : "center").catch(() => {})
        await mp.keyboard.type(app === "gridcraft" ? "7" : " z telefonu", { delay: 40 })
        if (app === "gridcraft") await mp.keyboard.press("Enter")
        const tl = await statusTimeline(mp, 10_000, saveStatus)
        await shot(mp, `${tag}-mobile-typed`)
        const fullpage = `${tag}-mobile-fullpage.png`
        await mp.screenshot({ path: `${process.env.UX_OUT}/${fullpage}`, fullPage: true }).catch(() => {})
        typed = { takeBox: box, timeline: tl, landed: await headIncludes(api, ws.id, id, app === "gridcraft" ? "7" : "z telefonu") }
        await mp.getByRole("button", { name: "Uvolnit úpravy" }).tap().catch(() => {})
        await pause(1500)
      }
      const iframeBox = await mp.locator("iframe").first().boundingBox()
      return { editorReadyMs: ms, small: a.small.map((c) => `${c.name || c.html.slice(0, 60)} ${c.w}x${c.h}`), overflow: a.horizontalOverflow, iframeBox, typed }
    })
  }
  await phone.close()
  await api.dispose()
})
