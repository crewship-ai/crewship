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

const RENEW_EVERY = 100

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

        const session = await doc.session(request)
        const acquired = (await (await doc.leaseOp(request, session, "acquire")).json()) as LeaseView
        expect(acquired.mine, "the owner holds the editor").toBe(true)
        const generation = acquired.generation!
        const head = await doc.document(request, session)
        let version = head.version
        const first = BigInt(version)

        for (let i = 1; i <= SOAK_SAVES; i++) {
          if (i % RENEW_EVERY === 0) {
            const renewed = await doc.leaseOp(request, session, "renew", { generation })
            expect(renewed.status(), `renew the lease before save ${i}`).toBe(200)
          }
          const bytes = i === SOAK_SAVES ? last : body
          const res = await doc.save(request, session, { generation, baseVersion: version, enginePin: head.enginePin, bytes, editorRevision: `prd-soak-${i}` })
          if (res.status() !== 200) throw new Error(`save ${i}/${SOAK_SAVES} on version ${version}: HTTP ${res.status()} ${(await res.text()).slice(0, 300)}`)
          const receipt = (await res.json()) as { version: string; phase: string }
          expect(receipt.phase, `save ${i} committed`).toBe("committed")
          expect(BigInt(receipt.version), `save ${i} advanced the version`).toBe(BigInt(version) + 1n)
          version = receipt.version
        }
        expect(BigInt(version) - first, "versions written").toBe(BigInt(SOAK_SAVES))
        const released = await doc.leaseOp(request, session, "release", { generation })
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

        log.result = { firstVersion: first.toString(), finalVersion: reopened.version, bytes: got.length, sha256: digest(got) }
        await prd.evidence("result.json", log)
      } finally {
        for (const c of cleanup.reverse()) await c().catch(() => {})
      }
    })
  })
}
