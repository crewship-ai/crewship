// PRD §7 A5: the server restarts while a person is editing a native Word or
// Sheet document with unsaved changes. Afterwards nothing is lost (the work is
// saved, or the editor keeps it as a local draft and offers it), no version
// appears twice, nothing is saved behind the person's back, and the editor
// connects again: after the person reloads, the document opens, the person
// takes the editor, edits and saves.
//
// Full mode only. The restart is the operator's command, PRD_RESTART_COMMAND or PRD_RESTART_HANDSHAKE
// (see craft-session/restart.ts); the row fails without it. Run these rows
// serially and only when needed: a restart interrupts everyone on the instance.
import { inspectDrafts, placeCaret, saveByAutosaveOrKey, typeLines } from "./craft-session/draft"
import { restartServer } from "./craft-session/restart"
import { expect, prdRequirement, test } from "./fixtures"
import { disposeActors, isolatedActor } from "./outage-lostack/actor"
import * as N from "./outage-lostack/native"

test.afterEach(disposeActors)

for (const app of ["wordcraft", "gridcraft"] as const) {
  const label = N.APP_LABEL[app]
  test(
    `${label}: a server restart during editing loses nothing and duplicates nothing`,
    { annotation: prdRequirement(`A.resilience.restart.${label.toLowerCase()}`, `${label}: server restart with unsaved changes → nothing lost (saved or offered as a draft), no duplicate version, the editor reconnects after reload`) },
    async ({ prd, browser }) => {
      prd.requireFull(`native ${label} needs a deployment with native engines`)
      test.setTimeout(15 * 60_000)
      const { context: ctx, api: request, ws } = await isolatedActor(prd, browser, `restart ${label}`, { viewport: N.VIEWPORT })
      const probe = await N.nativeEnabled(request, ws.id)
      expect(probe.enabled, `native ${label} editing must be on: ${probe.detail}`).toBe(true)
      const ref = await N.createNative(request, ws.id, app, `PRD restart ${label}`)
      const ops = N.instrument(ctx)
      const tag = Math.random().toString(36).slice(2, 6)
      const M = { M1: `M1saved${tag}`, M2: `M2unsaved${tag}`, M3: `M3after${tag}` }

      let page = await ctx.newPage()
      await N.openEditor(page, ref)
      const f = await N.acquire(page, app)
      await N.typeMarker(page, f, app, M.M1, "A1")
      const v0 = await saveByAutosaveOrKey(page, ref.initVersion)
      expect(v0, "the first save is confirmed").toBeTruthy()
      // Unsaved work needs continuous typing (the editor saves 300 ms after the
      // last keystroke). The person keeps typing while the server restarts.
      await placeCaret(page, app, 5)
      const typing = typeLines(page, M.M2, 40, 40, 400)
      await page.waitForTimeout(2_500)
      const headBefore = await N.readHead(request, ref)
      expect(headBefore.text).toContain(M.M1)
      expect(headBefore.text, "the typing is not saved yet").not.toContain(`${M.M2}-0`)
      const savesBefore = ops.filter((o) => o.kind === "save").length
      const restart = await restartServer(prd.origin, `row A.resilience.restart.${label.toLowerCase()}`)
      const typedLines = await typing

      // What the open editor does on its own after the server is back.
      const timeline: Awaited<ReturnType<typeof N.uiState>>[] = []
      for (let i = 0; i < 30; i++) {
        await N.pause(2_000)
        timeline.push(await N.uiState(page))
        if (timeline.at(-1)!.recoveries.length || timeline.at(-1)!.alerts.length || /odpojen|Uloženo/.test(timeline.at(-1)!.status)) break
      }
      const savesWithoutPerson = ops.slice(savesBefore).filter((o) => o.kind === "save")
      const headAfter = await N.readHead(request, ref)
      expect(headAfter.text, "the work saved before the restart survived it").toContain(M.M1)
      const savedByEditor = headAfter.text.includes(`${M.M2}-0`)

      // The disconnected editor still offers the unsaved work: look into it.
      const draftsInSession = savedByEditor ? null : await inspectDrafts(page, `${M.M2}-0`)
      // The person reloads (after the editor's lease has run out). The document
      // opens; once the person takes the editor again the draft is offered
      // again (a draft is never imported into a read-only view).
      await N.pause(20_000)
      await page.close()
      page = await ctx.newPage()
      await N.openEditor(page, ref)
      await N.frameOf(page, app)
      await N.pause(2_000)
      const reopened = await N.uiState(page)
      const headReopened = await N.readHead(request, ref)
      expect(headReopened.version, "reopening saved nothing").toBe(headAfter.version)
      await N.acquire(page, app)
      await N.pause(1_500)
      const drafts = savedByEditor ? null : await inspectDrafts(page, `${M.M2}-0`)
      if (!savedByEditor) {
        expect(draftsInSession!.rows.length, `the unsaved work is offered as a draft in the open editor (ui ${JSON.stringify(timeline.at(-1))})`).toBeGreaterThan(0)
        expect(draftsInSession!.hasMarker.some(Boolean), "the draft offered in the open editor holds the unsaved work").toBe(true)
        expect(drafts!.rows.length, `the unsaved work is offered again after the reload (ui ${JSON.stringify(reopened)})`).toBeGreaterThan(0)
        expect(drafts!.hasMarker.some(Boolean), "the draft offered after the reload holds the unsaved work").toBe(true)
      }

      // The editor works again: type, save once.
      // A freshly mounted editor can still be replacing its frame: let it settle, take the current frame, focus it.
      await N.pause(2_000)
      await placeCaret(page, app, 3)
      await page.keyboard.type(M.M3, { delay: 8 })
      await page.keyboard.press("Enter")
      const v1 = await saveByAutosaveOrKey(page, headReopened.version)
      expect(v1, "a save after the restart is confirmed").toBeTruthy()
      await N.release(page).catch(() => {})
      const fin = await N.readHead(request, ref)

      // No duplicate version: every version came from a distinct save the editor sent.
      const committed = ops.filter((o) => o.kind === "save" && o.status === 200).length
      const unanswered = ops.filter((o) => o.kind === "save" && o.status === undefined).length
      const repeats = ops.filter((o, i) => o.status === 200 && o.contentHash && ops.slice(0, i).some((p) => p.status === 200 && p.contentHash === o.contentHash))
      await prd.evidence("result.json", {
        appSha: prd.appSha, app, pageId: ref.pageId, restart, initVersion: ref.initVersion,
        baselineSaved: v0, headBefore: { version: headBefore.version, hash: headBefore.hash },
        typedLines, timelineAfterRestart: timeline.slice(-3), savesWithoutPerson: savesWithoutPerson.length,
        headAfterRestart: { version: headAfter.version, hash: headAfter.hash, savedByEditor },
        draftsInSession, reopened, drafts, afterReopen: { version: headReopened.version },
        savedAfterRestart: v1 ?? null, final: { version: fin.version, hash: fin.hash, ...Object.fromEntries(Object.entries(M).map(([k, v]) => [k, fin.text.includes(v)])) },
        versionAccounting: { init: ref.initVersion, committedSaves: committed, unanswered }, ops,
      })
      expect(fin.text.includes(M.M1) && fin.text.includes(M.M3), "the work before and after the restart is in the document").toBe(true)
      expect(savesWithoutPerson.filter((o) => o.status === 200).length, "nothing was saved behind the person's back after the restart").toBe(savedByEditor ? savesWithoutPerson.length : 0)
      expect(repeats.map((o) => o.n), "no save repeated identical bytes").toEqual([])
      expect(BigInt(fin.version) - BigInt(ref.initVersion), "no duplicate version").toBeLessThanOrEqual(BigInt(committed + unanswered))
      expect(fin.version, "the save after the restart is the next version").toBe(v1)
    },
  )
}
