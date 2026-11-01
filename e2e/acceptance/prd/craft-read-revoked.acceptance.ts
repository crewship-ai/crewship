// PRD A: a reader who has a native Word/Sheet document open loses the read
// right. The event stream must stop, no further content may be delivered, and
// the editor must show the revoked state. Server side this is the contract of
// craft_native_events.go (session_retired / authority_changed / draft purge);
// here it is proven from the outside, as the reader's browser experiences it.
//
// Quick mode drives the stream over the editor's own HTTP API when the server
// runs with a native editor profile (skipped otherwise). Full mode also opens
// the editor in a real browser on a deployed instance with native bundles.
// A Read person is shown the read-only shared preview of the published version
// (not the live editor), so the browser test publishes one first.
import { peerApi, requireNative, traceCraft, type CraftCall } from "./craft-lease/helpers"
import { APPLICATIONS, NativePage, emptyDocument, parseEvents, sessionUserId } from "./craft-lease/native"
import { pause } from "./drafts-crash/crash"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = {
  wordcraft: prdRequirement("A.craft.read-revoked", "Word: when a reader loses read access the event stream stops, no content is delivered and the editor shows it"),
  gridcraft: prdRequirement("A.craft.read-revoked-sheet", "Sheet: when a reader loses read access the event stream stops, no content is delivered and the editor shows it"),
} as const
// #4348: losing only write keeps read. The writer admission is retired with
// reason write_revoked and draft retain; only a FRESH admission reads, and it
// cannot write or reuse the old lease generation.
const WRITE_REQ = {
  wordcraft: prdRequirement("A.craft.write-revoked-word", "Word: when an editor loses only write access the session retires its write authority and a fresh admission reads read-only"),
  gridcraft: prdRequirement("A.craft.write-revoked-sheet", "Sheet: when an editor loses only write access the session retires its write authority and a fresh admission reads read-only"),
} as const

// The server rechecks authority on a 5 s tick; leave generous slack.
const STOP_WITHIN_MS = 45_000
// The shared preview is told by a push (page.access_recheck) and rereads at once;
// the 30 s poll is only the fallback and must not be what makes this pass.
const PREVIEW_REMOVED_WITHIN_MS = 5_000

