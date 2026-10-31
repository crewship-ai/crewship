// PRD A: a native Word or Sheet document survives at least 2,000 saves and a
// native state above 4 MiB. The store proves it in isolation
// (TestNativeStoreWholeStateBeyondCheckpointAndSaveCountLimits); this drives
// the same editor session API the browser's editor host calls (sessions,
// lease/acquire, lease/renew, document/save, document) and then reopens the
// document with a fresh login. Driving the API is how the 2,000 iterations
// stay affordable; the editor itself is covered by the round trip scenarios.
//
// Skipped in quick mode when the server has no native editor profile.
import { apiAs, loginStorageState } from "./auth"
import { requireNative } from "./craft-lease/helpers"
import { APPLICATIONS, NativePage, type LeaseView } from "./craft-lease/native"
import { bigDocument, digest, MIN_NATIVE_BYTES, SOAK_SAVES } from "./craft-soak/documents"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = {
  wordcraft: prdRequirement("A.craft.soak.saves-2000", "Word: a document above 4 MiB survives 2,000 saves; a fresh login reopens exact content and version count"),
  gridcraft: prdRequirement("A.craft.soak.saves-2000-sheet", "Sheet: a workbook above 4 MiB survives 2,000 saves; a fresh login reopens exact content and version count"),
} as const

// Leases live at most a minute; renew well inside that.
const RENEW_EVERY_MS = 5_000
const READMIT_EVERY_MS = 8 * 60_000

for (const { application, label } of APPLICATIONS) {
  test.describe(`${label} soak`, () => {
    test(`${label}: 2,000 saves of a document above 4 MiB, then reopen`, { annotation: REQ[application] }, async ({ prd, request }) => {
      test.setTimeout(prd.mode === "full" ? 60 * 60_000 : 20 * 60_000)
      const cleanup: Array<() => Promise<void>> = []
      try {
        const ws = await prd.qaWorkspace()
        const doc = await NativePage.create(request, ws.id, application, `PRD soak ${label}`)
        await requireNative(prd, request, doc)
        const log: Record<string, unknown> = { application, mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId, saves: SOAK_SAVES }

        const marker = `prd-soak-final-${Date.now().toString(36)}`
        const body = bigDocument(application, null)
        const last = bigDocument(application, marker)
        expect(body.length, "native state above 4 MiB").toBeGreaterThan(4 * 1024 * 1024)
        expect(body.length).toBeGreaterThan(MIN_NATIVE_BYTES - 1)

        // The login lives about 15 minutes, less than a slow run. Before it ends the
        // editor re-admits like a reloaded tab: release custody, sign in again, take a
        // fresh admission and a new generation, continue on the current head.
        let api = request
        let loggedInAt = Date.now()
        let session = await doc.session(api)
        let generation = ((await (await doc.leaseOp(api, session, "acquire")).json()) as LeaseView).generation!
        const head = await doc.document(api, session)
        let version = head.version
        const first = BigInt(version)
        const reAdmit = async () => {
          const released = await doc.leaseOp(api, session, "release", { generation })
          expect(released.status(), "release custody before signing in again").toBe(200)
          const account = prd.env.accounts.owner
          expect(account, "owner account").toBeTruthy()
          api = await apiAs(prd.origin, await loginStorageState(prd.origin, account!))
          cleanup.push(() => api.dispose())
          session = await doc.session(api)
          const view = (await (await doc.leaseOp(api, session, "acquire")).json()) as LeaseView
          expect(view.mine, "the owner holds the editor again").toBe(true)
          generation = view.generation!
          loggedInAt = Date.now()
          readmissions++
        }
        let readmissions = 0
        let renewedAt = Date.now()
        for (let i = 1; i <= SOAK_SAVES; i++) {
          if (Date.now() - loggedInAt >= READMIT_EVERY_MS) await reAdmit()
          if (Date.now() - renewedAt >= RENEW_EVERY_MS) {
            renewedAt = Date.now()
            const renewed = await doc.leaseOp(api, session, "renew", { generation })
            expect(renewed.status(), `renew the lease before save ${i}`).toBe(200)
          }
          const bytes = i === SOAK_SAVES ? last : body
          const res = await doc.save(api, session, { generation, baseVersion: version, enginePin: head.enginePin, bytes, editorRevision: `prd-soak-${i}` })
          if (res.status() !== 200) throw new Error(`save ${i}/${SOAK_SAVES} on version ${version}: HTTP ${res.status()} ${(await res.text()).slice(0, 300)}`)
          const receipt = (await res.json()) as { version: string; phase: string }
          expect(receipt.phase, `save ${i} committed`).toBe("committed")
          expect(BigInt(receipt.version), `save ${i} advanced the version`).toBe(BigInt(version) + 1n)
          version = receipt.version
        }
        expect(BigInt(version) - first, "versions written").toBe(BigInt(SOAK_SAVES))
        const released = await doc.leaseOp(api, session, "release", { generation })
        expect(released.status()).toBe(200)

        // Reopen with a fresh login and a fresh admission.
        const owner = prd.env.accounts.owner
        expect(owner, "owner account").toBeTruthy()
        const reopenApi = await apiAs(prd.origin, await loginStorageState(prd.origin, owner!))
        cleanup.push(() => reopenApi.dispose())
        const reopened = await doc.document(reopenApi, await doc.session(reopenApi))
        const got = Buffer.from(reopened.bytes, "base64")
        expect(reopened.version, "version count after reopen").toBe(version)
        expect(reopened.version).toBe(String(first + BigInt(SOAK_SAVES)))
        expect(got.length, "byte length").toBe(last.length)
        expect(digest(got), "exact bytes").toBe(digest(last))
        expect(got.toString("utf8").includes(marker), "the last save's content is present").toBe(true)

        log.result = { readmissions, firstVersion: first.toString(), finalVersion: reopened.version, bytes: got.length, sha256: digest(got) }
        await prd.evidence("result.json", log)
      } finally {
        for (const c of cleanup.reverse()) await c().catch(() => {})
      }
    })
  })
}
