// PRD §0.1 "aggregate quota" (#4647, #4654): when a workspace's artifact
// storage is full, a further write is refused with 413 artifact_storage_quota,
// everything already stored is kept and the editor tells the person in Czech
// that the capacity does not allow another save.
//
// The limits are instance-wide, and the product CLI is the only way to change
// them (`crewship admin instance artifact-storage set`). The scenario puts a
// QA workspace just under the smallest limit the product accepts (1 MiB) while
// the limit is still the normal one, lowers the workspace limit for the
// shortest time the checks need, and restores the ORIGINAL values in a finally
// block (verified, and recorded in the row's evidence). While it is lowered,
// other workspaces larger than 1 MiB cannot grow; nothing is deleted anywhere.
import { expect, prdRequirement, test } from "./fixtures"
import { AdminCli, type InstanceStorage } from "./ops"
import { acquire, createNative, NATIVE_APPS, nativeAvailability, openEditor, readNative, typeMarker } from "./quota/native"
import { createProject, ensureArtifactProjects, head, saveVersion } from "./quota/project"
import { ownerLogin, SharedWorkspace } from "./quota/workspace"

const MiB = 1 << 20
const LOWERED_WORKSPACE_LIMIT = MiB // the smallest limit the product accepts
const HEADROOM = 24 * 1024 // less than one native save (an empty Word document is 55 KB)
const MESSAGE = "Kapacita úložiště neumožňuje další uložení"

interface WorkspaceUsage {
  used_bytes: number
  reserved_bytes: number
  limit_bytes: number
  level: string
}

