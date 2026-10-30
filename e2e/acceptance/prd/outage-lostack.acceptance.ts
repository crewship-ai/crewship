// PRD §7: connection outage and lost save acknowledgement.
//
// Requirement: after an outage or a lost save reply there is no duplicate
// version and no data loss; the lost save is reconciled with the server (or
// offered for comparison); nothing is saved automatically.
//
// Project runs in both modes (Pages API + Project page). Native Word/Sheet
// need the native engines of the deployment, so they run in full mode only and
// are skipped on an origin whose availability probe says native editing is off
// (PRD_REQUIRE_NATIVE=1 turns that skip into a failure).
//
// A "lost reply" is real: the request reaches the server and commits, then the
// browser is cut off before it reads the answer. The Project outage uses the
// browser's offline switch; the native outage uses a local CONNECT proxy that
// severs every tunnel, including the editor's lease event stream.
import type { TestInfo } from "@playwright/test"
import { expect, prdRequirement, test } from "./fixtures"
import { disposeActors, isolatedActor } from "./outage-lostack/actor"
import * as N from "./outage-lostack/native"
import * as P from "./outage-lostack/project"

const OUTAGE_MS = Number(process.env.PRD_OUTAGE_MS ?? 6_000)

test.describe("outage and lost acknowledgement", () => {
  test(
    "Project API: a replayed save cannot create a duplicate and the lost version is readable",
    { annotation: prdRequirement("B.durability.lost-ack.contract", "Project: a save whose reply was lost cannot be committed twice; the exact successor is readable for confirmation") },
    async ({ prd, browser }) => {
      const { api: request, owner, ws } = await isolatedActor(prd, browser, "outage-lostack api")
      const ref = await P.createProject(request, ws.id, "api", prd.mode === "quick", owner)
      const body = P.files(ref.slug, "LOST-REPLY")
      const first = await P.postVersion(request, ref, 1, body)
      expect(first.status, "first save commits").toBe(200)
      expect(first.body?.version).toBe(2)
      // The client never saw that reply and sends the same save again.
      const replay = await P.postVersion(request, ref, 1, body)
      expect(replay.status, "a replay from the old base is a conflict, not a second version").toBe(409)
      const head = await P.snapshot(request, ref)
      const successor = await P.snapshot(request, ref, 2)
      const same = successor.manifest.files.length === body.length && body.every((f) => successor.manifest.files.some((g) => g.path === f.path && g.content === f.content))
      await prd.evidence("result.json", {
        appSha: prd.appSha,
        pageId: ref.pageId,
        firstSave: first,
        replay: { status: replay.status, error: replay.body?.error ?? null },
        latestVersion: head.latest_version,
        versions: head.versions.map((v) => v.version),
        successorMatchesLostSave: same,
      })
      expect(head.latest_version, "exactly one new version").toBe(2)
      expect(head.versions.map((v) => v.version).sort()).toEqual([1, 2])
      expect(same, "GET ?version=base+1 returns exactly the lost save's files").toBe(true)
    },
  )

  test(
    "Project: a lost save reply is confirmed as the exact version",
    { annotation: prdRequirement("B.durability.lost-ack", "Project: lost save reply → the exact version is confirmed, no duplicate, no false conflict, no leftover draft") },
    async ({ prd, browser }, testInfo) => {
      const { context, page, api: request, owner, ws } = await isolatedActor(prd, browser, "outage-lostack lostack")
      const ref = await P.createProject(request, ws.id, "lostack", prd.mode === "quick", owner)
      await P.pinWorkspace(context, request, ws.id)
      const saves = P.countSaves(page)
      const intercepted: { serverStatus: number; serverVersion?: number }[] = []
      await page.route("**/artifact/versions*", async (route) => {
        if (route.request().method() !== "POST") return route.continue()
        const resp = await route.fetch({ maxRetries: 0 })
        const j = (await resp.json().catch(() => null)) as { version?: number } | null
        intercepted.push({ serverStatus: resp.status(), serverVersion: j?.version })
        await route.abort("connectionreset")
      })
      await requireProjectUi(prd.mode, await P.openProject(page, ref), testInfo)
      await page.getByRole("button", { name: "Edit source", exact: true }).click()
      const src = P.sourceBox(page)
      await src.fill((await src.inputValue()).replace(" v1</h1>", " LOST-REPLY</h1>"))
      await P.saveButton(page).click()
      await expect(src, "the editor closes as saved").toHaveCount(0, { timeout: 30_000 })
      await P.pause(2_000)
      const ui = {
        alerts: await P.alerts(page),
        preview: await P.previewHeading(page).innerText().catch(() => "ERR"),
        versionSelect: await page.getByRole("combobox", { name: "Version" }).inputValue().catch(() => null),
        localDrafts: (await P.localDrafts(page)).length,
      }
      const head = await P.snapshot(request, ref)
      await page.unroute("**/artifact/versions*")
      await page.reload({ waitUntil: "domcontentloaded" })
      await page.getByTitle("Artifact preview").waitFor()
      await P.pause(2_000)
      const restoreOffered = await page.getByRole("button", { name: "Restore source draft" }).count()
      await prd.evidence("result.json", { appSha: prd.appSha, pageId: ref.pageId, intercepted, savePosts: saves.posts.length, ui, latestVersion: head.latest_version, versions: head.versions.map((v) => v.version), afterReload: { restoreOffered } })

      expect(intercepted, "the save reached the server exactly once").toHaveLength(1)
      expect(intercepted[0].serverStatus).toBe(200)
      expect(saves.posts, "no automatic retry").toHaveLength(1)
      expect(head.latest_version, "no duplicate version").toBe(2)
      expect(head.versions).toHaveLength(2)
      expect(head.manifest.files.find((f) => f.path === "index.html")?.content).toContain("LOST-REPLY")
      expect(ui.alerts.filter((a) => /changed|conflict/i.test(a)), "no false conflict").toEqual([])
      expect(ui.preview).toContain("LOST-REPLY")
      expect(ui.versionSelect).toBe("2")
      expect(ui.localDrafts, "the confirmed save leaves no local draft").toBe(0)
      expect(restoreOffered, "no stale draft offered after reload").toBe(0)
    },
  )

  test(
    "Project: an outage during save keeps the draft and a retry confirms one version",
    { annotation: prdRequirement("B.durability.outage", "Project: outage cuts off a committed save → draft kept, nothing auto-saved, retry reconciles to the same single version") },
    async ({ prd, browser }, testInfo) => {
      const { context, page, api: request, owner, ws } = await isolatedActor(prd, browser, "outage-lostack outage")
      const ref = await P.createProject(request, ws.id, "outage", prd.mode === "quick", owner)
      await P.pinWorkspace(context, request, ws.id)
      const saves = P.countSaves(page)
      const intercepted: { serverStatus: number; serverVersion?: number; offlineAt: string }[] = []
      let outageStart = 0
      await page.route("**/artifact/versions*", async (route) => {
        if (route.request().method() !== "POST" || intercepted.length > 0) return route.continue()
        const resp = await route.fetch({ maxRetries: 0 })
        const j = (await resp.json().catch(() => null)) as { version?: number } | null
        // The server committed; the connection drops before the reply arrives.
        await context.setOffline(true)
        outageStart = Date.now()
        intercepted.push({ serverStatus: resp.status(), serverVersion: j?.version, offlineAt: new Date().toISOString() })
        await route.abort("internetdisconnected")
      })
      await requireProjectUi(prd.mode, await P.openProject(page, ref), testInfo)
      await page.getByRole("button", { name: "Edit source", exact: true }).click()
      const src = P.sourceBox(page)
      const edited = (await src.inputValue()).replace(" v1</h1>", " OUTAGE</h1>")
      await src.fill(edited)
      await P.saveButton(page).click()
      const report = page.getByRole("alert").filter({ hasText: /\S/ }).first()
      await expect(report, "the unconfirmed save is reported").toBeVisible({ timeout: 30_000 })
      const reported = (await report.innerText().catch(() => "")).trim()
      const during = {
        reported,
        alerts: await P.alerts(page),
        sourceOpen: await src.count(),
        sourceKeepsEdit: (await src.inputValue().catch(() => "")).includes("OUTAGE"),
        localDraftKeepsEdit: (await P.localDrafts(page)).some((d) => d.includes("OUTAGE")),
      }
      const serverDuring = await P.snapshot(request, ref)
      while (Date.now() - outageStart < OUTAGE_MS) await P.pause(250)
      await context.setOffline(false)
      // Longer than the page's 15 s refresh: a reconnect must not save on its own.
      await P.pause(20_000)
      const postsBeforeRetry = saves.posts.length
      const serverAfterReconnect = await P.snapshot(request, ref)

      // The person retries. The draft is in the editor, or offered after a reload.
      let retryPath = "editor"
      if ((await src.count()) === 0) {
        retryPath = "reload+restore"
        await page.reload({ waitUntil: "domcontentloaded" })
        await page.getByRole("button", { name: "Restore source draft" }).click({ timeout: 30_000 })
      }
      await expect(src).toHaveValue(/OUTAGE/)
      await P.saveButton(page).click()
      await expect(src, "the retry is confirmed as saved").toHaveCount(0, { timeout: 30_000 })
      await P.pause(2_000)
      const afterRetry = {
        alerts: await P.alerts(page),
        preview: await P.previewHeading(page).innerText().catch(() => "ERR"),
        localDrafts: (await P.localDrafts(page)).length,
      }
      const head = await P.snapshot(request, ref)
      await prd.evidence("result.json", {
        appSha: prd.appSha,
        pageId: ref.pageId,
        outageMs: OUTAGE_MS,
        intercepted,
        during,
        serverDuring: { latest: serverDuring.latest_version },
        postsBeforeRetry,
        serverAfterReconnect: { latest: serverAfterReconnect.latest_version },
        retryPath,
        afterRetry,
        final: { latest: head.latest_version, versions: head.versions.map((v) => v.version) },
        savePosts: saves.posts.length,
      })

      expect(intercepted).toHaveLength(1)
      expect(intercepted[0].serverStatus, "the cut-off save committed on the server").toBe(200)
      expect(serverDuring.latest_version).toBe(2)
      expect(serverDuring.manifest.files.find((f) => f.path === "index.html")?.content, "no data loss").toContain("OUTAGE")
      expect(during.sourceKeepsEdit || during.localDraftKeepsEdit, "the draft is kept while unconfirmed").toBe(true)
      expect(postsBeforeRetry, "nothing is saved automatically during or after the outage").toBe(1)
      expect(serverAfterReconnect.latest_version).toBe(2)
      expect(afterRetry.alerts.filter((a) => /changed|conflict/i.test(a)), "the retry is reconciled, not reported as a conflict").toEqual([])
      expect(afterRetry.preview).toContain("OUTAGE")
      expect(afterRetry.localDrafts, "the reconciled draft is cleared").toBe(0)
      expect(head.latest_version, "no duplicate version").toBe(2)
      expect(head.versions).toHaveLength(2)
    },
  )

  for (const app of ["wordcraft", "gridcraft"] as const) {
    const label = N.APP_LABEL[app]

    test(
      `${label}: a lost save reply is reconciled to the exact version`,
      { annotation: prdRequirement(`A.resilience.lost-ack.${label.toLowerCase()}`, `${label}: lost save reply (connection alive) → reconciled to the exact server version, next save contiguous, no duplicate, nothing auto-saved`) },
      async ({ prd, browser }) => {
        prd.requireFull(`native ${label} needs a deployment with native engines`)
        const { context: ctx, api: request, ws } = await isolatedActor(prd, browser, `outage-lostack ${label}`, { viewport: N.VIEWPORT })
        await requireNative(request, ws.id)
        const ref = await N.createNative(request, ws.id, app, `PRD outage lost-ack ${label}`)
        try {
          const ops = N.instrument(ctx)
          const tag = Math.random().toString(36).slice(2, 6)
          const M = { M1: `M1before${tag}`, M2: `M2lost${tag}`, M4: `M4next${tag}` }
          const lost: { serverStatus?: number; serverVersion?: string; serverHash?: string }[] = []
          let armed = false
          await ctx.route("**/craft/document/save**", async (route) => {
            if (!armed) return route.continue()
            armed = false
            const res = await route.fetch({ maxRetries: 0 })
            const j = (await res.json().catch(() => null)) as { version?: unknown; contentHash?: string } | null
            lost.push({ serverStatus: res.status(), serverVersion: j?.version === undefined ? undefined : String(j.version), serverHash: j?.contentHash })
            await route.abort("connectionreset")
          })
          const page = await ctx.newPage()
          await N.openEditor(page, ref)
          let f = await N.acquire(page, app)
          await N.typeMarker(page, f, app, M.M1, "A1")
          await page.keyboard.press("Control+s")
          const v0 = await N.waitSaved(page)
          armed = true
          await N.typeMarker(page, f, app, M.M2, "A2")
          await page.keyboard.press("Control+s")
          await expect.poll(() => lost.length, { timeout: 20_000 }).toBe(1)
          const opsAtLoss = ops.length
          // Untouched for longer than the lease TTL: the state must stay honest.
          const timeline: Awaited<ReturnType<typeof N.uiState>>[] = []
          let reconciledBy: "automatic" | "verify" | null = null
          let saved: string | undefined
          for (let i = 0; i < 20 && !saved; i++) {
            await N.pause(1_000)
            const s = await N.uiState(page)
            timeline.push(s)
            saved = s.status.match(/^Uloženo · verze (\d+)/)?.[1]
            if (saved) reconciledBy = "automatic"
          }
          const headUncertain = await N.readHead(request, ref)
          const savesWhileUncertain = ops.slice(opsAtLoss).filter((o) => o.kind === "save").length
          if (!saved && (await page.getByRole("button", { name: "Ověřit výsledek", exact: true }).count())) {
            await page.getByRole("button", { name: "Ověřit výsledek", exact: true }).click()
            saved = await N.waitSaved(page, 15_000)
            if (saved) reconciledBy = "verify"
          }
          let next: string | undefined
          let afterNext: Awaited<ReturnType<typeof N.uiState>> | null = null
          if (saved) {
            f = await N.frameOf(page, app)
            await f.locator("canvas").first().click({ position: app === "wordcraft" ? { x: 678, y: 324 } : { x: 54, y: 240 } })
            await N.typeMarker(page, f, app, M.M4, "A4")
            await page.keyboard.press("Control+s")
            next = await N.waitSaved(page)
            afterNext = await N.uiState(page)
            await N.release(page).catch(() => {})
          }
          const fin = await N.readHead(request, ref)
          // The first committed save after the reconciliation must build on the
          // reconciled version: base = lost version, result = lost version + 1.
          const reconcileAt = ops.findIndex((o) => o.kind === "reconcile" && o.status === 200)
          const afterLoss = ops.slice(Math.max(reconcileAt, opsAtLoss))
          // A save of the already committed (lost-reply) bytes after the loss is a duplicate version.
          const resavedLost = afterLoss.filter((o) => o.kind === "save" && o.contentHash === lost[0]?.serverHash)
          const firstNext = afterLoss.find((o) => o.kind === "save" && o.status === 200 && o.contentHash !== lost[0]?.serverHash)
          const identicalRepeats = repeatedSaves(ops)
          await prd.evidence("result.json", {
            appSha: prd.appSha,
            app,
            pageId: ref.pageId,
            initVersion: ref.initVersion,
            baselineSaved: v0,
            lost,
            headWhileUncertain: { version: headUncertain.version, hash: headUncertain.hash, hasM2: headUncertain.text.includes(M.M2) },
            timeline: timeline.slice(-3),
            savesWhileUncertain,
            reconciledBy,
            savedLabel: saved ?? null,
            nextSavedLabel: next ?? null,
            afterNext,
            firstNextSave: firstNext ?? null,
            resavedLost,
            identicalRepeats,
            ops,
            final: { version: fin.version, hash: fin.hash, ...Object.fromEntries(Object.entries(M).map(([k, v]) => [k, fin.text.includes(v)])) },
          })

          expect(lost[0]?.serverStatus, "the lost save committed on the server").toBe(200)
          expect(headUncertain.version).toBe(lost[0].serverVersion)
          expect(savesWhileUncertain, "nothing is re-saved while the result is unconfirmed").toBe(0)
          expect(saved, "the editor confirms the exact server version (automatically or via Ověřit výsledek)").toBe(lost[0].serverVersion)
          const successor = String(BigInt(lost[0].serverVersion!) + 1n)
          expect.soft(resavedLost.map((o) => ({ base: o.baseVersion, status: o.status, version: o.version })), "the reconciled save is not saved again as a duplicate version").toEqual([])
          expect.soft(firstNext && { base: firstNext.baseVersion, version: firstNext.version }, "the next save builds on the reconciled version (no duplicate)").toEqual({ base: lost[0].serverVersion, version: successor })
          expect.soft(next, "the editor confirms the next save").toBe(successor)
          expect(fin.text.includes(M.M1) && fin.text.includes(M.M2) && fin.text.includes(M.M4), "no data loss").toBe(true)
        } finally {
          await ctx.close()
        }
      },
    )

    test(
      `${label}: a connection outage with a lost save loses nothing and duplicates nothing`,
      { annotation: prdRequirement(`A.resilience.outage.${label.toLowerCase()}`, `${label}: outage cuts off a committed save → no data loss, no duplicate version, nothing auto-saved, the save is reconciled (not offered back as an identical draft)`) },
      async ({ prd, browser }) => {
        prd.requireFull(`native ${label} needs a deployment with native engines`)
        expect(prd.origin.startsWith("https://"), "the outage proxy cuts CONNECT tunnels; the origin must be https").toBe(true)
        const proxy = await N.startProxy()
        const { context: ctx, api: request, ws } = await isolatedActor(prd, browser, `outage-lostack ${label}`, { viewport: N.VIEWPORT, proxy: { server: proxy.server } })
        await requireNative(request, ws.id)
        const ref = await N.createNative(request, ws.id, app, `PRD outage ${label}`)
        try {
          const ops = N.instrument(ctx)
          const tag = Math.random().toString(36).slice(2, 6)
          const M = { M1: `M1before${tag}`, M2: `M2lost${tag}` }
          const lost: { serverStatus?: number; serverVersion?: string; serverHash?: string; severed?: number }[] = []
          let armed = false
          let outageStart = 0
          await ctx.route("**/craft/document/save**", async (route) => {
            if (!armed) return route.continue()
            armed = false
            const res = await route.fetch({ maxRetries: 0, timeout: 30_000 })
            const j = (await res.json().catch(() => null)) as { version?: unknown; contentHash?: string } | null
            const severed = proxy.down()
            outageStart = Date.now()
            lost.push({ serverStatus: res.status(), serverVersion: j?.version === undefined ? undefined : String(j.version), serverHash: j?.contentHash, severed })
            await route.abort("internetdisconnected")
          })
          let page = await ctx.newPage()
          await N.openEditor(page, ref)
          const f = await N.acquire(page, app)
          await N.typeMarker(page, f, app, M.M1, "A1")
          await page.keyboard.press("Control+s")
          const v0 = await N.waitSaved(page)
          armed = true
          await N.typeMarker(page, f, app, M.M2, "A2")
          await page.keyboard.press("Control+s")
          await expect.poll(() => lost.length, { timeout: 20_000 }).toBe(1)
          const timeline: Awaited<ReturnType<typeof N.uiState>>[] = []
          await N.pause(500)
          timeline.push(await N.uiState(page))
          while (Date.now() - outageStart < OUTAGE_MS) await N.pause(250)
          proxy.up()
          // Back online, untouched: does the editor reconcile on its own?
          let reconciled: string | undefined
          for (let i = 0; i < 15 && !reconciled; i++) {
            await N.pause(1_000)
            const s = await N.uiState(page)
            timeline.push(s)
            reconciled = s.status.match(/^Uloženo · verze (\d+)/)?.[1]
          }
          let reconciledBy: "automatic" | "verify" | null = reconciled ? "automatic" : null
          if (!reconciled && (await page.getByRole("button", { name: "Ověřit výsledek", exact: true }).count())) {
            await page.getByRole("button", { name: "Ověřit výsledek", exact: true }).click()
            reconciled = await N.waitSaved(page, 15_000)
            if (reconciled) reconciledBy = "verify"
          }
          const headAfterOutage = await N.readHead(request, ref)
          const opsPhase1 = ops.length
          await page.close()

          // Reopen in the same browser profile (same local draft journal) after the lease TTL.
          await N.pause(20_000)
          page = await ctx.newPage()
          await N.openEditor(page, ref)
          await N.frameOf(page, app)
          await N.pause(1_500)
          if (await page.getByRole("button", { name: "Převzít úpravy", exact: true }).count()) {
            await page.getByRole("button", { name: "Převzít úpravy", exact: true }).click()
            await page.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor({ timeout: 30_000 }).catch(() => {})
            await N.pause(3_000)
          }
          const reopened = await N.uiState(page)
          const comparisons = await N.compareRecoveries(page, M)
          await N.pause(5_000)
          const savesAfterReopen = ops.slice(opsPhase1).filter((o) => o.kind === "save").length
          const headReopened = await N.readHead(request, ref)
          const committedSaves = ops.filter((o) => o.kind === "save" && o.status === 200).length + lost.filter((l) => l.serverStatus === 200).length
          const expectedHead = String(BigInt(ref.initVersion) + BigInt(committedSaves))
          const opsBeforeRelease = ops.length
          await N.release(page).catch(() => {})
          const headReleased = await N.readHead(request, ref)

          await prd.evidence("result.json", {
            appSha: prd.appSha,
            app,
            pageId: ref.pageId,
            outageMs: OUTAGE_MS,
            initVersion: ref.initVersion,
            baselineSaved: v0,
            lost,
            timeline,
            reconciledBy,
            reconciledLabel: reconciled ?? null,
            reconcileRequests: ops.filter((o) => o.kind === "reconcile"),
            headAfterOutage: { version: headAfterOutage.version, hash: headAfterOutage.hash, hasM1: headAfterOutage.text.includes(M.M1), hasM2: headAfterOutage.text.includes(M.M2) },
            reopened,
            comparisons,
            savesAfterReopen,
            headReopened: { version: headReopened.version, hash: headReopened.hash },
            headReleased: { version: headReleased.version, hash: headReleased.hash },
            versionAccounting: { committedSavesBeforeRelease: committedSaves, expectedHead },
            releaseOps: ops.slice(opsBeforeRelease),
            identicalRepeats: repeatedSaves(ops),
            ops,
          })

          expect(lost[0]?.serverStatus, "the cut-off save committed on the server").toBe(200)
          expect(headAfterOutage.text.includes(M.M1) && headAfterOutage.text.includes(M.M2), "no data loss").toBe(true)
          expect.soft(savesAfterReopen, "nothing is saved automatically after reopening").toBe(0)
          expect.soft(headReopened.version, "no duplicate version").toBe(expectedHead)
          const identicalOffered = comparisons.filter((c) => c.equal)
          expect.soft(
            reconciled === lost[0].serverVersion || identicalOffered.length === 0,
            "the committed save is reconciled, not offered back as a local draft with identical data",
          ).toBe(true)
          expect.soft(identicalOffered.map((c) => c.caption), "no recovery draft identical to the server version").toEqual([])
          expect.soft(
            headReleased.version === headReopened.version || headReleased.hash !== headReopened.hash,
            "releasing the editor does not re-save identical bytes as a new version",
          ).toBe(true)
        } finally {
          await ctx.close()
          proxy.close()
        }
      },
    )
  }
})

