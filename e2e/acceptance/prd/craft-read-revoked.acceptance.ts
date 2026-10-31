// PRD A: a reader who has a native Word/Sheet document open loses the read
// right. The event stream must stop, no further content may be delivered, and
// the editor must show the revoked state. Server side this is the contract of
// craft_native_events.go (session_retired / authority_changed / draft purge);
// here it is proven from the outside, as the reader's browser experiences it.
//
// Quick mode drives the stream over the editor's own HTTP API when the server
// runs with a native editor profile (skipped otherwise). Full mode also opens
// the editor in a real browser on a deployed instance with native bundles.
import { peerApi, requireNative, traceCraft, type CraftCall } from "./craft-lease/helpers"
import { APPLICATIONS, NativePage, parseEvents, sessionUserId } from "./craft-lease/native"
import { pause } from "./drafts-crash/crash"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = {
  wordcraft: prdRequirement("A.craft.read-revoked", "Word: when a reader loses read access the event stream stops, no content is delivered and the editor shows it"),
  gridcraft: prdRequirement("A.craft.read-revoked-sheet", "Sheet: when a reader loses read access the event stream stops, no content is delivered and the editor shows it"),
} as const

// The server rechecks authority on a 5 s tick; leave generous slack.
const STOP_WITHIN_MS = 45_000

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
          const { peer, state } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "read")
          expect(await sessionUserId(request)).not.toBe(peer.userId)

          const readerContext = await prd.browserContext(state)
          const page = await readerContext.newPage()
          page.setDefaultTimeout(60_000)
          traceCraft(page, "reader", trail)
          await page.goto(doc.editorPath)
          await expect(page.getByRole("status").filter({ hasText: "Pouze čtení" }).first()).toBeVisible({ timeout: 90_000 })
          await expect(page.locator("iframe")).toHaveCount(1, { timeout: 90_000 })
          const shots: Record<string, Buffer> = { "reader-open.png": await page.screenshot({ fullPage: true }) }
          const streamOpen = trail.some((c) => c.path === "craft/events")
          const revokedAt = Date.now()
          await doc.revoke(request, peer.userId, "read")

          await expect(page.getByRole("status").filter({ hasText: "Přístup ke čtení byl odebrán" }).first(), "the editor says read access was withdrawn").toBeVisible({ timeout: STOP_WITHIN_MS })
          await expect(page.locator("iframe"), "the document content is removed from the page").toHaveCount(0)
          await expect(page.locator("canvas")).toHaveCount(0)
          const shownAfterMs = Date.now() - revokedAt
          shots["reader-revoked.png"] = await page.screenshot({ fullPage: true })
          expect(await page.getByRole("button", { name: "Převzít úpravy", exact: true }).count(), "no way to take the editor").toBe(0)

          // The stream is over and stays over: no further successful editor GET.
          await expect.poll(() => trail.some((c) => c.path === "craft/events" && c.status === "stream ended"), { timeout: 30_000, message: "the event stream ended" }).toBe(true)
          const settled = trail.length
          await pause(12_000)
          const after = trail.slice(settled).filter((c) => c.method === "GET" && typeof c.status === "number" && c.status < 300)
          expect(after, "no content or event request succeeds after the editor shows the revoked state").toEqual([])
          shots["reader-later.png"] = await page.screenshot({ fullPage: true })

          await prd.evidence("result.json", {
            application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId,
            streamOpenBeforeRevoke: streamOpen, revokedShownAfterMs: shownAfterMs, requestsAfter: after.length,
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
