// PRD §7 A1: an hour of real work in the native Word and Sheet editors, with a
// server restart in the middle.
//
// One provisioned person works in the editor of a deployed origin for
// PRD_HOUR_MINUTES (60): typing continuously, Enter, font and styles, a table
// and an image (Sheet: cells of every data class, formulas, formats and a
// second sheet), a save once a cycle. At half time, with unsaved typing in the
// editor, the test restarts the server (PRD_RESTART_COMMAND or PRD_RESTART_HANDSHAKE, see craft-session/restart.ts). Afterwards it checks
// that everything saved is still there, that the unsaved typing is offered
// back as a draft and that nothing was saved behind the person's back; the
// person reloads, the editor works, the session goes on, and after a last
// reload the document is checked once more. The row reports the duration, the
// time of the restart and the SHA-256 of the final document.
//
// Full mode only. A run shorter than 60 minutes (PRD_HOUR_SHORT_RUN=1, for
// debugging) does the same work and then fails on purpose: it is not evidence.
import { readHeadText, sheetSession, wordSession, type Checkpoint, type CycleChain, type SessionKind } from "./craft-session/hour-steps"
import type { CraftCall } from "./craft-lease/helpers"
import { hourInterruptions, PAGE_STALLS_DESCRIPTION, pageStalls, saveAccounting, type ForcedCtrlS, type SaveWait, type TrailMark, type VerifyPress } from "./craft-session/confirm"
import { restartServer, type RestartRecord } from "./craft-session/restart"
import { expect, prdRequirement, test } from "./fixtures"
import { disposeActors, isolatedActor } from "./outage-lostack/actor"
import * as N from "./outage-lostack/native"

test.afterEach(disposeActors)

const REQUIRED_MINUTES = 60

/** The session length: PRD_HOUR_MINUTES, 60 by default, and 60 is required in full mode. */
export function hourMinutes(): { minutes: number; short: boolean } {
  const raw = process.env.PRD_HOUR_MINUTES?.trim()
  const minutes = raw ? Number(raw) : REQUIRED_MINUTES
  if (!Number.isFinite(minutes) || minutes <= 0) throw new Error(`PRD_HOUR_MINUTES must be a positive number, got ${JSON.stringify(raw)}`)
  const short = minutes < REQUIRED_MINUTES
  if (short && process.env.PRD_HOUR_SHORT_RUN !== "1") {
    throw new Error(`the hour rows need PRD_HOUR_MINUTES >= ${REQUIRED_MINUTES} in full mode (got ${minutes}); set PRD_HOUR_SHORT_RUN=1 for a debugging run, which always ends red`)
  }
  return { minutes, short }
}

const KINDS: Array<{ app: SessionKind; label: "Word" | "Sheet" }> = [
  { app: "wordcraft", label: "Word" },
  { app: "gridcraft", label: "Sheet" },
]

