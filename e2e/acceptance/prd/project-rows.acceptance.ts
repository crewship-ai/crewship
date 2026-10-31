// PRD §7 milestone B, "project": concurrent saves and a server restart.
//
// conflict: two people (two browser sessions) edit the same Project from the
// same base version. The second save is refused with a clear conflict in the
// page and the second person's work is not lost: the typed source stays in
// the editor, the stored version is the first person's, and after a reload the
// draft is offered again (restoring it gives the typed text back).
//
// restore-restart: restoring an old version makes a new one and deletes
// nothing; the history is the same after a reload and after the server was
// restarted (PRD_RESTART_COMMAND or PRD_RESTART_HANDSHAKE, see docs/development/prd-acceptance.md).
import { apiAs, loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import { canRestart, pause, restartServer } from "./ops"
import { createProject as createQuotaProject, openProject as openPagesProject, sourceBox } from "./quota/project"
import { readAppBuild } from "./app-sha"
import {
  HERO, createProject, enableProjects, heroOf, historyRow, openHistory, openProject, pinWorkspace, preview, projectSlug, publish, snapshot, threeScreens,
} from "./project/project-api"

const V1 = "Welcome to the PRD cafe"
const V2 = "Welcome to the PRD cafe, open 8-18"

test.describe("project rows", () => {
  test(
    "two concurrent saves from the same base: the second gets a conflict and keeps its work",
    { annotation: prdRequirement("B.project.conflict", "Two saves from the same base version: the second one gets a clear conflict in the page and its work is kept") },
    async ({ prd, request, page, browser }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const project = await createQuotaProject(request, ws.id)
      const ownerAccount = prd.requireAccount("owner")
      // The second person: a separate session (own cookies, own browser storage).
      const second = await browser.newContext({ baseURL: prd.origin, storageState: await loginStorageState(prd.origin, ownerAccount), viewport: { width: 1440, height: 1000 } })
      try {
        const pageB = await second.newPage()
        await pinWorkspace(page.context(), ws.id)
        await pinWorkspace(second, ws.id)
        const tag = Date.now().toString(36)
        const A = `FIRST-SAVE-${tag}`
        const B = `SECOND-SAVE-${tag}`

        // Both open the Project at version 1 and start editing the source.
        await openPagesProject(page, project.slug, ws.id)
        await openPagesProject(pageB, project.slug, ws.id)
        await page.getByRole("button", { name: "Edit source", exact: true }).click()
        await pageB.getByRole("button", { name: "Edit source", exact: true }).click()
        const edit = async (p: typeof page, marker: string) => {
          const box = sourceBox(p)
          const text = (await box.inputValue()).replace("Quota QA paragraph.", marker)
          expect(text).toContain(marker)
          await box.fill(text)
          return text
        }
        const textA = await edit(page, A)
        const textB = await edit(pageB, B)

        // The first save wins.
        const savedA = page.waitForResponse((r) => r.request().method() === "POST" && r.url().includes("/artifact/versions"))
        await page.getByRole("button", { name: "Save project", exact: true }).click()
        const resA = await savedA
        expect(resA.status(), await resA.text()).toBe(200)

        // The second one is refused.
        const savedB = pageB.waitForResponse((r) => r.request().method() === "POST" && r.url().includes("/artifact/versions"))
        await pageB.getByRole("button", { name: "Save project", exact: true }).click()
        const resB = await savedB
        const bodyB = (await resB.json().catch(() => ({}))) as { code?: string; error?: string }
        await pause(1500)
        const alertsB = (await pageB.getByRole("alert").allInnerTexts()).map((s) => s.trim()).filter(Boolean)
        const afterConflict = {
          status: resB.status(),
          code: bodyB.code ?? null,
          error: bodyB.error ?? null,
          alerts: alertsB,
          sourceKept: (await sourceBox(pageB).count()) === 1 && (await sourceBox(pageB).inputValue()) === textB,
          draftNote: await pageB.getByText(/Kept in this browser until you save or discard/).count(),
        }
        await pageB.screenshot({ path: test.info().outputPath("second-person-conflict.png"), fullPage: true })

        // Nothing of the second person reached the server, nothing of the first was overwritten.
        const read = await request.get(`/api/v1/pages/@${encodeURIComponent(project.pageId)}/artifact?workspace_id=${encodeURIComponent(ws.id)}`)
        const head = (await read.json()) as { latest_version: number; manifest: { files: Array<{ path: string; content: string }> } }
        const stored = head.manifest.files.find((f) => f.path === "index.html")?.content ?? ""

        // After a reload the second person's draft is offered again, and restoring gives the typed text back.
        await pageB.reload({ waitUntil: "domcontentloaded" })
        await expect(pageB.getByTitle("Artifact preview")).toBeVisible({ timeout: 60_000 })
        await pageB.waitForTimeout(2500)
        const restore = pageB.getByRole("button", { name: "Restore source draft", exact: true })
        const offered = await restore.count()
        let restoredIdentical: boolean | null = null
        let reloadNote: string | null = null
        if (offered === 1) {
          reloadNote = (await pageB.getByRole("status").allInnerTexts()).map((s) => s.trim()).find((s) => /draft/i.test(s)) ?? null
          await restore.click()
          restoredIdentical = (await sourceBox(pageB).inputValue()) === textB
        }
        await pageB.screenshot({ path: test.info().outputPath("second-person-after-reload.png"), fullPage: true })
        await prd.evidence("result.json", {
          mode: prd.mode, appSha: prd.appSha, workspace: ws.id, project: project.slug,
          firstSave: { status: resA.status() },
          secondSave: afterConflict,
          server: { latest: head.latest_version, hasFirst: stored.includes(A), hasSecond: stored.includes(B) },
          afterReload: { restoreOffered: offered, restoredIdentical, note: reloadNote },
          textALength: textA.length,
        })

        expect(afterConflict.status, "the second save from the same base is refused").toBe(409)
        expect(afterConflict.alerts.join(" | "), "the page says the project changed").toMatch(/project changed/i)
        expect(afterConflict.alerts.join(" | "), "the page says the draft is preserved").toMatch(/draft is preserved/i)
        expect.soft(afterConflict.sourceKept, "the typed source stays in the editor").toBe(true)
        expect.soft(head.latest_version, "no third version was created").toBe(2)
        expect.soft(stored.includes(A) && !stored.includes(B), "the stored version is the first person's").toBe(true)
        expect.soft(offered, "after reload the second person's draft is offered again").toBe(1)
        expect.soft(restoredIdentical, "the restored draft is what the second person typed").toBe(true)
      } finally {
        await second.close().catch(() => {})
      }
    },
  )

  test(
    "restoring an older version survives a reload and a server restart",
    { annotation: prdRequirement("B.project.restore-restart", "Restoring an old version creates a new version and deletes nothing; it survives a reload and a server restart") },
    async ({ prd, page, request, context }) => {
      test.setTimeout(Math.max(test.info().timeout, 10 * 60_000))
      prd.requireFull("the server is restarted")
      if (!canRestart()) throw new Error("full mode needs PRD_RESTART_COMMAND (for example \"crew restart dev2\") or PRD_RESTART_HANDSHAKE for this scenario")
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      await createProject(request, ws.id, slug, threeScreens(V1))
      expect((await publish(request, ws.id, slug, 1, threeScreens(V2), "v2")).status()).toBe(200)
      await pinWorkspace(context, ws.id)

      // Restore v1 in the page.
      await openProject(page, ws.id, slug)
      await openHistory(page)
      const restored = page.waitForResponse((r) => r.url().includes("/artifact/restore") && r.request().method() === "POST")
      await historyRow(page, 1).getByRole("button", { name: "Restore as new version" }).click()
      const restoredRes = await restored
      expect(restoredRes.status(), await restoredRes.text()).toBe(200)
      await expect(page.getByText(/Restored v1 as v3/)).toBeVisible()
      const shape = (s: Awaited<ReturnType<typeof snapshot>>) => ({
        latest: s.latest_version,
        versions: s.versions.map((v) => ({ version: v.version, restored_from: v.restored_from ?? null })).sort((a, b) => a.version - b.version),
        hero: heroOf(s),
      })
      const before = shape(await snapshot(request, ws.id, slug))
      expect(before.latest).toBe(3)
      expect(before.versions.map((v) => v.version)).toEqual([1, 2, 3])
      expect(before.versions.find((v) => v.version === 3)?.restored_from).toBe(1)
      expect(before.hero).toBe(V1)

      await page.reload()
      await openHistory(page)
      await expect(historyRow(page, 3)).toContainText("Restored from v1")

      // Restart the server, then look again with a fresh sign-in.
      const restart = await restartServer(prd.origin, `PRD B.project.restore-restart (${ws.slug}): restore must survive a restart`)
      const ownerState = await loginStorageState(prd.origin, prd.requireAccount("owner"))
      const api = await apiAs(prd.origin, ownerState)
      try {
        const build = await readAppBuild(api)
        expect(build.sha, "the same build is running after the restart").toBe(prd.appSha)
        const after = shape(await snapshot(api, ws.id, slug))
        expect(after, "the history is identical after the restart").toEqual(before)
        expect(heroOf(await snapshot(api, ws.id, slug, 2)), "v2 is still there, unchanged").toBe(V2)
        expect(heroOf(await snapshot(api, ws.id, slug, 1)), "v1 is still there, unchanged").toBe(V1)

        const ctx = await prd.browserContext(ownerState)
        await pinWorkspace(ctx, ws.id)
        const fresh = await ctx.newPage()
        await openProject(fresh, ws.id, slug)
        await expect(preview(fresh).locator(HERO)).toHaveText(V1)
        await openHistory(fresh)
        await expect(historyRow(fresh, 3)).toContainText("Restored from v1")
        await expect(historyRow(fresh, 2)).toBeVisible()
        await expect(historyRow(fresh, 1)).toBeVisible()
        await fresh.screenshot({ path: test.info().outputPath("after-restart.png"), fullPage: true })
        await prd.evidence("result.json", { mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, restart, before, after, buildAfterRestart: build.sha })
      } finally {
        await api.dispose()
      }
    },
  )
})