for (const { application, label } of APPLICATIONS) {
  test.describe(`${label} read right lost`, () => {
    test(
      `${label}: revoking read stops the stream and delivers nothing more (API)`,
      { annotation: REQ[application] },
      async ({ prd, request }) => {
        const cleanup: Array<() => Promise<void>> = []
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD read revoked ${label}`)
          await requireNative(prd, request, doc)
          const { peer, api: readerApi } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "read")
          const log: Record<string, unknown> = { application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId }

          // The reader has the document open: admitted, content readable, read-only.
          const admission = await doc.session(readerApi)
          const open = await doc.document(readerApi, admission)
          expect(open.bytes.length, "the reader receives the document while allowed").toBeGreaterThan(0)
          const lease = await doc.lease(readerApi, admission)
          expect(lease.can_write, "a reader cannot write").toBe(false)
          const probe = open.bytes.slice(0, 64)

          // The stream is live; then the owner withdraws the right.
          const startedAt = Date.now()
          const stream = doc.eventsRaw(readerApi, admission, STOP_WITHIN_MS)
          const outcome = stream.then(
            (res) => ({ res }),
            (error: unknown) => ({ error: String(error).slice(0, 300) }),
          )
          await pause(1500)
          await doc.revoke(request, peer.userId, "read")
          const ended = await outcome
          expect("error" in ended ? ended.error : "", "the event stream ends after the read right is withdrawn").toBe("")
          if ("error" in ended) throw new Error(ended.error)
          const stoppedAfterMs = Date.now() - startedAt
          const body = await ended.res.text()
          const events = parseEvents(body)
          const last = events.at(-1)
          expect(ended.res.status(), "the stream was accepted before the revocation").toBe(200)
          expect(last?.event, "the stream's last event retires the session").toBe("session_retired")
          expect(last?.data).toMatchObject({ delivery: "stopped", reason: "authority_changed", draft: "purge" })
          expect(events.filter((e) => e.event === "session_retired"), "session_retired is sent exactly once, last").toHaveLength(1)
          expect(body, "the stream never carries document content").not.toContain(probe)
          expect(body).not.toMatch(/"bytes"\s*:/)
          log.stream = { status: ended.res.status(), stoppedAfterMs, events: events.map((e) => e.event), last: last?.data }

          // Nothing more is delivered to the old admission, a new one or a new stream.
          const later = await doc.documentRaw(readerApi, admission)
          const laterBody = await later.text()
          expect(later.status(), "document read with the old admission").toBeGreaterThanOrEqual(400)
          expect(laterBody).not.toContain(probe)
          const leaseRead = await readerApi.get(`/api/v1/pages/${encodeURIComponent("@" + doc.pageId)}/craft/lease?workspace_id=${ws.id}`, { headers: { "X-Craft-Session": admission.token } })
          expect(leaseRead.status(), "lease read with the old admission").toBeGreaterThanOrEqual(400)
          const reopened = await doc.sessionRaw(readerApi)
          expect(reopened.status(), "a new admission after the revocation").toBeGreaterThanOrEqual(400)
          expect(await reopened.text()).not.toContain("session_token")
          const again = await doc.eventsRaw(readerApi, admission, 15_000)
          expect(again.status(), "a new event stream with the old admission").toBeGreaterThanOrEqual(400)
          expect(await again.text()).not.toContain("event: lease")
          log.afterRevocation = { document: later.status(), lease: leaseRead.status(), session: reopened.status(), events: again.status() }

          // The owner is unaffected.
          const ownerSession = await doc.session(request)
          expect((await doc.document(request, ownerSession)).bytes, "the owner still reads the document").toBe(open.bytes)
          await prd.evidence("result.json", log)
        } finally {
          for (const c of cleanup.reverse()) await c().catch(() => {})
        }
      },
    )

    test(
      `${label}: revoking only write retires the writer and leaves a fresh read-only admission (API)`,
      { annotation: WRITE_REQ[application] },
      async ({ prd, request }) => {
        const cleanup: Array<() => Promise<void>> = []
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD write revoked ${label}`)
          await requireNative(prd, request, doc)
          const { peer, api: editorApi } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "write")

          // The editor holds the lease and its stream is live.
          const admission = await doc.session(editorApi)
          const head = await doc.document(editorApi, admission)
          const acquired = await doc.leaseOp(editorApi, admission, "acquire")
          expect(acquired.status(), "the editor takes the lease").toBe(200)
          const generation = ((await acquired.json()) as { generation: string }).generation
          const startedAt = Date.now()
          const outcome = doc.eventsRaw(editorApi, admission, STOP_WITHIN_MS).then(
            (res) => ({ res }),
            (error: unknown) => ({ error: String(error).slice(0, 300) }),
          )
          await pause(1500)
          await doc.grant(request, peer.userId, "read")
          await doc.revoke(request, peer.userId, "write")
          const ended = await outcome
          if ("error" in ended) throw new Error(ended.error)
          const events = parseEvents(await ended.res.text())
          expect(events.at(-1)?.event, "the writer admission is retired").toBe("session_retired")
          expect(events.at(-1)?.data, "read remains, so the draft stays and the reason is given").toMatchObject({ delivery: "stopped", reason: "write_revoked", draft: "retain" })
          const stoppedAfterMs = Date.now() - startedAt

          // No write path of the old admission works any more.
          const save = await doc.save(editorApi, admission, { generation, baseVersion: head.version, enginePin: head.enginePin, bytes: emptyDocument(application), editorRevision: "1:1:1" })
          expect([403, 409], "old admission save after the write revoke").toContain(save.status())
          expect((await doc.leaseOp(editorApi, admission, "renew", { generation })).status(), "old admission renew").toBeGreaterThanOrEqual(400)

          // A fresh admission reads the current document, read-only.
          const fresh = await doc.session(editorApi)
          const reread = await doc.document(editorApi, fresh)
          expect(reread.version, "the fresh admission reads the current version").toBe(head.version)
          const view = await doc.lease(editorApi, fresh)
          expect(view.can_edit, "the fresh admission cannot edit").toBe(false)
          expect(view.can_write).toBe(false)
          expect((await doc.leaseOp(editorApi, fresh, "acquire")).status(), "the fresh admission cannot take the lease").toBe(403)
          const reused = await doc.save(editorApi, fresh, { generation, baseVersion: head.version, enginePin: head.enginePin, bytes: emptyDocument(application), editorRevision: "1:2:1" })
          expect([403, 409], "the fresh admission cannot reuse the old generation").toContain(reused.status())
          expect((await doc.document(request, await doc.session(request))).version, "nothing was written").toBe(head.version)
          await prd.evidence("write-revoked.json", { application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId, stoppedAfterMs, save: save.status(), reused: reused.status() })
        } finally {
          for (const c of cleanup.reverse()) await c().catch(() => {})
        }
      },
    )

    test(
      `${label}: the reader's open editor shows the revoked state in the browser`,
      { annotation: REQ[application] },
      async ({ prd, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        const cleanup: Array<() => Promise<void>> = []
        const trail: CraftCall[] = []
        let passed = false
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD read revoked UI ${label}`)
          await requireNative(prd, request, doc)
          const { peer, state, api: readerApi } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "read")
          expect(await sessionUserId(request)).not.toBe(peer.userId)

          // A Read person never opens the live editor: the entry mounts the
          // read-only shared preview (docs/specs/craft-native.md, "Shared
          // previews"), which exists only once the owner has published a
          // version. Without it the reader rightly sees nothing.
          const unpublished = await doc.previewRaw(readerApi)
          expect(unpublished.status(), "no shared version is published yet").toBe(404)
          const version = await doc.publishPreview(request)
          const preview = await doc.previewRaw(readerApi)
          expect(preview.status(), "the reader receives the published version").toBe(200)
          const probe = ((await preview.json()) as { bytes: string }).bytes.slice(0, 64)
          expect(probe.length).toBeGreaterThan(0)

          const readerContext = await prd.browserContext(state)
          const page = await readerContext.newPage()
          page.setDefaultTimeout(60_000)
          traceCraft(page, "reader", trail)
          await page.goto(doc.editorPath)
          await expect(page.getByRole("status").filter({ hasText: `Sdílený náhled · verze ${version}` }).first()).toBeVisible({ timeout: 90_000 })
          await expect(page.locator("iframe"), "the read-only editor with the content is shown").toHaveCount(1, { timeout: 90_000 })
          expect(await page.getByRole("button", { name: "Převzít úpravy", exact: true }).count(), "a reader cannot take the editor").toBe(0)
          expect(trail.some((c) => c.path === "craft/sessions"), "a reader opens no editor admission").toBe(false)
          const shots: Record<string, Buffer> = { "reader-open.png": await page.screenshot({ fullPage: true }) }
          const revokedAt = Date.now()
          await doc.revoke(request, peer.userId, "read")

          // The server pushes "re-check access" to this reader only; the preview
          // rereads at once and removes the content as soon as the read is
          // refused (the 30 s timer and focus/online rereads are the fallback).
          await expect(page.getByRole("alert").filter({ hasText: "Sdílený náhled není dostupný" }).first(), "the preview says access is gone").toBeVisible({ timeout: PREVIEW_REMOVED_WITHIN_MS })
          await expect(page.locator("iframe"), "the document content is removed from the page").toHaveCount(0, { timeout: 1_000 })
          await expect(page.locator("canvas")).toHaveCount(0)
          const shownAfterMs = Date.now() - revokedAt
          expect(shownAfterMs, "removed by the push, not by the 30 s poll").toBeLessThan(PREVIEW_REMOVED_WITHIN_MS)
          shots["reader-revoked.png"] = await page.screenshot({ fullPage: true })

          // Nothing is served any more, not to the old tab, a direct call or a reload.
          const denied = await doc.previewRaw(readerApi)
          expect(denied.status(), "preview read after the revocation").toBe(403)
          expect(await denied.text()).not.toContain(probe)
          // The availability probe answers 200 with every flag false and no content.
          const flags = await readerApi.get(`/api/v1/pages/${encodeURIComponent("@" + doc.pageId)}/craft/availability?workspace_id=${ws.id}`)
          expect(await flags.json(), "availability after the revocation grants nothing").toEqual({ enabled: false, can_edit: false, preview_enabled: false, agents_enabled: false })
          const settled = trail.length
          await page.reload()
          await expect(page.locator("iframe"), "no content after a reload").toHaveCount(0, { timeout: 30_000 })
          expect(trail.slice(settled).filter((c) => c.path === "craft/preview" && c.status === 200), "the reload receives no preview").toEqual([])

          // No further successful editor GET.
          const settled2 = trail.length
          await pause(12_000)
          const after = trail.slice(settled2).filter((c) => c.method === "GET" && c.path !== "craft/availability" && typeof c.status === "number" && c.status < 300)
          expect(after, "no content or event request succeeds after the editor shows the revoked state").toEqual([])
          shots["reader-later.png"] = await page.screenshot({ fullPage: true })

          await prd.evidence("result.json", {
            application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId,
            revokedShownAfterMs: shownAfterMs, requestsAfter: after.length,
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