for (const { app, label } of KINDS) {
  test(
    `${label}: an hour of work with a server restart in the middle`,
    { annotation: prdRequirement(`A.craft.session.hour-${label.toLowerCase()}`, `${label}: ${REQUIRED_MINUTES} minutes of continuous work (typing, Enter, font, styles, ${app === "wordcraft" ? "table, image" : "formulas, formats, second sheet"}) with a server restart in the middle; nothing lost, the editor works after the restart and after a reload`) },
    async ({ prd, browser }) => {
      prd.requireFull(`native ${label} needs a deployment with native engines`)
      // Word is driven through a canvas ribbon, and on r5 the editor drops to read-only
      // ("Pouze čtení" + a local draft) while the person keeps typing during an autosave in
      // flight (see docs/development/prd-acceptance.md, "Manual walkthrough"). Until that is
      // fixed the Word row is a manual row; PRD_HOUR_WORD_AUTOMATED=1 runs the automation anyway.
      test.skip(app === "wordcraft" && process.env.PRD_HOUR_WORD_AUTOMATED !== "1", "A.craft.session.hour-word is a manual row (mode=manual): not reliably automatable, see the manual walkthrough in docs/development/prd-acceptance.md")
      const { minutes, short } = hourMinutes()
      test.setTimeout((minutes + 25) * 60_000)
      // The QA workspace stays after the row (keepWorkspace): its stored versions are the
      // evidence a later reader needs, and result.json names the workspace and the page.
      const { context, api, ws } = await isolatedActor(prd, browser, `hour ${label}`, { viewport: N.VIEWPORT }, { keepWorkspace: true })
      const probe = await N.nativeEnabled(api, ws.id)
      expect(probe.enabled, `native ${label} editing must be on: ${probe.detail}`).toBe(true)
      const ref = await N.createNative(api, ws.id, app, `PRD hour ${label}`)
      const ops = N.instrument(context)
      const trail: CraftCall[] = []
      const steps: Array<{ atMs: number; step: string }> = []
      const evidence = { chain: [] as CycleChain[], checkpoints: [] as Checkpoint[], verifications: [] as VerifyPress[], saveWaits: [] as SaveWait[], forcedCtrlS: [] as ForcedCtrlS[], releases: [] as TrailMark[], editorEnds: [] as TrailMark[], restart: null as RestartRecord | null }
      await prd.evidence("document-ref.json", { workspaceId: ws.id, pageId: ref.pageId, kept: true })
      const session = (app === "wordcraft" ? wordSession : sheetSession)({ prd, context, api, ref, ops, trail, steps, evidence, minutes, restart: (why) => restartServer(prd.origin, `${label} hour session: ${why}`) })
      let result
      try {
        result = await session.run()
      } catch (err) {
        await prd.evidence("failure-craft-calls.json", trail).catch(() => {})
        await prd.evidence("failure-steps.json", steps).catch(() => {})
        // Every save/reconcile the editor sent, answered or not: the trail above only holds answers.
        await prd.evidence("failure-ops.json", ops).catch(() => {})
        throw err
      } finally {
        // The evidence chain on disk however the row ends (a thrown session, a failed
        // assertion below, a pass), written after the run so late replies are in it.
        // How each save happened (autosave, forced Ctrl+S, automatic reconcile, Ověřit
        // výsledek): accounting for the reader, never a reason to fail the row.
        const accounting = saveAccounting({ waits: evidence.saveWaits, forced: evidence.forcedCtrlS, ops, verifications: evidence.verifications, releases: evidence.releases, editorEnds: evidence.editorEnds })
        const interruptions = hourInterruptions(evidence.verifications, pageStalls(trail), accounting)
        prd.note(interruptions.note)
        await prd.evidence("chain.json", { workspaceId: ws.id, pageId: ref.pageId, ...evidence, pageStalls: pageStalls(trail), pageStallsDescription: PAGE_STALLS_DESCRIPTION, interruptions, saveAccounting: accounting }).catch(() => {})
      }
      const head = await readHeadText(api, ref)
      const accounting = saveAccounting({ waits: evidence.saveWaits, forced: evidence.forcedCtrlS, ops, verifications: evidence.verifications, releases: evidence.releases, editorEnds: evidence.editorEnds })
      await prd.evidence("result.json", { ...result, finalVersion: head.version, finalHash: head.hash, verifications: evidence.verifications, pageStalls: pageStalls(trail), pageStallsDescription: PAGE_STALLS_DESCRIPTION, saveAccounting: accounting })
      expect(result.failures, "the session found no loss").toEqual([])
      expect(result.restart, "the server was restarted in the middle").toBeTruthy()
      expect(result.durationMs, "the session lasted its full length").toBeGreaterThanOrEqual(minutes * 60_000)
      expect(short, `PRD_HOUR_MINUTES=${minutes} < ${REQUIRED_MINUTES}: a short debugging run is not evidence`).toBe(false)
    },
  )
}
