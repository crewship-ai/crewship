// PRD §7 "full quota": when a document's history is full, the person gets a
// clear choice and keeps their work. Nothing is lost, the message does not
// promise more space (or a copy) the product cannot provide, and Word/Sheet
// surface their history budget.
//
// Project: the scenario fills a fresh Project's history to the byte through
// the API (256 MiB in sub-16 MiB versions), then checks the refused save over
// the API and in the Project page. Runs in quick and full mode.
//
// Word/Sheet: the native history budget is 16 GiB, which a test cannot fill.
// Full mode checks that the editor shows the real budget and, with the save
// endpoint answering the server's own 413, that the editor keeps the draft
// and says the history is full. Skipped where the origin has no native editor.
import type { APIRequestContext, Page } from "@playwright/test"
import * as fs from "node:fs"
import { expect, prdRequirement, test } from "./fixtures"
import { acquire, createNative, NATIVE_APPS, nativeAvailability, openEditor, readNative, refuseSavesAsFull, statusText, typeMarker } from "./quota/native"
import {
  alerts,
  announcedCopyActions,
  createProject,
  ensureArtifactProjects,
  fillHistory,
  FULL_MARK,
  head,
  openProject,
  promisesMoreSpace,
  readArtifact,
  saveFromUi,
  saveVersion,
  sourceBox,
  type Head,
  type ProjectRef,
} from "./quota/project"
import { ownerLogin, SharedWorkspace } from "./quota/workspace"

const R = {
  refused: prdRequirement("quota.project.refused", "Full Project history: a further save is refused (413 artifact_history_quota) and changes nothing"),
  kept: prdRequirement("quota.project.work-kept", "Full Project history: the unsaved source, the preview and every existing version are kept; the draft is offered again after reload"),
  message: prdRequirement("quota.project.message", "Full Project history: the message says the history is full and that work is kept, without promising more space or a copy the page does not offer"),
  nativeVisible: prdRequirement("quota.native.visible", "Word/Sheet: the editor shows the document's history budget (used / limit, all versions retained)"),
  nativeFull: prdRequirement("quota.native.full", "Word/Sheet: a save refused by a full history keeps the draft in the editor and says the history is full, without promising more space"),
}

const FILL_TIMEOUT = 15 * 60_000

async function pinWorkspace(page: Page, api: APIRequestContext, workspaceId: string) {
  const s = await api.get("/api/auth/session")
  const uid = ((await s.json().catch(() => ({}))) as { user?: { id?: string } }).user?.id
  if (!uid) return
  await page.addInitScript(({ uid, ws }) => {
    try {
      localStorage.setItem(`crewship.workspaceId:${uid}`, ws)
    } catch {
      /* URLs carry workspace_id anyway */
    }
  }, { uid, ws: workspaceId })
}

