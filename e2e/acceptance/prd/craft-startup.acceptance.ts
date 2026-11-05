// PRD A diagnostics for native editor startup (#4935): a real document, a real
// engine and a real server, with only the engine's arrival delayed in the
// browser. The frame stays hidden until the host has read proof; a slow start
// within the connection deadline opens, a longer one ends in the honest message,
// and a person whose access is withdrawn while it loads never sees the document.
import type { Page } from "@playwright/test"

import { peerApi, requireNative } from "./craft-lease/helpers"
import { APPLICATIONS, NativePage } from "./craft-lease/native"
import { pause } from "./drafts-crash/crash"
import { expect, prdRequirement, test } from "./fixtures"

const TAKE = "Převzít úpravy"
const REFUSED = "Editor se bezpečně nepřipojil"

/** Holds the engine's document, as a slow network or a loaded machine would. */
async function delayEngine(page: Page, ms: number): Promise<{ held(): number }> {
  let held = 0
  await page.route(/\/b\/[0-9a-f]{64}\/index\.html/, async (route) => {
    held++
    await pause(ms)
    await route.continue().catch(() => {})
  })
  return { held: () => held }
}

/** Samples whether any native frame is shown to the person. */
function watchFrame(page: Page) {
  const samples: Array<{ at: number; visible: boolean; present: boolean }> = []
  const started = Date.now()
  let running = true
  const loop = (async () => {
    while (running) {
      const state = await page
        .evaluate(() => {
          const frame = document.querySelector("iframe")
          if (!frame) return { present: false, visible: false }
          const style = getComputedStyle(frame)
          return { present: true, visible: style.visibility !== "hidden" && !frame.hasAttribute("inert") }
        })
        .catch(() => ({ present: false, visible: false }))
      samples.push({ at: Date.now() - started, ...state })
      await pause(100)
    }
  })()
  return {
    samples,
    firstVisible: () => samples.find((s) => s.visible)?.at ?? null,
    stop: async () => {
      running = false
      await loop
    },
  }
}

for (const { application, label } of APPLICATIONS) {
  test.describe(`${label} startup`, () => {
    test(
      `${label}: a slow start stays hidden, then opens`,
      { annotation: prdRequirement(`A.craft.startup.slow.${application}`, "A slow engine start keeps the frame hidden until read proof and then opens") },
      async ({ prd, page, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        const ws = await prd.qaWorkspace()
        const doc = await NativePage.create(request, ws.id, application, `PRD startup slow ${label}`)
        await requireNative(prd, request, doc)
        const engine = await delayEngine(page, 12_000)
        const frame = watchFrame(page)
        const started = Date.now()
        await page.goto(doc.editorPath)
        await expect(page.getByRole("button", { name: TAKE, exact: true })).toBeEnabled({ timeout: 45_000 })
        const openedAfterMs = Date.now() - started
        await frame.stop()
        expect(engine.held(), "the engine really was delayed").toBeGreaterThan(0)
        expect(frame.firstVisible(), "the frame is hidden while the engine is delayed").toBeGreaterThan(11_000)
        await expect(page.getByText(REFUSED)).toHaveCount(0)
        await prd.evidence("result.json", { application, appSha: prd.appSha, openedAfterMs, firstVisibleMs: frame.firstVisible(), samples: frame.samples.length })
      },
    )

    test(
      `${label}: a start past the connection deadline says so and shows nothing`,
      { annotation: prdRequirement(`A.craft.startup.deadline.${application}`, "An engine that misses the connection deadline ends in the honest message without showing a frame") },
      async ({ prd, page, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        const ws = await prd.qaWorkspace()
        const doc = await NativePage.create(request, ws.id, application, `PRD startup deadline ${label}`)
        await requireNative(prd, request, doc)
        await delayEngine(page, 40_000)
        const frame = watchFrame(page)
        await page.goto(doc.editorPath)
        await expect(page.getByText(REFUSED).first()).toBeVisible({ timeout: 35_000 })
        await frame.stop()
        expect(frame.firstVisible(), "no frame is ever shown").toBeNull()
        await expect(page.getByRole("button", { name: TAKE, exact: true })).toHaveCount(0)
        await prd.evidence("result.json", { application, appSha: prd.appSha, samples: frame.samples.length })
      },
    )

    test(
      `${label}: access withdrawn during a slow start shows nothing`,
      { annotation: prdRequirement(`A.craft.startup.revoked.${application}`, "Access withdrawn while the editor loads never reveals the document") },
      async ({ prd, request }) => {
        prd.requireFull("needs the deployed native editor bundles")
        const cleanup: Array<() => Promise<void>> = []
        try {
          const ws = await prd.qaWorkspace()
          const doc = await NativePage.create(request, ws.id, application, `PRD startup revoked ${label}`)
          await requireNative(prd, request, doc)
          const { peer, state } = await peerApi(prd, cleanup)
          await doc.grant(request, peer.userId, "write")
          const context = await prd.browserContext(state)
          const page = await context.newPage()
          await delayEngine(page, 12_000)
          const frame = watchFrame(page)
          await page.goto(doc.editorPath)
          await pause(3_000)
          await doc.revoke(request, peer.userId, "write")
          await pause(35_000)
          await frame.stop()
          expect(frame.firstVisible(), "the frame is never shown after the right is withdrawn").toBeNull()
          await expect(page.getByRole("button", { name: TAKE, exact: true })).toHaveCount(0)
          await expect(page.getByRole("button", { name: /Stáhnout|Kopie konceptu/ })).toHaveCount(0)
          await prd.evidence("result.json", { application, appSha: prd.appSha, samples: frame.samples.length, framePresentAtEnd: frame.samples.at(-1)?.present })
        } finally {
          for (const c of cleanup.reverse()) await c().catch(() => {})
        }
      },
    )
  })
}
