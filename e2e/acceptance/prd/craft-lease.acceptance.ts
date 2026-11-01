// PRD A: native Word and Sheet editor lease. Exactly one person edits at a
// time; everyone sees who ("Upravuje X"); custody passes only by an explicit
// request and handoff; and a write under a retired generation is refused (409)
// without touching the document.
//
// Quick mode drives the lease protocol over the editor's own HTTP API when the
// server runs with a native editor profile, and otherwise proves that the
// endpoints fail closed. Full mode additionally drives two real browsers
// through the editor's lease bar on a deployed instance with native bundles.
import { loginStorageState } from "./auth"
import { captureAdmission, isCraft, nativeProfile, PEER_NAME, peerApi, requireNative, traceCraft, type CraftCall } from "./craft-lease/helpers"
import { APPLICATIONS, NativePage, sessionUserId, type Admission, type LeaseView } from "./craft-lease/native"
import { headText, inspectDrafts, placeCaret, newTyping, typedMarkers, typeLines, waitForDraftOffer } from "./craft-session/draft"
import * as editor from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"

/**
 * Requirements per application. Word keeps the original ids; Sheet has its own
 * "-sheet" rows so the table shows each editor separately.
 */
const REQ = {
  wordcraft: {
    holder: prdRequirement("A.craft.lease.holder", "Word: everyone sees who holds the editor (\"Upravuje X\"); a second person cannot take it"),
    handoff: prdRequirement("A.craft.lease.handoff", "Word: custody passes only through request and handoff; the new holder can save"),
    stale: prdRequirement("A.craft.lease.stale-generation", "Word: a write under a retired generation is refused (409) and the document stays unchanged"),
  },
  gridcraft: {
    holder: prdRequirement("A.craft.lease.holder-sheet", "Sheet: everyone sees who holds the editor (\"Upravuje X\"); a second person cannot take it"),
    handoff: prdRequirement("A.craft.lease.handoff-sheet", "Sheet: custody passes only through request and handoff; the new holder can save"),
    stale: prdRequirement("A.craft.lease.stale-generation-sheet", "Sheet: a write under a retired generation is refused (409) and the document stays unchanged"),
  },
} as const
const REQ_DISABLED = prdRequirement("A.craft.lease.fail-closed", "Without a native editor profile no lease or native document is ever granted")

