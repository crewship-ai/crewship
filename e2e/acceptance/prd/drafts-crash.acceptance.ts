// PRD §7 "drafts after a crash": the browser is killed (SIGKILL of the whole
// profile, no unload) before the work is saved or sent. When the user starts
// the browser again the draft is offered back — Project source with Restore,
// unsent comments in the viewer and on the Project page, native Word/Sheet
// drafts with a comparison — nothing is saved or posted automatically, and
// another person sees none of it.
//
// Quick mode covers the Project and comment drafts against the local test
// server. The Word/Sheet crash needs the native editors of a deployed origin
// and runs only in full mode (skipped where the origin offers no native editor).
import { apiAs, loginStorageState } from "./auth"
import {
  createProject,
  CrashableProfile,
  ensureArtifactProjects,
  grantPage,
  openProject,
  openViewer,
  artifactDraftStore,
  pause,
  prepareSourceDraft,
  prepareViewerComment,
  projectCommentBox,
  projectOffer,
  readArtifact,
  settleLocalStorageCommit,
  shareProject,
  typeSourceDraft,
  typeViewerComment,
  viewerCommentValue,
  type Traffic,
} from "./drafts-crash/crash"
import * as native from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"

// Chromium writes localStorage to disk lazily (about 5 s) and rate-limits
// commits per origin, so a draft can stay memory-only for up to a minute.
// The first test crashes once the drafts are on disk (and records how long
// that took): it checks recovery, privacy and "nothing saved". The second
// crashes 300 ms after typing, inside a fresh commit window, so the result
// depends only on where the product keeps its drafts: it checks that drafts
// are durable at all (a known gap while they live only in localStorage).
const PERSIST_TIMEOUT_MS = 150_000
const IMMEDIATE_CRASH_MS = 300
// The burst plus the crash must land inside Chromium's 5 s commit delay.
const IMMEDIATE_WINDOW_MS = 3_500

const R = {
  source: prdRequirement("drafts.crash.source", "Browser killed before save: the unsaved Project source draft is offered again (not applied) and restores identically"),
  comments: prdRequirement("drafts.crash.comments", "Browser killed before sending: unsent comment drafts in the viewer and on the Project page are restored"),
  nothingSaved: prdRequirement("drafts.crash.nothing-saved", "After a crash nothing is saved or posted automatically"),
  private: prdRequirement("drafts.crash.private", "Another user sees none of the crashed drafts"),
  immediate: prdRequirement("drafts.crash.immediate", "Browser killed right after typing: the Project source and viewer comment drafts are still restored"),
  nativeOffer: prdRequirement("drafts.crash.native-offer", "Word/Sheet: after a crash before the save the unsent draft is offered with a comparison"),
  nativeNothingSaved: prdRequirement("drafts.crash.native-nothing-saved", "Word/Sheet: after a crash nothing is saved automatically"),
  nativePrivate: prdRequirement("drafts.crash.native-private", "Word/Sheet: another user sees no crashed draft"),
}

/** Mutations of the Project or its comments; a crash recovery must not send any. */
const projectWrites = (traffic: Traffic, since: number) =>
  traffic.writes.slice(since).filter((w) => /\/artifact(\/|$)|\/comments(\/|$)/.test(w.path))

async function ownerIdentity(request: import("@playwright/test").APIRequestContext) {
  const res = await request.get("/api/auth/session")
  expect(res.status()).toBe(200)
  const id = ((await res.json()) as { user?: { id?: string } }).user?.id
  expect(id, "owner session has a user id").toBeTruthy()
  return id!
}

