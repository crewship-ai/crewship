// A ~5 MB synthetic workbook (sheets, SUMIFS, a PivotTable,
// conditional formats, validations, merged cells) added in a NEW chat opens by
// itself as a Sheet beside the chat. The time to its values and to editing is
// measured. A change by hand is saved as a new version; Download .xlsx holds
// the change, Download original is version 1, byte for byte.
import { expect, test } from "@playwright/test"
import { cell, values, workbookOf } from "../craft-packages/workbook-file"
import * as H from "./helpers"
import { clientWorkbook } from "./workbooks"

test.describe.configure({ mode: "serial" })

test("a 5 MB workbook in a new chat opens by itself beside the chat, is edited, saved and downloaded", async ({ browser }) => {
  H.requireAgent()
  test.setTimeout(20 * 60_000)
  const book = clientWorkbook()
  test.info().annotations.push({ type: "workbook", description: `${book.name}: ${(book.bytes.length / 1048576).toFixed(2)} MiB, ${book.rows} data rows, sha256 ${H.sha(book.bytes)}` })
  expect(book.bytes.length, "the workbook is about 5 MB").toBeGreaterThan(4.5 * 1048576)

  const ctx = await H.clientContext(browser)
  const page = await ctx.newPage()
  const api = await H.ownerApi()
  let pageId = ""
  try {
    await H.newChat(page)
    const attached = Date.now()
    await H.attach(page, book.name, book.bytes)

    // Auto-open: no button to press; the Sheet opens beside the chat on its own.
    const row = page.getByRole("group", { name: `${book.name} as a Sheet` })
    await expect(row.or(H.panelOf(page)).first()).toBeVisible({ timeout: 60_000 })
    await expect(page.getByRole("button", { name: `Open ${book.name} as a Sheet` }), "no Open as a Sheet button on a workbook server").toHaveCount(0)
    const panelAfter = await (async () => { await expect(H.panelOf(page)).toBeVisible({ timeout: H.LONG }); return Date.now() - attached })()
    const valuesAfter = await H.waitViewing(page, attached)
    await H.shot(page, "01-sheet-open")
    pageId = await H.pageIdOf(page)

    const v1 = await H.head(api, pageId, "1")
    expect(H.sha(v1.bytes), "version 1 is the uploaded file, byte for byte").toBe(H.sha(book.bytes))
    expect(values(v1, "Souhrn", ["B4", "B5", "B6", "B7"]), "SUMIFS by region as uploaded").toEqual(book.expected.byRegion)

    const editFrom = Date.now()
    const frame = await H.startEditing(page)
    const editAfter = Date.now() - attached
    const takeOver = Date.now() - editFrom
    await H.shot(page, "02-editing")

    // A change by hand (Vstupy B5, the factor, 2 → 3), saved as a new version.
    const before = /version (\d+)/.exec(await H.stateOf(page).innerText())?.[1] ?? "1"
    const typed = Date.now()
    await H.setCell(page, frame, "B5", "3")
    const saved = await H.waitSavedAfter(page, before)
    const saveAfter = Date.now() - typed
    await H.shot(page, "03-saved")
    await H.recordTimings({ bytes: book.bytes.length, rows: book.rows, panel_ms: panelAfter, values_ms: valuesAfter, editing_ms: editAfter, take_over_ms: takeOver, save_ms: saveAfter, saved_version: saved })

    const now = await H.head(api, pageId)
    expect(now.version, "the head is the version the bar names").toBe(saved)
    expect(cell(now, "Vstupy", "B5")).toBe(3)
    expect(values(now, "Souhrn", ["C4", "C5", "C6", "C7"]), "Souhrn follows the factor").toEqual(book.adjustedFor(3))
    expect(values(now, "Souhrn", ["B4", "B5", "B6", "B7"]), "SUMIFS unchanged").toEqual(book.expected.byRegion)
    expect(now.sheet("Souhrn"), "conditional formats kept").toMatch(/<(x14:)?cfRule[^>]*type="cellIs"/)
    expect(now.sheet("Vstupy"), "merged title kept").toMatch(/<mergeCell ref="A1:D1"\/>/)
    expect(now.sheet("Vstupy"), "validations kept").toMatch(/<dataValidation[^>]*type="list"/)

    // Download .xlsx: the change is in it, and it is the stored head itself.
    const latest = await H.downloadFromMenu(page, /^Download \.xlsx/)
    expect(latest.name).toMatch(/\.xlsx$/)
    const file = workbookOf("", latest.bytes)
    expect(cell(file, "Vstupy", "B5"), "the download holds the change").toBe(3)
    expect(values(file, "Souhrn", ["C4", "C5", "C6", "C7"])).toEqual(book.adjustedFor(3))
    expect.soft(H.sha(latest.bytes), "the download is the stored head").toBe(H.sha(now.bytes))

    // Download original (version 1): the uploaded file, unchanged.
    const original = await H.downloadFromMenu(page, "Download original (version 1)")
    expect(H.sha(original.bytes), "Download original is the uploaded file").toBe(H.sha(book.bytes))
    await H.shot(page, "04-downloaded")
  } finally {
    await H.deletePage(api, pageId)
    await ctx.close()
    await api.dispose()
  }
})