test.describe("full Project history", () => {
  const shared = new SharedWorkspace()
  let api: APIRequestContext
  let wsId = ""
  let availability = ""
  let project: ProjectRef | null = null
  let full: Head | null = null

  test.beforeAll(async () => {
    const opened = await shared.open("quota")
    api = opened.api
    wsId = opened.workspace.id
    availability = await ensureArtifactProjects(api, wsId)
  })
  test.afterAll(async () => {
    await shared.close()
  })

  /**
   * The filled Project of this group. The first test fills it; a later test
   * fills its own when the state is gone (Playwright restarts the worker
   * after a failed test, which resets module state and this group's workspace).
   */
  const requireFullProject = async () => {
    if (!project || !full) {
      test.setTimeout(Math.max(test.info().timeout, FILL_TIMEOUT))
      const p = await createProject(api, wsId)
      const fill = await fillHistory(api, wsId, p)
      expect(fill.remaining, "the history is full to within a few hundred bytes").toBeLessThan(1024)
      project = p
      full = fill.final
    }
    return { p: project, h: full }
  }

  test("a further save is refused and changes nothing", { annotation: R.refused }, async ({ prd }) => {
    test.setTimeout(Math.max(test.info().timeout, FILL_TIMEOUT))
    const p = await createProject(api, wsId)
    const fill = await fillHistory(api, wsId, p)
    await prd.evidence("project-fill.json", {
      availability,
      pageId: p.pageId,
      limitBytes: fill.final.limit,
      usedBytes: fill.final.used,
      remainingBytes: fill.remaining,
      versions: fill.final.versions.length,
      fillMs: fill.fillMs,
      perVersionOverhead: fill.overhead,
      saves: fill.log,
    })
    expect(fill.remaining, "the history is full to within a few hundred bytes").toBeLessThan(1024)

    const over = await saveVersion(api, wsId, p, "over", 4096)
    const result = {
      status: over.status,
      code: over.code,
      error: over.error,
      before: { latest: over.before.latest, used: over.before.used, versions: over.before.versions.length },
      after: { latest: over.after.latest, used: over.after.used, versions: over.after.versions.length },
    }
    await prd.evidence("project-over-limit-api.json", result)
    expect(over.status, "a save over the limit is refused").toBe(413)
    expect(over.code).toBe("artifact_history_quota")
    expect(over.after.latest, "no version was added").toBe(over.before.latest)
    expect(over.after.versions, "the version list is unchanged").toEqual(over.before.versions)
    expect(over.after.used, "no bytes were spent").toBe(over.before.used)
    project = p
    full = over.after
  })

  test("the work is kept: draft, preview and every version", { annotation: R.kept }, async ({ prd }) => {
    const { p, h } = await requireFullProject()
    const page = await (await prd.browserContext(await ownerLogin())).newPage()
    await pinWorkspace(page, api, wsId)
    await openProject(page, p.slug, wsId)
    const marker = `UI-AFTER-FULL-${Date.now().toString(36)}`
    const save = await saveFromUi(page, marker)
    await page.waitForTimeout(2500)
    const src = sourceBox(page)
    const ev: Record<string, unknown> = { pageId: p.pageId, uiSave: { status: save.status, code: save.code }, alerts: await alerts(page) }
    ev.sourceKept = (await src.count()) === 1 && (await src.inputValue()) === save.text
    ev.previewVisible = await page.getByTitle("Artifact preview").isVisible()
    ev.previewHeading = await page
      .frameLocator('iframe[title="Artifact preview"]')
      .locator("#qa-head")
      .innerText({ timeout: 10_000 })
      .catch(() => null)
    ev.localDraftKept = await page.evaluate((m) => Object.keys(localStorage).some((k) => k.startsWith("artifact-source:") && (localStorage.getItem(k) ?? "").includes(m)), marker)
    await page.screenshot({ path: test.info().outputPath("project-after-413.png"), fullPage: true })

    const after = await head(api, wsId, p.pageId)
    ev.history = { before: { latest: h.latest, used: h.used, versions: h.versions.length }, after: { latest: after.latest, used: after.used, versions: after.versions.length } }
    const probe = [...new Set([h.versions[h.versions.length - 1], h.versions[Math.floor(h.versions.length / 2)], h.versions[0]])]
    const readback: Record<number, { status: number; version: number | null; files: number }> = {}
    for (const v of probe) {
      const r = await readArtifact(api, wsId, p.pageId, v)
      readback[v] = { status: r.status, version: r.body?.manifest?.version ?? null, files: r.body?.manifest?.files?.length ?? 0 }
    }
    ev.readback = readback

    await page.reload({ waitUntil: "domcontentloaded" })
    await expect(page.getByTitle("Artifact preview")).toBeVisible({ timeout: 60_000 })
    await page.waitForTimeout(2500)
    const restore = page.getByRole("button", { name: "Restore source draft", exact: true })
    const offered = await restore.count()
    let restoredIdentical: boolean | null = null
    if (offered === 1) {
      await restore.click()
      restoredIdentical = (await sourceBox(page).inputValue()) === save.text
    }
    ev.afterReload = { restoreOffered: offered, restoredIdentical }
    await page.screenshot({ path: test.info().outputPath("project-after-reload.png"), fullPage: true })
    await prd.evidence("project-work-kept.json", ev)

    expect(save.status, "the page's save is refused by the full history").toBe(413)
    expect(save.code).toBe("artifact_history_quota")
    expect.soft(ev.sourceKept, "the typed source stays in the editor").toBe(true)
    expect.soft(ev.previewVisible, "the preview stays visible").toBe(true)
    expect.soft(ev.previewHeading ?? "", "the preview still shows the latest saved version").toContain(FULL_MARK)
    expect.soft(after.versions, "every existing version is kept").toEqual(h.versions)
    expect.soft(after.used, "nothing was written").toBe(h.used)
    for (const v of probe) {
      expect.soft(readback[v].status, `version ${v} is still readable`).toBe(200)
      expect.soft(readback[v].version, `version ${v} reads as itself`).toBe(v)
    }
    expect.soft(offered, "after reload the unsaved draft is offered again").toBe(1)
    expect.soft(restoredIdentical, "the restored draft is what was typed").toBe(true)
  })

  test("the message gives a clear choice without promising more space", { annotation: R.message }, async ({ prd }) => {
    const { p } = await requireFullProject()
    const page = await (await prd.browserContext(await ownerLogin())).newPage()
    await pinWorkspace(page, api, wsId)
    await openProject(page, p.slug, wsId)
    const storageLine = (await page.getByRole("status").allInnerTexts()).map((s) => s.trim()).find((s) => s.startsWith("Storage")) ?? null
    const save = await saveFromUi(page, `MSG-${Date.now().toString(36)}`)
    await page.waitForTimeout(2000)
    const all = await alerts(page)
    const message = all.find((a) => /quota|history|full|storage/i.test(a)) ?? ""
    const promise = promisesMoreSpace(message)
    const copies = await announcedCopyActions(page, message)
    await page.screenshot({ path: test.info().outputPath("project-quota-message.png"), fullPage: true })
    await prd.evidence("project-quota-message.json", { uiSave: { status: save.status, code: save.code }, alerts: all, message, storageLine, promisesMoreSpace: promise, announcedCopyActions: copies })

    expect(save.status).toBe(413)
    expect(message, "a message explains the refused save").not.toBe("")
    expect.soft(message, "it says the history is full").toMatch(/\bfull\b/i)
    expect.soft(message, "it says existing versions are kept").toMatch(/(version|history)[^.]*\b(retain|kept|keep|remain|safe)|\b(retain|kept)\b[^.]*version/i)
    expect.soft(message, "it says what happens to the unsaved work").toMatch(/draft|source|work|changes/i)
    expect.soft(promise, `no promise of more space ("${promise}")`).toBeNull()
    for (const c of copies) expect.soft(c.control, `the message announces "${c.phrase}" but the page offers no such control`).toBe(true)
  })
})