test.describe("drafts after a crash", () => {
  test(
    "Project source and comment drafts survive a hard browser crash",
    { annotation: [R.source, R.comments, R.nothingSaved, R.private] },
    async ({ prd, request, context, playwright }) => {
      // Waiting for the browser to persist the drafts can take minutes.
      test.setTimeout(Math.max(test.info().timeout, 8 * 60_000))
      const ws = await prd.qaWorkspace()
      const availability = await ensureArtifactProjects(request, ws.id, !prd.env.workspaceId)
      const project = await createProject(request, ws.id, "idle")
      // The other person gets real access to the Project, so "sees nothing"
      // means the drafts are private, not that the page is hidden.
      const other = await prd.provision("MEMBER", "PRD drafts other person")
      const shareStatus = await shareProject(request, ws.id, project.pageId, 1)
      expect(shareStatus, "share version 1 with the workspace").toBeLessThan(300)
      const grant = await grantPage(request, ws.id, project.pageId, other.userId, "write")
      expect(grant, "grant the other person write access").toBeLessThan(300)

      const tag = Date.now().toString(36)
      const MARK = `UNSENT-SOURCE-${tag}`
      const viewerText = `Unsent viewer comment ${tag}`
      const projectText = `Unsent project comment ${tag}`

      const profile = new CrashableProfile(playwright.chromium, prd.origin, await context.storageState(), {
        userId: await ownerIdentity(request),
        workspaceId: ws.id,
      })
      const evidence: Record<string, unknown> = { availability, pageId: project.pageId, shareStatus, grantStatus: grant }
      const cleanup: Array<() => Promise<unknown>> = []
      try {
        // Before the crash: a source draft and an unsent comment on the Project
        // page, and an unsent comment in the /artifacts viewer (second tab).
        const a = await profile.page()
        await openProject(a, project.slug, ws.id)
        const sourceText = await typeSourceDraft(a, MARK)
        await expect(projectCommentBox(a)).toBeVisible()
        await projectCommentBox(a).fill(projectText)
        const v = await profile.context!.newPage()
        await openViewer(v, project.slug, ws.id)
        await typeViewerComment(v, viewerText)
        evidence.persistedAfterMs = await profile.waitPersisted([MARK, projectText, viewerText], PERSIST_TIMEOUT_MS)
        await pause(1000)

        const writesBeforeCrash = profile.traffic.writes.length
        evidence.killedProcesses = await profile.crash()
        expect(evidence.killedProcesses as number, "the crash killed the browser").toBeGreaterThan(0)
        const afterCrash = await readArtifact(request, ws.id, project.pageId)
        evidence.serverAfterCrash = { latest_version: afterCrash.latest_version, hasMark: JSON.stringify(afterCrash.manifest).includes(MARK), comments: afterCrash.comments.length }

        // Restart the browser on the same profile and come back.
        const b = await profile.reopen()
        await openProject(b, project.slug, ws.id)
        await pause(2500)
        const offer = await projectOffer(b)
        evidence.projectOffer = offer
        await b.screenshot({ path: test.info().outputPath("project-reopened.png"), fullPage: true })
        expect.soft(offer.restoreOffered, "Restore source draft is offered").toBe(1)
        expect.soft(offer.sourceOpenBeforeRestore, "the draft is offered, not silently applied").toBe(0)
        expect.soft(offer.commentBox, "the unsent Project-page comment is restored").toBe(projectText)
        if (offer.restoreOffered) {
          await b.getByRole("button", { name: "Restore source draft", exact: true }).click()
          const restored = await b.getByRole("textbox", { name: "Source file", exact: true }).inputValue()
          evidence.sourceRestoredIdentical = restored === sourceText
          expect.soft(restored, "the restored source is identical to what was typed").toBe(sourceText)
        }
        const previewText = await b.getByTitle("Artifact preview").contentFrame().locator("body").innerText().catch(() => "")
        evidence.previewHasMark = previewText.includes(MARK)

        const vb = await profile.context!.newPage()
        await openViewer(vb, project.slug, ws.id)
        await pause(1500)
        const viewerRestored = await viewerCommentValue(vb)
        evidence.viewerComment = { restored: viewerRestored === viewerText, formShown: viewerRestored !== null }
        await vb.screenshot({ path: test.info().outputPath("viewer-reopened.png"), fullPage: true })
        expect.soft(viewerRestored, "the unsent viewer comment is restored").toBe(viewerText)

        // Nothing reached the server, before or after the crash.
        await pause(3000)
        const after = await readArtifact(request, ws.id, project.pageId)
        const writes = projectWrites(profile.traffic, 0)
        evidence.serverAfterReopen = { latest_version: after.latest_version, hasMark: JSON.stringify(after.manifest).includes(MARK), comments: after.comments.length }
        evidence.projectWrites = writes
        evidence.writesAfterCrash = profile.traffic.writes.slice(writesBeforeCrash)
        expect.soft(after.latest_version, "the Project stays at version 1").toBe(1)
        expect.soft(JSON.stringify(after.manifest).includes(MARK), "the draft never reached the server").toBe(false)
        expect.soft(after.comments.length, "no comment was posted").toBe(0)
        expect.soft(writes, "no save or comment request was sent").toEqual([])
        expect.soft(evidence.previewHasMark, "the preview still shows the saved version").toBe(false)

        // Another person with write access signs in on the same browser
        // profile (a shared computer) and opens the same Project: the crashed
        // drafts stay on disk but are offered to nobody else.
        const otherState = await loginStorageState(prd.origin, other.account)
        const otherApi = await apiAs(prd.origin, otherState)
        cleanup.push(() => otherApi.dispose())
        const o = await profile.switchUser(otherState)
        await openProject(o, project.slug, ws.id)
        await pause(2000)
        const otherOffer = await projectOffer(o)
        const otherBody = await o.locator("body").innerText()
        const otherSource = await o.getByRole("textbox", { name: "Source file", exact: true }).count()
        await o.screenshot({ path: test.info().outputPath("other-person-project.png"), fullPage: true })
        await openViewer(o, project.slug, ws.id)
        await pause(1000)
        const otherViewer = await viewerCommentValue(o)
        const otherArtifact = await readArtifact(otherApi, ws.id, project.pageId)
        evidence.otherUser = {
          projectOffer: otherOffer,
          sourceEditorOpen: otherSource,
          pageMentionsDraft: [MARK, viewerText, projectText].some((t) => otherBody.includes(t)),
          viewerComment: otherViewer,
          canWrite: otherArtifact.can_write ?? null,
        }
        expect.soft(otherArtifact.can_write, "the other person can write the Project (the check is meaningful)").toBe(true)
        expect.soft(otherOffer.restoreOffered, "the other person is not offered the draft").toBe(0)
        expect.soft(otherSource, "no source draft opens for the other person").toBe(0)
        expect.soft(otherOffer.commentBox ?? "", "the other person's comment box is empty").toBe("")
        expect.soft((evidence.otherUser as { pageMentionsDraft: boolean }).pageMentionsDraft, "the other person sees no draft text").toBe(false)
        expect.soft(otherViewer ?? "", "the other person's viewer comment is empty").toBe("")
      } finally {
        await prd.evidence("drafts-crash.json", evidence)
        for (const c of cleanup) await c().catch(() => {})
        await profile.dispose()
      }
    },
  )

  test(
    "drafts survive a crash right after typing",
    { annotation: R.immediate },
    async ({ prd, request, context, playwright }) => {
      // Waiting for a localStorage commit can take up to a minute.
      test.setTimeout(Math.max(test.info().timeout, 5 * 60_000))
      const ws = await prd.qaWorkspace()
      await ensureArtifactProjects(request, ws.id, !prd.env.workspaceId)
      const project = await createProject(request, ws.id, "immediate")
      const tag = Date.now().toString(36)
      const MARK = `UNSENT-FAST-${tag}`
      const viewerText = `Unsent fast comment ${tag}`
      const profile = new CrashableProfile(playwright.chromium, prd.origin, await context.storageState(), {
        userId: await ownerIdentity(request),
        workspaceId: ws.id,
      })
      const evidence: Record<string, unknown> = { pageId: project.pageId, crashAfterTypingMs: IMMEDIATE_CRASH_MS }
      try {
        // Both tabs are loaded and ready to type before anything is typed, so
        // the two drafts go in one burst and the crash timing does not depend
        // on how fast a tab loads.
        const v = await profile.page()
        await openViewer(v, project.slug, ws.id)
        const fillViewerComment = await prepareViewerComment(v)
        const a = await profile.context!.newPage()
        await openProject(a, project.slug, ws.id)
        const fillSource = await prepareSourceDraft(a, MARK)

        // Start from a fresh localStorage commit window: whatever the pages
        // stored while loading is on disk, and Chromium's next commit is at
        // least 5 s away. The burst and the crash fit well inside it.
        evidence.commitSettledMs = await settleLocalStorageCommit(profile, a)
        const burstStart = Date.now()
        await fillViewerComment(viewerText)
        const sourceText = await fillSource()
        const store = await artifactDraftStore(a, MARK)
        await pause(IMMEDIATE_CRASH_MS)
        evidence.killedProcesses = await profile.crash()
        const crashAfterSettleMs = profile.killedAt! - burstStart
        evidence.store = store
        evidence.crashAfterSettleMs = crashAfterSettleMs
        expect(evidence.killedProcesses as number).toBeGreaterThan(0)
        // Precondition of the scenario, not of the product: the crash came
        // inside the commit window. Checked before any known-gap marking so a
        // slow harness is reported as such.
        expect(crashAfterSettleMs, "the burst and the crash fit inside Chromium's 5 s localStorage commit delay").toBeLessThan(IMMEDIATE_WINDOW_MS)
        // Known product gap: drafts kept only in localStorage are lost by a
        // crash inside the commit window (deterministic with the timing above).
        // Expected to fail until the drafts move to strict IndexedDB (#4512).
        test.fail(!store.indexedDb, `known gap: artifact drafts live only in localStorage (in localStorage: ${store.inLocalStorage}); fix in #4512`)

        const b = await profile.reopen()
        await openProject(b, project.slug, ws.id)
        await pause(2500)
        const offer = await projectOffer(b)
        let restored: string | null = null
        if (offer.restoreOffered) {
          await b.getByRole("button", { name: "Restore source draft", exact: true }).click()
          restored = await b.getByRole("textbox", { name: "Source file", exact: true }).inputValue()
        }
        const vb = await profile.context!.newPage()
        await openViewer(vb, project.slug, ws.id)
        await pause(1500)
        const viewerRestored = await viewerCommentValue(vb)
        const after = await readArtifact(request, ws.id, project.pageId)
        evidence.result = {
          sourceOffered: offer.restoreOffered,
          sourceIdentical: restored === sourceText,
          viewerCommentRestored: viewerRestored === viewerText,
          latest_version: after.latest_version,
          comments: after.comments.length,
        }
        expect.soft(offer.restoreOffered, "the source draft typed just before the crash is offered").toBe(1)
        expect.soft(restored, "and restores identically").toBe(sourceText)
        expect.soft(viewerRestored, "the viewer comment typed just before the crash is restored").toBe(viewerText)
        expect.soft(after.latest_version, "nothing was saved").toBe(1)
        expect.soft(after.comments.length, "nothing was posted").toBe(0)
      } finally {
        await prd.evidence("drafts-immediate-crash.json", evidence)
        await profile.dispose()
      }
    },
  )

  test(
    "Word and Sheet drafts survive a crash before the save",
    { annotation: [R.nativeOffer, R.nativeNothingSaved, R.nativePrivate] },
    async ({ prd, request, context, playwright }) => {
      prd.requireFull("native Word/Sheet editors run only on a deployed origin")
      const ws = await prd.qaWorkspace()
      const available = await native.nativeAvailability(request, ws.id)
      test.skip(!available.wordcraft || !available.gridcraft, `origin offers no native Word/Sheet editor (documents/status ${available.status}, word ${available.wordcraft}, sheet ${available.gridcraft})`)
      // A minute for each lease to expire after the crash, plus two editors.
      test.setTimeout(Math.max(test.info().timeout, 15 * 60_000))

      const other = await prd.provision("MEMBER", "PRD drafts other person")
      const otherState = await loginStorageState(prd.origin, other.account)
      const otherApi = await apiAs(prd.origin, otherState)
      const ownerState = await context.storageState()
      const ownerId = await ownerIdentity(request)
      const results: Record<string, unknown> = { available }
      try {
        // All access changes happen before any editor opens: a permission
        // change in the workspace retires open native editor sessions.
        const pages: Partial<Record<native.NativeApp, { pageId: string; tag: string }>> = {}
        for (const app of ["wordcraft", "gridcraft"] as const) {
          const tag = Math.random().toString(36).slice(2, 6)
          const created = await native.createNative(request, ws.id, app, tag)
          const grantStatus = await grantPage(request, ws.id, created.pageId, other.userId, "read")
          expect(grantStatus, `${app}: grant the other person read access`).toBeLessThan(300)
          pages[app] = { pageId: created.pageId, tag }
          results[app] = { pageId: created.pageId, grantStatus }
        }
        await pause(5000)
        for (const app of ["wordcraft", "gridcraft"] as const) {
          const r = results[app] as Record<string, unknown>
          const { tag } = pages[app]!
          const created = pages[app]!
          const MC1 = `MC1saved${tag}`
          const MC2 = `MC2crash${tag}`

          const profile = new CrashableProfile(playwright.chromium, prd.origin, ownerState, { userId: ownerId, workspaceId: ws.id })
          try {
            const ctx = await profile.open()
            let hold = false
            let held = 0
            // Saves after MC2 are held in the browser and never reach the
            // server: the crash happens with the edit unsaved.
            await ctx.route("**/craft/document/save**", async (route) => {
              if (!hold) return route.continue()
              held++
            })
            const a = await profile.page()
            await native.openEditor(a, created.pageId, ws.id)
            const f = await native.acquire(a, app)
            await native.typeMarker(a, f, app, MC1, 1)
            await a.keyboard.press("Control+s")
            r.savedVersion = await native.waitSaved(a)
            hold = true
            await native.typeMarker(a, f, app, MC2, 2)
            await pause(2500)
            r.heldSaves = held
            r.beforeCrash = await native.uiState(a)
            r.killedProcesses = await profile.crash()
            expect(r.killedProcesses as number).toBeGreaterThan(0)
            const headAfterCrash = await native.readNative(request, ws.id, created.pageId)
            r.headAfterCrash = { version: headAfterCrash.version, hash: headAfterCrash.hash, hasMC1: headAfterCrash.raw.includes(MC1), hasMC2: headAfterCrash.raw.includes(MC2) }

            // Restart and sign in again (as the original harness did): cookies
            // replayed from before the crash may be rotated mid-test, which
            // retires the editor session for a reason unrelated to the crash.
            // The crashed session's lease must expire before taking over.
            const owner = prd.env.accounts.owner
            expect(owner, "owner account").toBeTruthy()
            const b = await profile.reopen(await loginStorageState(prd.origin, owner!))
            const saves: string[] = []
            profile.context!.on("request", (req) => {
              if (req.method() === "POST" && new URL(req.url()).pathname.endsWith("/craft/document/save")) saves.push(new Date().toISOString())
            })
            await native.openEditor(b, created.pageId, ws.id)
            await native.frameOf(b, app)
            r.reopened = await native.uiState(b)
            const t0 = Date.now()
            await expect(native.takeButton(b)).toBeVisible({ timeout: 5 * 60_000 })
            r.takeAvailableAfterMs = Date.now() - t0
            await native.takeButton(b).click()
            await b.getByRole("status").filter({ hasText: "Připraveno k úpravám" }).first().waitFor({ timeout: 60_000 }).catch(() => {})
            await pause(3000)
            r.reacquired = await native.uiState(b)
            await b.screenshot({ path: test.info().outputPath(`${app}-draft-offered.png`) })
            const comparisons = await native.compareDrafts(b, [MC1, MC2])
            r.comparisons = comparisons
            await pause(6000)
            r.savesAfterReopen = saves.length
            const headWhileOffered = await native.readNative(request, ws.id, created.pageId)
            r.headWhileOffered = { version: headWhileOffered.version, hash: headWhileOffered.hash, hasMC2: headWhileOffered.raw.includes(MC2) }

            expect.soft(r.heldSaves as number, `${app}: the edit was unsaved when the browser died`).toBeGreaterThan(0)
            expect.soft(headAfterCrash.raw.includes(MC2), `${app}: the server never got the unsaved edit`).toBe(false)
            expect.soft(comparisons.length, `${app}: the crashed draft is offered`).toBeGreaterThan(0)
            expect.soft(comparisons.some((c) => c.draft[MC2] && !c.server[MC2]), `${app}: the comparison shows the unsaved edit only in the draft`).toBe(true)
            expect.soft(saves.length, `${app}: nothing was saved after reopening`).toBe(0)
            expect.soft(headWhileOffered.hash, `${app}: the server document is unchanged`).toBe(headAfterCrash.hash)
            expect.soft(headWhileOffered.raw.includes(MC2), `${app}: the draft was not saved automatically`).toBe(false)

            // The other person opens the same document: no draft, no unsaved edit.
            const otherCtx = await prd.browserContext(otherState)
            const o = await otherCtx.newPage()
            await native.openEditor(o, created.pageId, ws.id)
            await native.frameOf(o, app)
            await pause(2000)
            r.otherUser = await native.uiState(o)
            const otherHead = await native.readNative(otherApi, ws.id, created.pageId)
            r.otherHead = { version: otherHead.version, hash: otherHead.hash, hasMC2: otherHead.raw.includes(MC2) }
            expect.soft((r.otherUser as { recoveries: string[] }).recoveries, `${app}: the other person is offered no draft`).toEqual([])
            expect.soft((r.otherUser as { buttons: string[] }).buttons.filter((t) => /Porovnat|Kopie konceptu/.test(t)), `${app}: no draft actions for the other person`).toEqual([])
            expect.soft(otherHead.raw.includes(MC2), `${app}: the other person cannot read the unsaved edit`).toBe(false)
            await otherCtx.close()

            await native.release(b).catch((e) => (r.releaseError = String(e).slice(0, 200)))
            const fin = await native.readNative(request, ws.id, created.pageId)
            r.final = { version: fin.version, hash: fin.hash, hasMC1: fin.raw.includes(MC1), hasMC2: fin.raw.includes(MC2) }
            expect.soft(fin.raw.includes(MC2), `${app}: releasing does not save the crashed draft`).toBe(false)
          } finally {
            await profile.dispose()
          }
        }
      } finally {
        await prd.evidence("native-drafts-crash.json", results)
        await otherApi.dispose()
      }
    },
  )
})
