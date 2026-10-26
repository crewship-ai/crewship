import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { ENGINE_SHA, canvas, focusDocument, openEditors, readState, selectAll, settings, text, waitRendered, waitSaved, observerFor } from './contract'
import { runWordCraftLatency, type Rectangle } from './latency'
import type { WordCraftHTTPObserver } from './http-transport'

/** Standalone real-browser measurement entry. Select with --grep 'latency'. */
test('latency: key → pixels, send → committed Saved, change → peer pixels', async ({ browser }) => {
  test.setTimeout(900_000)
  const directory = resolve(process.env.WORDCRAFT_CE_OUTPUT_DIR ?? 'test-results/wordcraft-ce')
  mkdirSync(directory, { recursive: true })
  let editors: Awaited<ReturnType<typeof openEditors>> | undefined
  let observer: WordCraftHTTPObserver | undefined
  try {
    settings('latency')
    const cropsRaw = process.env.WORDCRAFT_CE_LATENCY_CROPS
    if (!cropsRaw) throw Error('WORDCRAFT_CE_LATENCY_CROPS must specify page-coordinate glyph-only writer/peer rectangles; no whole-canvas fallback')
    const crops = JSON.parse(cropsRaw) as { writer: Rectangle; peer: Rectangle }
    const samples = Number(process.env.WORDCRAFT_CE_LATENCY_SAMPLES ?? 30)
    const timeoutMs = 30_000
    editors = await openEditors(browser, undefined, 'latency')
    observer = observerFor(editors.a).observer
    const { a, b, statePath } = editors
    const passive = observer!
    const settle = async (opId: string, transportId: string) => {
      const receipt = await passive.saved(opId, transportId)
      await waitSaved(a, receipt.revision, opId)
      await Promise.all([waitRendered(a, receipt.revision), waitRendered(b, receipt.revision)])
    }
    const clear = async () => {
      const current = await readState(a, statePath)
      if (text(current) === '' && current.document.body.length === 1) {
        await waitSaved(a, current.revision)
        await Promise.all([waitRendered(a, current.revision), waitRendered(b, current.revision)])
        return
      }
      await selectAll(a)
      const cursor = passive.cursor()
      await a.keyboard.press('Backspace')
      const command = await passive.commandSince(cursor, 'text.backspace')
      await settle(command.op_id, command.transportId)
      expect(text(await readState(a, statePath))).toBe('')
    }
    const undo = async () => {
      await focusDocument(a)
      const cursor = passive.cursor()
      await a.keyboard.press('Control+z')
      const command = await passive.commandSince(cursor, 'participant.undo')
      await settle(command.op_id, command.transportId)
    }
    const report = await runWordCraftLatency({
      writer: a, peer: b, observer: passive, samples, timeoutMs, engineSha: ENGINE_SHA,
      writerCrop: crops.writer, peerCrop: crops.peer,
      prepareBlank: clear, focusWriter: () => focusDocument(a), undoOwn: undo,
      readState: page => readState(page, statePath), waitSaved, waitRendered,
      canvasBounds: async (page: Page) => {
        const bounds = await canvas(page).boundingBox()
        if (!bounds) throw Error('Native canvas is not visible')
        return bounds
      },
    })
    writeFileSync(resolve(directory, 'latency.json'), JSON.stringify({ result: 'measured', ...report }, null, 2) + '\n')
  } catch (error) {
    writeFileSync(resolve(directory, 'latency.json'), JSON.stringify({ result: 'failed', measured: false, reason: error instanceof Error ? error.message : 'measurement failed', metrics: null }, null, 2) + '\n')
    throw error
  } finally {
    await Promise.all(editors?.observers.map(value => value.close()) ?? [])
    await Promise.all(editors?.contexts.map(context => context.close()) ?? [])
  }
})