test.describe("aggregate artifact storage", () => {
  const shared = new SharedWorkspace()
  test.afterAll(async () => {
    await shared.close()
  })

  test(
    "a write over the workspace limit is refused, the work is kept and the editor says so",
    { annotation: prdRequirement("quota.aggregate.workspace", "Workspace storage limit reached: 413 artifact_storage_quota, existing data kept, the editor says the capacity does not allow another save") },
    async ({ prd }) => {
      prd.requireFull("needs the instance admin CLI against a deployed origin")
      test.setTimeout(Math.max(test.info().timeout, 15 * 60_000))
      const cliBin = prd.requireCli()
      const owner = prd.requireAccount("owner")
      const { api, workspace: ws } = await shared.open("quota-aggregate")
      const avail = await nativeAvailability(api, ws.id)
      test.skip(!avail.wordcraft, `origin offers no native Word editor (documents/status ${avail.status})`)
      await ensureArtifactProjects(api, ws.id)

      const cli = new AdminCli(cliBin, prd.origin)
      const ev: Record<string, unknown> = { mode: prd.mode, appSha: prd.appSha, workspace: ws.id }
      let original: InstanceStorage | null = null
      let lowered = false
      let restoredAt: string | null = null
      try {
        cli.login(owner.email, owner.password)
        original = cli.json<InstanceStorage>(["admin", "instance", "artifact-storage", "get"])
        ev.original = { workspaceLimitBytes: original.workspace_limit_bytes, instanceLimitBytes: original.instance_limit_bytes }

        // 1. At the normal limit: a Word document and a Project that together leave less room than one more save.
        const doc = await createNative(api, ws.id, "wordcraft")
        const project = await createProject(api, ws.id, shared.keep)
        const usage = async () => {
          const res = await api.get(`/api/v1/pages/artifacts/storage?workspace_id=${encodeURIComponent(ws.id)}`)
          expect(res.status(), "workspace storage usage").toBe(200)
          return (await res.json()) as WorkspaceUsage
        }
        let u = await usage()
        for (let i = 0; i < 4 && LOWERED_WORKSPACE_LIMIT - u.used_bytes - u.reserved_bytes > 2 * HEADROOM; i++) {
          const room = LOWERED_WORKSPACE_LIMIT - u.used_bytes - u.reserved_bytes
          const r = await saveVersion(api, ws.id, project, `fill${i}`, Math.max(1, room - HEADROOM - 8_000))
          expect(r.status, `fill save: ${r.error}`).toBe(200)
          u = await usage()
        }
        const room = LOWERED_WORKSPACE_LIMIT - u.used_bytes - u.reserved_bytes
        expect(room, "the QA workspace fits under the lowered limit with less room than one save").toBeGreaterThan(0)
        expect(room, "less room than one native save").toBeLessThan(40 * 1024)
        const projectBefore = await head(api, ws.id, project.pageId)
        const docBefore = await readNative(api, ws.id, doc.pageId)
        ev.workspaceBefore = { usedBytes: u.used_bytes, reservedBytes: u.reserved_bytes, limitBytes: u.limit_bytes, roomUnderLoweredLimit: room }

        const page = await (await prd.browserContext(await ownerLogin())).newPage()
        const session = await (await page.context().request.get("/api/auth/session")).json().catch(() => ({}))
        const uid = (session as { user?: { id?: string } }).user?.id
        await page.addInitScript(({ uid, ws }) => {
          try {
            if (uid) localStorage.setItem(`crewship.workspaceId:${uid}`, ws)
          } catch {
            /* the URL carries workspace_id */
          }
        }, { uid, ws: ws.id })
        page.on("dialog", (d) => void d.accept().catch(() => {}))
        await openEditor(page, doc.pageId, ws.id)
        const frame = await acquire(page, "wordcraft", NATIVE_APPS[0].frameTitle)

        // 2. Lower the workspace limit with the product CLI, check, restore.
        const set = cli.json<InstanceStorage>(["admin", "instance", "artifact-storage", "set", "--workspace-limit", String(LOWERED_WORKSPACE_LIMIT)])
        lowered = true
        const loweredAt = new Date().toISOString()
        ev.lowered = { at: loweredAt, workspaceLimitBytes: set.workspace_limit_bytes, instanceLimitBytes: set.instance_limit_bytes }
        expect(set.workspace_limit_bytes).toBe(LOWERED_WORKSPACE_LIMIT)
        expect(set.instance_limit_bytes, "the instance limit was not touched").toBe(original.instance_limit_bytes)

        // API: a Project save over the limit.
        const over = await saveVersion(api, ws.id, project, "over-limit", 96 * 1024)
        ev.apiWrite = { status: over.status, code: over.code, error: over.error }
        // The editor: type and let it save.
        const marker = `AGG-QUOTA-${Date.now().toString(36)}`
        await typeMarker(page, frame, "wordcraft", marker)
        const deadline = Date.now() + 60_000
        let shown = false
        while (Date.now() < deadline && !shown) {
          shown = (await page.getByText(MESSAGE).count()) > 0
          if (!shown) await page.waitForTimeout(500)
        }
        const alertText = (await page.getByRole("alert").allInnerTexts()).map((s) => s.trim()).filter(Boolean)
        await page.screenshot({ path: test.info().outputPath("editor-storage-full.png"), fullPage: true })
        // Existing data, read while the limit is still low.
        const projectAfter = await head(api, ws.id, project.pageId)
        const docAfter = await readNative(api, ws.id, doc.pageId)
        const u2 = await usage()
        ev.editor = { messageShown: shown, alerts: alertText }
        ev.kept = {
          projectVersions: { before: projectBefore.versions, after: projectAfter.versions },
          projectUsed: { before: projectBefore.used, after: projectAfter.used },
          wordVersion: { before: docBefore.version, after: docAfter.version },
          workspaceUsedBytes: { before: u.used_bytes, after: u2.used_bytes },
        }

        const back = cli.json<InstanceStorage>(["admin", "instance", "artifact-storage", "set", "--workspace-limit", String(original.workspace_limit_bytes)])
        lowered = false
        restoredAt = new Date().toISOString()
        ev.restored = { at: restoredAt, workspaceLimitBytes: back.workspace_limit_bytes, instanceLimitBytes: back.instance_limit_bytes, loweredForMs: Date.parse(restoredAt) - Date.parse(loweredAt) }
        expect(back.workspace_limit_bytes).toBe(original.workspace_limit_bytes)
        expect(back.instance_limit_bytes).toBe(original.instance_limit_bytes)

        expect(over.status, "a Project save over the workspace limit is refused").toBe(413)
        expect(over.code).toBe("artifact_storage_quota")
        expect.soft(shown, `the editor shows "${MESSAGE}"`).toBe(true)
        expect.soft(projectAfter.versions, "every Project version is kept").toEqual(projectBefore.versions)
        expect.soft(projectAfter.used, "the refused save spent no bytes").toBe(projectBefore.used)
        expect.soft(docAfter.version, "the refused editor save did not create a Word version").toBe(docBefore.version)
        expect.soft(u2.used_bytes, "workspace usage is unchanged").toBe(u.used_bytes)
        // And once the limit is back, the same write works.
        const again = await saveVersion(api, ws.id, project, "after-restore", 96 * 1024)
        ev.afterRestore = { status: again.status }
        expect.soft(again.status, "the write succeeds again after the limit is restored").toBe(200)
      } finally {
        if (lowered && original) {
          try {
            const back = cli.json<InstanceStorage>(["admin", "instance", "artifact-storage", "set", "--workspace-limit", String(original.workspace_limit_bytes)])
            ev.restoredInFinally = { workspaceLimitBytes: back.workspace_limit_bytes, instanceLimitBytes: back.instance_limit_bytes }
          } catch (err) {
            ev.restoreFailed = String(err)
            console.error(`[prd] FAILED to restore the artifact workspace limit to ${original.workspace_limit_bytes}: ${String(err)}`)
          }
        }
        if (original) {
          try {
            const now = cli.json<InstanceStorage>(["admin", "instance", "artifact-storage", "get"])
            ev.finalLimits = { workspaceLimitBytes: now.workspace_limit_bytes, instanceLimitBytes: now.instance_limit_bytes }
          } catch {
            /* recorded above */
          }
        }
        await prd.evidence("result.json", ev)
        cli.dispose()
      }
      const final = ev.finalLimits as { workspaceLimitBytes?: number } | undefined
      expect(final?.workspaceLimitBytes, "the original workspace limit is back").toBe(original?.workspace_limit_bytes)
    },
  )
})