test.afterEach(disposeActors)

/** Committed saves whose bytes equal the previous committed save (recorded, not asserted outside the outage window). */
function repeatedSaves(ops: N.Op[]): { n: number; baseVersion?: string; version?: string }[] {
  let prev: string | undefined
  const out: { n: number; baseVersion?: string; version?: string }[] = []
  for (const o of ops) {
    if (o.status !== 200 || !o.contentHash) continue
    if (o.contentHash === prev) out.push({ n: o.n, baseVersion: o.baseVersion, version: o.version })
    prev = o.contentHash
  }
  return out
}

/** Quick mode only: skip when this build has the Project surface switched off. */
async function requireProjectUi(mode: string, opened: boolean, testInfo: TestInfo): Promise<void> {
  if (opened) return
  testInfo.skip(mode === "quick", "Project page not available in this build (NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS off)")
  throw new Error("the Project page did not render its preview")
}

async function requireNative(request: Parameters<typeof N.nativeEnabled>[0], workspaceId: string): Promise<void> {
  const probe = await N.nativeEnabled(request, workspaceId)
  if (probe.enabled) return
  test.skip(process.env.PRD_REQUIRE_NATIVE !== "1", `native Word/Sheet editing is off on this origin (${probe.detail}); PRD_REQUIRE_NATIVE=1 makes this a failure`)
  throw new Error(`native Word/Sheet editing is off on this origin: ${probe.detail}`)
}