test.describe("Word and Sheet history quota", () => {
  // Own workspace and login (opened on first use, so quick mode creates
  // nothing): the fixture's owner session can be rotated by an earlier
  // browser context, after which its requests and cleanup answer 401.
  const shared = new SharedWorkspace()
  const nativeWorkspace = async () => (shared.api && shared.workspace ? { api: shared.api, workspace: shared.workspace } : shared.open("quota-native"))
  test.afterAll(async () => {
    await shared.close()
  })

  test("the editors show their history budget", { annotation: R.nativeVisible }, async ({ prd }) => {
    prd.requireFull("native Word/Sheet editors need a deployed origin")
    const { api: request, workspace: ws } = await nativeWorkspace()
    const avail = await nativeAvailability(request, ws.id)
    const apps = NATIVE_APPS.filter((a) => avail[a.app])
    test.skip(apps.length === 0, `origin offers no native Word/Sheet editor (documents/status ${avail.status})`)
    const page = await (await prd.browserContext(await ownerLogin())).newPage()
    await pinWorkspace(page, request, ws.id)
    const ev: Record<string, unknown> = { availability: avail }
    for (const a of apps) {
      const doc = await createNative(request, ws.id, a.app)
      const read = await readNative(request, ws.id, doc.pageId)
      await openEditor(page, doc.pageId, ws.id)
      await page.waitForTimeout(1500)
      const status = await statusText(page)
      ev[a.label] = { pageId: doc.pageId, version: read.version, quota: read.quota, status }
      await page.screenshot({ path: test.info().outputPath(`${a.app}-budget.png`), fullPage: true })
      expect.soft(read.quota, `${a.label}: the document reports its history quota`).not.toBeNull()
      if (!read.quota) continue
      expect.soft(BigInt(read.quota.limitBytes) > 0n, `${a.label}: a positive history limit`).toBe(true)
      expect.soft(BigInt(read.quota.usedBytes) > 0n, `${a.label}: version 1 counts against the budget`).toBe(true)
      expect.soft(read.quota.retention, `${a.label}: every version is retained`).toBe("keep-all")
      expect.soft(status, `${a.label}: the editor shows used / limit`).toContain(`Historie ${read.quota.usedBytes} / ${read.quota.limitBytes} B`)
    }
    await prd.evidence("native-budget.json", ev)
  })

  test("a full history keeps the Word/Sheet draft and says so", { annotation: R.nativeFull }, async ({ prd }) => {
    prd.requireFull("native Word/Sheet editors need a deployed origin")
    test.setTimeout(Math.max(test.info().timeout, 10 * 60_000))
    const { api: request, workspace: ws } = await nativeWorkspace()
    const avail = await nativeAvailability(request, ws.id)
    const apps = NATIVE_APPS.filter((a) => avail[a.app])
    test.skip(apps.length === 0, `origin offers no native Word/Sheet editor (documents/status ${avail.status})`)
    const page = await (await prd.browserContext(await ownerLogin())).newPage()
    await pinWorkspace(page, request, ws.id)
    // The editor guards unsaved work on leave; this test leaves on purpose.
    page.on("dialog", (d) => void d.accept().catch(() => {}))
    const ev: Record<string, unknown> = { availability: avail, note: "save answered with the server's 413 body by route interception; the 16 GiB budget is not filled" }
    const checks: Array<() => void> = []
    for (const a of apps) {
      const doc = await createNative(request, ws.id, a.app)
      await openEditor(page, doc.pageId, ws.id)
      const f = await acquire(page, a.app, a.frameTitle)
      const refused = await refuseSavesAsFull(page)
      const marker = `QUOTA-${a.label.toUpperCase()}-${Date.now().toString(36)}`
      await typeMarker(page, f, a.app, marker)
      const deadline = Date.now() + 60_000
      let status = await statusText(page)
      while (Date.now() < deadline && !(refused.saves > 0 && /Neuložený koncept|Koncept zůstává|koncept zachován/i.test(status))) {
        await page.waitForTimeout(500)
        status = await statusText(page)
      }
      await page.waitForTimeout(1000)
      status = await statusText(page)
      const all = await alerts(page)
      const message = all.join(" | ")
      const promise = promisesMoreSpace(message)
      const copies = await announcedCopyActions(page, message)
      let download: { offered: boolean; bytes: number | null } = { offered: false, bytes: null }
      const button = page.getByRole("button", { name: /Stáhnout koncept|Download/i })
      if ((await button.count()) > 0) {
        const pending = page.waitForEvent("download", { timeout: 30_000 })
        await button.first().click()
        const d = await pending.catch(() => null)
        const file = d ? await d.path().catch(() => null) : null
        download = { offered: true, bytes: file ? fs.statSync(file).size : null }
      }
      await page.screenshot({ path: test.info().outputPath(`${a.app}-full.png`), fullPage: true })
      await page.unroute(/\/craft\/document\/(save|reconcile)\?/)
      const server = await readNative(request, ws.id, doc.pageId)
      ev[a.label] = { pageId: doc.pageId, refusedSaves: refused.saves, status, alerts: all, promisesMoreSpace: promise, announcedCopyActions: copies, download, serverVersion: { before: doc.version, after: server.version } }
      checks.push(() => {
        expect.soft(refused.saves, `${a.label}: the editor tried to save`).toBeGreaterThan(0)
        expect.soft(status, `${a.label}: the editor keeps the unsaved draft`).toMatch(/Neuložený koncept|Koncept zůstává|koncept zachován/i)
        expect.soft(server.version, `${a.label}: nothing was saved`).toBe(doc.version)
        expect.soft(message, `${a.label}: the editor says the document history is full`).toMatch(/histori\w*[^.|]{0,40}pln|history[^.|]{0,20}full|kvót|quota/i)
        expect.soft(promise, `${a.label}: no promise of more space ("${promise}")`).toBeNull()
        for (const c of copies) expect.soft(c.control, `${a.label}: the message announces "${c.phrase}" but offers no such control`).toBe(true)
        if (download.offered) expect.soft(download.bytes ?? 0, `${a.label}: the offered draft download has content`).toBeGreaterThan(0)
      })
      // Leave without the leave guard: the draft is meant to stay unsaved.
      await page.goto("about:blank")
    }
    await prd.evidence("native-full.json", ev)
    for (const c of checks) c()
  })
})
