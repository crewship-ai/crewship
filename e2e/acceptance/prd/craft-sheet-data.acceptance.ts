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
import { EXPECTED_CELLS, EXPECTED_SHEETS, readFacts, richWorkbook } from "./craft-lease/sheet-workbook"
import * as native from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = prdRequirement("A.craft.sheet.data-roundtrip", "Sheet: formulas, number formats and multiple sheets survive save, reload and reopen")

test.describe("Sheet data classes", () => {
  test("formulas, number formats and sheets survive save, reload and reopen", { annotation: REQ }, async ({ prd, request }) => {
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
      expect(afterReopen.cells, "cells after reopen").toEqual(EXPECTED_CELLS)
      log.api = { savedVersion: receipt.version, sheets: afterReopen.sheets, cells: afterReopen.cells }

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
        log.editor = { opened: true }
      }
      await prd.evidence("result.json", log)
    } finally {
      for (const c of cleanup.reverse()) await c().catch(() => {})
    }
  })
})