for (const { application, label } of APPLICATIONS) {
  test.describe(`${label} lease`, () => {
    test(
      `${label}: holder, handoff and stale generation over the editor API`,
      { annotation: [REQ[application].holder, REQ[application].handoff, REQ[application].stale] },
      async ({ prd, request }) => {
        const cleanup: Array<() => Promise<void>> = []
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD lease ${label}`)
          await requireNative(prd, request, doc)
          // Membership and grants change the workspace epoch, which retires
          // native admissions; settle them before anyone is admitted.
          const { peer, api: peerCtx } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "write")
          const ownerId = await sessionUserId(request)
          const owner = await doc.session(request)
          const other = await doc.session(peerCtx)
          const log: Record<string, unknown> = { application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId }

          // Free document; both may edit.
          const free = await doc.lease(peerCtx, other)
          expect(free.holder).toBeNull()
          expect(free.can_edit).toBe(true)

          // Owner takes custody. The peer sees who edits and cannot take it.
          const acquired = (await (await doc.leaseOp(request, owner, "acquire")).json()) as LeaseView
          expect(acquired.mine).toBe(true)
          expect(acquired.can_write).toBe(true)
          expect(acquired.holder?.actor_id).toBe(`user:${ownerId}`)
          const gOwner = acquired.generation
          expect(gOwner).toMatch(/^[1-9][0-9]*$/)
          const seenByPeer = await doc.lease(peerCtx, other)
          expect(seenByPeer.holder?.actor_id).toBe(`user:${ownerId}`)
          expect(seenByPeer.holder?.kind).toBe("human")
          expect(seenByPeer.holder?.display_name.trim()).not.toBe("")
          expect(seenByPeer.mine).toBe(false)
          expect(seenByPeer.can_write).toBe(false)
          const peerAcquire = await doc.leaseOp(peerCtx, other, "acquire")
          expect(peerAcquire.status(), "a second person cannot take a held lease").toBe(409)
          log.holder = { shownToPeer: `Upravuje ${seenByPeer.holder?.display_name}`, peerAcquire: peerAcquire.status() }

          // The holder saves; a non-holder save under the holder's generation is refused.
          const v1 = await doc.document(request, owner)
          const bytes = Buffer.from(v1.bytes, "base64")
          const ownerSave = await doc.save(request, owner, { generation: gOwner!, baseVersion: v1.version, enginePin: v1.enginePin, bytes, editorRevision: `prd-owner-${Date.now()}` })
          expect(ownerSave.status(), "the holder saves").toBe(200)
          const v2 = (await ownerSave.json()) as { version: string; phase: string }
          expect(v2.phase).toBe("committed")
          expect(BigInt(v2.version)).toBeGreaterThan(BigInt(v1.version))
          const intruder = await doc.save(peerCtx, other, { generation: gOwner!, baseVersion: v2.version, enginePin: v1.enginePin, bytes, editorRevision: `prd-intruder-${Date.now()}` })
          expect(intruder.status(), "a non-holder cannot save under the holder's generation").toBe(409)
          expect((await doc.document(peerCtx, other)).version).toBe(v2.version)

          // Request and handoff.
          const requested = await doc.leaseOp(peerCtx, other, "request-handoff")
          expect(requested.status()).toBe(201)
          const { request_id: requestId } = (await requested.json()) as { request_id: string }
          const pending = await doc.lease(request, owner)
          const entry = pending.handoff_requested_by.find((r) => r.request_id === requestId)
          expect(entry, "the holder sees the handoff request").toBeTruthy()
          expect(entry?.actor_id).toBe(`user:${peer.userId}`)
          expect(entry?.display_name).toBe(PEER_NAME)
          const handed = await doc.leaseOp(request, owner, "handoff", { generation: gOwner!, request_id: requestId })
          expect(handed.status()).toBe(200)
          const afterHandoff = (await handed.json()) as LeaseView
          expect(afterHandoff.mine).toBe(false)
          expect(afterHandoff.holder?.actor_id).toBe(`user:${peer.userId}`)
          const peerView = await doc.lease(peerCtx, other)
          expect(peerView.mine).toBe(true)
          expect(peerView.can_write).toBe(true)
          const gPeer = peerView.generation
          expect(gPeer).toMatch(/^[1-9][0-9]*$/)
          expect(gPeer).not.toBe(gOwner)
          const ownerSees = await doc.lease(request, owner)
          expect(ownerSees.holder?.display_name).toBe(PEER_NAME)
          log.handoff = { requestSeenByHolder: true, from: gOwner, to: gPeer, shownToOwner: `Upravuje ${ownerSees.holder?.display_name}` }

          // The retired generation is fenced: save, renew and release all 409, nothing changes.
          const head = await doc.document(peerCtx, other)
          const stale = await doc.save(request, owner, { generation: gOwner!, baseVersion: head.version, enginePin: head.enginePin, bytes, editorRevision: `prd-stale-${Date.now()}` })
          expect(stale.status(), "stale-generation save").toBe(409)
          const staleRenew = await doc.leaseOp(request, owner, "renew", { generation: gOwner! })
          expect(staleRenew.status(), "stale-generation renew").toBe(409)
          const staleRelease = await doc.leaseOp(request, owner, "release", { generation: gOwner! })
          expect(staleRelease.status(), "stale-generation release").toBe(409)
          const ownerReacquire = await doc.leaseOp(request, owner, "acquire")
          expect(ownerReacquire.status(), "the former holder cannot take it back").toBe(409)
          const unchanged = await doc.document(peerCtx, other)
          expect(unchanged.version).toBe(head.version)
          expect(unchanged.bytes).toBe(head.bytes)
          log.stale = { save: stale.status(), renew: staleRenew.status(), release: staleRelease.status(), reacquire: ownerReacquire.status(), versionUnchanged: unchanged.version }

          // The new holder saves and releases; the document is free again.
          const peerSave = await doc.save(peerCtx, other, { generation: gPeer!, baseVersion: head.version, enginePin: head.enginePin, bytes, editorRevision: `prd-peer-${Date.now()}` })
          expect(peerSave.status(), "the new holder saves").toBe(200)
          const v3 = (await peerSave.json()) as { version: string }
          expect(BigInt(v3.version)).toBeGreaterThan(BigInt(head.version))
          const released = await doc.leaseOp(peerCtx, other, "release", { generation: gPeer! })
          expect(released.status()).toBe(200)
          expect(((await released.json()) as LeaseView).holder).toBeNull()
          log.final = { peerSavedVersion: v3.version, releasedHolder: null }
          await prd.evidence("result.json", log)
        } finally {
          for (const c of cleanup.reverse()) await c().catch(() => {})
        }
      },
    )

    test(
      `${label}: two people hand the editor over in the browser`,
      { annotation: [REQ[application].holder, REQ[application].handoff, REQ[application].stale] },
      async ({ prd, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        const cleanup: Array<() => Promise<void>> = []
        const trail: CraftCall[] = []
        let passed = false
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD lease UI ${label}`)
          await requireNative(prd, request, doc)
          const { peer, state, api: peerCtx } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "write")
          const ownerId = await sessionUserId(request)

          // A fresh owner login, not the shared storage state: an earlier
          // browser test may have rotated that refresh family, and reusing it
          // signs this browser out ("Signed out on this device").
          const ownerAccount = prd.env.accounts.owner
          expect(ownerAccount, "owner account").toBeTruthy()
          const ownerContext = await prd.browserContext(await loginStorageState(prd.origin, ownerAccount!))
          const page = await ownerContext.newPage()
          page.setDefaultTimeout(60_000)
          traceCraft(page, "owner", trail)
          const ownerAdmission = captureAdmission(page)
          await page.goto(doc.editorPath)
          const peerContext = await prd.browserContext(state)
          const peerPage = await peerContext.newPage()
          peerPage.setDefaultTimeout(60_000)
          traceCraft(peerPage, "peer", trail)
          await peerPage.goto(doc.editorPath)
          const ownerBar = page.getByRole("toolbar", { name: "Úpravy dokumentu" })
          const peerBar = peerPage.getByRole("toolbar", { name: "Úpravy dokumentu" })
          await expect(ownerBar).toContainText("Volné", { timeout: 60_000 })
          await expect(peerBar).toContainText("Volné", { timeout: 60_000 })

          // Owner takes the editor; the peer sees "Upravuje <owner>".
          const acquiredRes = page.waitForResponse((r) => isCraft(r, "lease/acquire") && r.status() === 200)
          await ownerBar.getByRole("button", { name: "Převzít úpravy", exact: true }).click()
          const gOwner = ((await (await acquiredRes).json()) as LeaseView).generation
          await expect(ownerBar.getByRole("button", { name: "Uvolnit úpravy", exact: true })).toBeVisible()
          const observer = await doc.session(peerCtx)
          const held = await doc.lease(peerCtx, observer)
          expect(held.holder?.actor_id).toBe(`user:${ownerId}`)
          await expect(peerBar).toContainText(`Upravuje ${held.holder?.display_name}`, { timeout: 30_000 })
          await expect(peerBar.getByRole("button", { name: "Převzít úpravy", exact: true })).toHaveCount(0)
          const shots: Record<string, Buffer> = { "peer-sees-owner.png": await peerPage.screenshot({ fullPage: true }) }

          // Peer asks; owner hands over.
          await peerBar.getByRole("button", { name: "Požádat o předání", exact: true }).click()
          const handTo = ownerBar.getByRole("button", { name: `Předat uživateli ${PEER_NAME}`, exact: true })
          await expect(handTo).toBeVisible({ timeout: 30_000 })
          const handoffRes = page.waitForResponse((r) => isCraft(r, "lease/handoff"))
          await handTo.click()
          expect((await handoffRes).status()).toBe(200)
          await expect(ownerBar).toContainText(`Upravuje ${PEER_NAME}`, { timeout: 30_000 })
          await expect(peerBar.getByRole("button", { name: "Uvolnit úpravy", exact: true })).toBeVisible({ timeout: 30_000 })
          shots["owner-sees-peer.png"] = await page.screenshot({ fullPage: true })

          // The owner's browser admission under the retired generation is refused.
          const admission = ownerAdmission.current()
          expect(admission, "the owner's editor was admitted").toBeTruthy()
          const head = await doc.document(peerCtx, observer)
          // Sent from the owner's own browser session: an admission is bound to
          // the login that obtained it, so another session would get 403.
          const stale = await doc.save(ownerContext.request, admission!, { generation: gOwner!, baseVersion: head.version, enginePin: head.enginePin, bytes: Buffer.from(head.bytes, "base64"), editorRevision: `prd-ui-stale-${Date.now()}` })
          expect(stale.status(), "stale-generation save from the former holder's editor").toBe(409)
          const after = await doc.document(peerCtx, observer)
          expect(after.version).toBe(head.version)
          expect(after.bytes).toBe(head.bytes)

          // Peer binds the editor and releases it; both see it free.
          const bind = peerPage.getByRole("button", { name: "Převzít úpravy", exact: true })
          if (await bind.isVisible()) await bind.click()
          const releaseRes = peerPage.waitForResponse((r) => isCraft(r, "lease/release"))
          await peerBar.getByRole("button", { name: "Uvolnit úpravy", exact: true }).click()
          expect((await releaseRes).status()).toBe(200)
          await expect(ownerBar).toContainText("Volné", { timeout: 30_000 })

          await prd.evidence("result.json", {
            application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId,
            peerSaw: `Upravuje ${held.holder?.display_name}`, ownerSaw: `Upravuje ${PEER_NAME}`,
            ownerGeneration: gOwner, staleSave: stale.status(), versionUnchanged: after.version, releasedByPeer: true,
          })
          for (const [name, png] of Object.entries(shots)) await prd.evidence(name, png)
          passed = true
        } finally {
          // On failure the editor calls are the first evidence: which request
          // ended the session is what a red row has to explain.
          if (trail.length) await prd.evidence(passed ? "craft-calls.json" : "failure-craft-calls.json", trail).catch(() => {})
          for (const c of cleanup.reverse()) await c().catch(() => {})
        }
      },
    )

    test(
      `${label}: a late write is refused and the unsaved draft is kept`,
      { annotation: REQ[application].stale },
      async ({ prd, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        test.setTimeout(10 * 60_000)
        const cleanup: Array<() => Promise<void>> = []
        const trail: CraftCall[] = []
        let passed = false
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD lease draft ${label}`)
          await requireNative(prd, request, doc)
          const { peer, api: peerCtx } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "write")
          const ownerAccount = prd.env.accounts.owner
          expect(ownerAccount, "owner account").toBeTruthy()
          const ownerContext = await prd.browserContext(await loginStorageState(prd.origin, ownerAccount!))
          const page = await ownerContext.newPage()
          page.setDefaultTimeout(60_000)
          traceCraft(page, "owner", trail)
          const admission = captureAdmission(page)
          await page.goto(doc.editorPath)
          await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor({ timeout: 90_000 })

          // The owner holds the editor and has typed work that is not saved.
          const LATE = `PRDLATE${Date.now().toString(36)}`
          await editor.acquire(page, application)
          // The editor's own admission (bound to this browser's login): the
          // lease belongs to that session, not to the person's other sessions.
          const ownAdmission = admission.current()
          expect(ownAdmission, "the owner's editor was admitted").toBeTruthy()
          const held = await doc.lease(ownerContext.request, ownAdmission!)
          expect(held.mine, "the owner holds the editor").toBe(true)
          const generation = held.generation!
          const before = await headText(request, doc)
          const savesBefore = trail.filter((c) => c.path === "craft/document/save" && c.status === 200).length

          // The owner keeps typing (no pause long enough for the autosave) and,
          // in the middle of it, the custody moves without the owner's editor
          // flushing: the peer's request is handed over by a call that does not
          // go through the editor.
          await placeCaret(page, application, 7)
          const progress = newTyping()
          const typing = typeLines(page, LATE, 8, 40, 120, progress)
          await page.waitForTimeout(1_600)
          expect(progress.accepted.length, "lines were typed before the handoff").toBeGreaterThan(0)
          const observer = await doc.session(peerCtx)
          const ask = await doc.leaseOp(peerCtx, observer, "request-handoff")
          expect(ask.status()).toBe(201)
          const requestId = ((await ask.json()) as { request_id: string }).request_id
          const moved = await doc.leaseOp(ownerContext.request, ownAdmission!, "handoff", { generation, request_id: requestId })
          expect(moved.status(), `custody moved: ${(await moved.text()).slice(0, 200)}`).toBe(200)
          await typing
          // Every line the editor took (also after the request, until it turned read-only) must be in the draft.
          const typedBefore = typedMarkers(LATE, progress.accepted)
          const offered = await waitForDraftOffer(page, 60_000)

          // The late write under the retired generation is refused and the draft stays.
          const head = await doc.document(ownerContext.request, ownAdmission!).catch(async () => doc.document(request, await doc.session(request)))
          const late = await doc.save(ownerContext.request, ownAdmission!, {
            generation, baseVersion: head.version, enginePin: head.enginePin, bytes: Buffer.from(head.bytes, "base64"), editorRevision: `prd-ui-late-${Date.now()}`,
          })
          expect(late.status(), "the late write under the retired generation").toBe(409)
          const after = await headText(request, doc)
          expect(after.version, "the late write created no version").toBe(before.version)
          expect(after.text).not.toContain(`${LATE}-0`)
          const editorSaves = trail.filter((c) => c.path === "craft/document/save" && c.status === 200).length - savesBefore
          expect(editorSaves, "the editor saved nothing under the retired generation").toBe(0)
          const kept = await inspectDrafts(page, typedBefore)
          expect(kept.hasMarker.some(Boolean), `the offered draft holds all ${typedBefore.length} lines the editor took`).toBe(true)
          const shots: Record<string, Buffer> = { "owner-draft-offered.png": await page.screenshot({ fullPage: true }) }

          // After a reload the draft is offered again once the owner takes the
          // editor back (a draft is never imported into a read-only view), and
          // nothing is saved on the way.
          const peerView = await doc.lease(peerCtx, observer)
          expect(peerView.mine, "the peer holds the editor now").toBe(true)
          expect((await doc.leaseOp(peerCtx, observer, "release", { generation: peerView.generation! })).status(), "the peer releases").toBe(200)
          await page.reload()
          await editor.frameOf(page, application)
          await editor.takeButton(page).click()
          await page.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor({ timeout: 60_000 })
          await waitForDraftOffer(page, 60_000)
          const reloaded = await inspectDrafts(page, typedBefore)
          expect(reloaded.hasMarker.some(Boolean), "the draft with every line is still offered after a reload").toBe(true)
          expect((await headText(request, doc)).text, "the reload saved nothing").not.toContain(`${LATE}-0`)

          await prd.evidence("result.json", {
            application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId,
            lateWrite: late.status(), versionUnchanged: after.version, draftOffered: offered, draftHasWork: kept.hasMarker, acceptedLines: progress.accepted, typingStoppedBy: progress.stoppedBy ?? null, afterReload: reloaded.rows,
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

test(
  "native editor off: lease and document endpoints fail closed",
  { annotation: REQ_DISABLED },
  async ({ prd, request }) => {
    const ws = await prd.qaWorkspace()
    const doc = await NativePage.create(request, ws.id, "wordcraft", "PRD lease disabled")
    // Decide the skip before initialize: a server without a profile that
    // still admits native documents must turn this test red, not skip it.
    test.skip(await nativeProfile(prd, request, doc), "this server runs with native editors; covered by the lease tests")
    const init = await doc.initializeRaw(request)
    if (init.status() === 201) {
      // Fail-closed regression: drop the creation custody, then fail below.
      const body = (await init.json()) as { session_token: string; generation: string }
      await doc.leaseOp(request, { token: body.session_token, sessionId: "" }, "release", { generation: body.generation }).catch(() => undefined)
    }
    expect(init.status(), "initialize without a native profile").toBe(503)
    const availability = await doc.availability(request)
    expect(availability.enabled).toBe(false)
    expect(availability.can_edit).toBe(false)
    const session = await doc.sessionRaw(request)
    expect(session.status(), "admission without a native profile").toBe(503)
    const fake: Admission = { token: "0".repeat(64), sessionId: "none" }
    const acquire = await doc.leaseOp(request, fake, "acquire")
    expect(acquire.status(), "acquire without a native profile").toBe(503)
    const read = await doc.documentRaw(request, fake)
    expect(read.status(), "document read without a native profile").toBe(503)
    await prd.evidence("result.json", { mode: prd.mode, appSha: prd.appSha, availability, initialize: init.status(), session: session.status(), acquire: acquire.status(), document: read.status() })
  },
)
