// PRD A: a Sheet keeps its data classes. Formulas, number formats and more
// than one sheet survive the save, a reload of the same editor session and a
// reopen by a fresh login with a fresh admission.
//
// Quick mode drives the editor's HTTP API when the server runs with a native
// editor profile (skipped otherwise); full mode also opens the saved workbook
// in the real GridCraft editor of the deployed origin.
import { apiAs, loginStorageState } from "./auth"
import { requireNative } from "./craft-lease/helpers"
import { NativePage, sessionUserId, type LeaseView } from "./craft-lease/native"
import { EXPECTED_CELLS, EXPECTED_SHEETS, EXPECTED_DATE_SYSTEM, readFacts, richWorkbook } from "./craft-lease/sheet-workbook"
import * as native from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = prdRequirement("A.craft.sheet.data-roundtrip", "Sheet: date, Bool, Text, numbers, formulas, formats and multiple sheets survive save, reload and reopen")

test.describe("Sheet data classes", () => {
  test("typed values, dates, formulas, formats and sheets survive save, reload and reopen", { annotation: REQ }, async ({ prd, request }) => {
    const cleanup: Array<() => Promise<void>> = []
    try {
      const ws = await prd.qaWorkspace()
      const doc = await NativePage.create(request, ws.id, "gridcraft", "PRD sheet data")
      await requireNative(prd, request, doc)
      const log: Record<string, unknown> = { mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId }

      // Save the workbook as the holder.
      const writer = await doc.session(request)
      const acquired = (await (await doc.leaseOp(request, writer, "acquire")).json()) as LeaseView
      expect(acquired.mine).toBe(true)
      const blank = await doc.document(request, writer)
      const bytes = richWorkbook()
      const saved = await doc.save(request, writer, { generation: acquired.generation!, baseVersion: blank.version, enginePin: blank.enginePin, bytes, editorRevision: `prd-sheet-data-${Date.now()}` })
      expect(saved.status(), `save the workbook: ${saved.status() === 200 ? "" : (await saved.text()).slice(0, 300)}`).toBe(200)
      const receipt = (await saved.json()) as { version: string; phase: string }
      expect(receipt.phase).toBe("committed")
      expect(BigInt(receipt.version)).toBeGreaterThan(BigInt(blank.version))

      // Reload: a fresh admission of the same login reads what was saved.
      const reloadSession = await doc.session(request)
      const reloaded = await doc.document(request, reloadSession)
      expect(reloaded.version).toBe(receipt.version)
      const afterReload = readFacts(Buffer.from(reloaded.bytes, "base64"))
      expect(afterReload.sheets, "sheets after reload").toEqual(EXPECTED_SHEETS)
      expect(afterReload.dateSystem, "date system after reload").toBe(EXPECTED_DATE_SYSTEM)
      expect(afterReload.cells, "cells after reload").toEqual(EXPECTED_CELLS)

      // Reopen: the custody is released and a new login opens the workbook.
      const released = await doc.leaseOp(request, writer, "release", { generation: acquired.generation! })
      expect(released.status()).toBe(200)
      const ownerAccount = prd.env.accounts.owner
      expect(ownerAccount, "owner account").toBeTruthy()
      const reopenApi = await apiAs(prd.origin, await loginStorageState(prd.origin, ownerAccount!))
      cleanup.push(() => reopenApi.dispose())
      expect(await sessionUserId(reopenApi)).toBe(await sessionUserId(request))
      const reopenSession = await doc.session(reopenApi)
      const reopened = await doc.document(reopenApi, reopenSession)
      expect(reopened.version).toBe(receipt.version)
      const afterReopen = readFacts(Buffer.from(reopened.bytes, "base64"))
      expect(afterReopen.sheets, "sheets after reopen").toEqual(EXPECTED_SHEETS)
      expect(afterReopen.dateSystem, "date system after reopen").toBe(EXPECTED_DATE_SYSTEM)
      expect(afterReopen.cells, "cells after reopen").toEqual(EXPECTED_CELLS)
      log.api = { savedVersion: receipt.version, sheets: afterReopen.sheets, dateSystem: afterReopen.dateSystem, cells: afterReopen.cells }

      // The real editor opens the saved workbook (full mode: needs the bundle).
      if (prd.mode === "full") {
        const context = await prd.browserContext(await loginStorageState(prd.origin, ownerAccount!))
        const page = await context.newPage()
        page.setDefaultTimeout(90_000)
        await native.openEditor(page, doc.pageId, ws.id)
        await native.frameOf(page, "gridcraft")
        const alerts = await page.getByRole("alert").allInnerTexts()
        expect(alerts.filter(Boolean), "the editor opens the workbook without an error").toEqual([])
        await prd.evidence("editor-reopened.png", await page.screenshot({ fullPage: true }))

        // Edit in the real editor and save through its engine: the whole workbook
        // goes through the engine's own serializer, so every class must survive it.
        const frame = await native.acquire(page, "gridcraft")
        const marker = `PRDGRID${Date.now().toString(36)}`
        await native.typeMarker(page, frame, "gridcraft", marker, 3)
        const editorVersion = await native.waitSaved(page, 60_000)
        await native.release(page)
        const edited = await native.readNative(request, ws.id, doc.pageId)
        expect(BigInt(edited.version), "the editor saved a new version").toBeGreaterThan(BigInt(receipt.version))
        const afterEditor = readFacts(Buffer.from(edited.raw, "utf8"))
        expect(afterEditor.sheets, "sheets after the editor saved").toEqual(EXPECTED_SHEETS)
        expect(afterEditor.dateSystem, "date system after the editor saved").toBe(EXPECTED_DATE_SYSTEM)
        expect(afterEditor.cells, "every data class after the editor saved").toMatchObject(EXPECTED_CELLS)
        expect(afterEditor.cells["Sheet1!A3"], "the typed cell").toMatchObject({ valueType: "Text", value: marker })

        // And it opens again from that engine-written version.
        await page.reload()
        await native.frameOf(page, "gridcraft")
        expect((await page.getByRole("alert").allInnerTexts()).filter(Boolean), "the edited workbook reopens without an error").toEqual([])
        await prd.evidence("editor-after-edit.png", await page.screenshot({ fullPage: true }))
        log.editor = { opened: true, editedVersion: edited.version, editorVersion, marker, cells: afterEditor.cells }
      }
      await prd.evidence("result.json", log)
    } finally {
      for (const c of cleanup.reverse()) await c().catch(() => {})
    }
  })
})
