// PRD A5: a person who has the native Word/Sheet editor open and holds the
// editor loses the write right. The editor must turn read-only at once, the
// server must refuse any further write from that person, and the unsaved
// work must not be lost: it stays in the person's browser and is offered back
// (compare / copy), never saved automatically and never deleted. Losing only
// the write right is not losing access: the person still reads the document.
//
// Full mode only: it drives the real editor of a deployed origin.
import { captureAdmission, peerApi, requireNative, traceCraft, type CraftCall } from "./craft-lease/helpers"
import { APPLICATIONS, NativePage } from "./craft-lease/native"
import { headText, inspectDrafts, placeCaret, typedMarkers, typeLines, waitForDraftOffer, type Typing } from "./craft-session/draft"
import * as editor from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = {
  wordcraft: prdRequirement("A.craft.write-revoked-word", "Word: when the write right is withdrawn the open editor turns read-only and the unsaved draft is kept and offered"),
  gridcraft: prdRequirement("A.craft.write-revoked-sheet", "Sheet: when the write right is withdrawn the open editor turns read-only and the unsaved draft is kept and offered"),
} as const

// The server rechecks authority on a 5 s tick; leave generous slack.
const READ_ONLY_WITHIN_MS = 45_000

for (const { application, label } of APPLICATIONS) {
  test.describe(`${label} write right lost`, () => {
    test(
      `${label}: withdrawing write turns the open editor read-only and keeps the draft`,
      { annotation: REQ[application] },
      async ({ prd, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        test.setTimeout(10 * 60_000)
        const cleanup: Array<() => Promise<void>> = []
        const trail: CraftCall[] = []
        let passed = false
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD write revoked ${label}`)
          await requireNative(prd, request, doc)
          const { peer, state } = await peerApi(prd, cleanup)
          // Write plus read: withdrawing write must leave the person a reader.
          await doc.grant(request, peer.userId, "read")
          await doc.grant(request, peer.userId, "write")
          const log: Record<string, unknown> = { application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId }

          const context = await prd.browserContext(state)
          const page = await context.newPage()
          page.setDefaultTimeout(60_000)
          traceCraft(page, "peer", trail)
          const admission = captureAdmission(page)
          await page.goto(doc.editorPath)
          await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })

          // The person takes the editor and types; nothing is saved.
          const tag = Date.now().toString(36)
          const WORK = `PRDWRITE${tag}`
          await editor.acquire(page, application)
          // The editor's own admission: the lease belongs to that session.
          const own = admission.current()
          expect(own, "the editor was admitted").toBeTruthy()
          const mine = await doc.lease(context.request, own!)
          expect(mine.mine && mine.can_write, "the person holds the editor").toBe(true)
          const generation = mine.generation!
          const before = await headText(request, doc)
          const saves = trail.filter((c) => c.path === "craft/document/save" && c.status === 200).length

          // The person keeps typing and, in the middle of it, the owner
          // withdraws the write right. Nothing is saved before the right goes.
          await placeCaret(page, application, 6)
          const progress: Typing = { typed: 0 }
          const typing = typeLines(page, WORK, 8, 40, 120, progress)
          await page.waitForTimeout(1_600)
          // Every line typed while the right was still there must be in the draft.
          const typedBefore = typedMarkers(WORK, progress.typed)
          expect(typedBefore.length, "lines were typed before the right was withdrawn").toBeGreaterThan(0)
          const revokedAt = Date.now()
          await doc.revoke(request, peer.userId, "write")
          await typing
          await expect(page.getByRole("button", { name: "Uvolnit úpravy", exact: true }), "the editor is no longer the person's to edit").toHaveCount(0, { timeout: READ_ONLY_WITHIN_MS })
          // The editor ends the write session: "Pouze čtení", or "Editor odpojen · koncept zachován"
          // when the server closes the editor's event stream because the authority changed.
          await expect(page.getByRole("status").filter({ hasText: /Pouze čtení|Editor odpojen/ }).first()).toBeVisible({ timeout: READ_ONLY_WITHIN_MS })
          const readOnlyAfterMs = Date.now() - revokedAt
          const offeredRows = await waitForDraftOffer(page, READ_ONLY_WITHIN_MS)
          const drafts = await inspectDrafts(page, typedBefore)
          expect(drafts.hasMarker.some(Boolean), `the offered draft holds all ${typedBefore.length} lines typed before the right was withdrawn`).toBe(true)
          const shots: Record<string, Buffer> = { "peer-read-only.png": await page.screenshot({ fullPage: true }) }

          // The server takes no further write from the person and nothing was saved.
          const view = await doc.lease(context.request, admission.current()!).catch(() => null)
          const headNow = await headText(request, doc)
          expect(headNow.version, "no version was created").toBe(before.version)
          expect(headNow.text).not.toContain(`${WORK}-0`)
          expect(trail.filter((c) => c.path === "craft/document/save" && c.status === 200).length, "the editor saved nothing after the right was withdrawn").toBe(saves)
          const head = await doc.document(request, await doc.session(request))
          const late = await doc.save(context.request, own!, {
            generation,
            baseVersion: head.version,
            enginePin: head.enginePin,
            bytes: Buffer.from(head.bytes, "base64"),
            editorRevision: `prd-write-revoked-${Date.now()}`,
          })
          expect(late.status(), "a write after the right was withdrawn").toBeGreaterThanOrEqual(400)
          const reacquire = await doc.leaseOp(context.request, own!, "acquire")
          expect(reacquire.status(), "the person cannot take the editor again").toBeGreaterThanOrEqual(400)
          expect((await headText(request, doc)).version, "the refused write created no version").toBe(before.version)

          // The draft survives a reload; it is offered again once the person may edit.
          await page.reload()
          await page.getByRole("status").first().waitFor({ timeout: 90_000 })
          await doc.grant(request, peer.userId, "write")
          await page.reload()
          await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })
          await editor.frameOf(page, application)
          // A draft is never imported into a read-only view: it is offered once
          // the person takes the editor back (the old lease runs out first).
          await editor.takeButton(page).click({ timeout: 90_000 })
          await page.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor({ timeout: 60_000 })
          await waitForDraftOffer(page, 60_000)
          const regained = await inspectDrafts(page, typedBefore)
          expect(regained.hasMarker.some(Boolean), "after the right returns the draft with every line is offered again").toBe(true)
          expect((await headText(request, doc)).text, "nothing was saved automatically").not.toContain(`${WORK}-0`)
          shots["peer-draft-after-regrant.png"] = await page.screenshot({ fullPage: true })

          await prd.evidence("result.json", {
            ...log,
            readOnlyAfterMs,
            leaseAfterRevocation: view && { mine: view.mine, can_write: view.can_write, can_edit: view.can_edit },
            offered: offeredRows,
            draftHasWork: drafts.hasMarker,
            typedBeforeRevocation: typedBefore.length,
            lateWrite: late.status(),
            reacquire: reacquire.status(),
            versionUnchanged: before.version,
            afterRegrant: regained.rows,
          })
          for (const [name, png] of Object.entries(shots)) await prd.evidence(name, png)
          passed = true
        } finally {
          if (trail.length) await prd.evidence(passed ? "craft-calls.json" : "failure-craft-calls.json", trail).catch(() => {})
          for (const c of cleanup.reverse()) await c().catch(() => {})
        }
      },
    )
  })
}
