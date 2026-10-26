import { expect, test, type Page } from '@playwright/test'
import { STAGES, canvas, focusDocument, openEditors, readState, referencedList, selectAll, selectedProps, selectSeedWord, settings, text, typeConfirmed, pressConfirmed, observerFor, waitRendered, waitSaved, type CommittedState, type Stage } from './contract'
import type { WordCraftHTTPObserver } from './http-transport'

import { features, type Feature } from './features'
import { applyNativeAction } from './native-actions'
import { featureCrops, glyphImage, stableGlyphImage } from './visual'

const prefix = 'Prefix ', suffix = ' tail'

function verifyFeature(feature: Feature, state: CommittedState, seed: string, word: string) {
  const paragraphs = state.document.body
  if (feature.id === 'text') expect(text(state)).toBe(`${prefix}Z${suffix}`)
  else if (feature.id === 'enter') expect(paragraphs.map(p => p.text)).toEqual([prefix, suffix])
  else {
    expect(text(state)).toBe(seed)
    if (feature.character) for (const props of selectedProps(state, prefix.length, word.length)) expect(props[feature.character.field]).toEqual(feature.character.value)
    if (feature.paragraph) expect(paragraphs[0].props?.[feature.paragraph.field]).toEqual(feature.paragraph.value)
    if (feature.group === 'lists') {
      const numbering = paragraphs[0].props?.numbering as { num: number; level: number } | undefined
      expect(numbering, 'The paragraph must refer to a real native list definition').toBeTruthy()
      expect(numbering?.num).toEqual(expect.any(Number))
      const nativeNumbering = state.document.numbering
      expect(nativeNumbering, 'List definitions must persist along with the paragraph reference').toBeTruthy()
      const list = nativeNumbering?.nums.find(item => item.id === numbering?.num)
      expect(list, 'The actual referenced list must exist').toBeTruthy()
      const level = list?.levelOverrides?.find(([index]) => index === numbering?.level)?.[1]
        ?? nativeNumbering?.abstracts.find(item => item.id === list?.abstractId)?.levels[numbering!.level]
      expect(level?.format).toBe(feature.id === 'bullets' ? 'bullet' : 'decimal')
    }
  }
}
async function apply(page: Page, feature: Feature) {
  if (feature.key) { await page.keyboard.press(feature.key); return }
  await applyNativeAction(page, feature.id)
}

for (const feature of features) {
  test(`${feature.id}: selection → engine → Saved → peer → reload → own undo`, {
    annotation: [{ type: 'wordcraft-feature', description: feature.id }, { type: 'wordcraft-group', description: feature.group }],
  }, async ({ browser }, testInfo) => {
    const stages: Record<Stage, string> = Object.fromEntries(STAGES.map(stage => [stage, 'not-reached'])) as Record<Stage, string>
    let current: Stage = 'ui'
    let editors: Awaited<ReturnType<typeof openEditors>> | undefined
    const observers: WordCraftHTTPObserver[] = []
    const stage = (name: Stage) => { current = name }
    const done = (name: Stage) => { stages[name] = 'passed' }
    try {
      settings(feature.id) // No target means a hard failed test, without any fallback server.
      let observer!: WordCraftHTTPObserver
      editors = await openEditors(browser, undefined, feature.id)
      observer = observerFor(editors.a).observer
      observers.push(...editors.observers)
      const { a, b, statePath } = editors
      const crops = featureCrops(feature.id)
      const word = `Focus${feature.id}`
      const seed = `${prefix}${word}${suffix}`
      const initial = await readState(a, statePath)
      expect(initial.document.body).toHaveLength(1)
      expect(text(initial), 'Each feature requires a separate fresh blank disposable document').toBe('')
      await selectAll(a)
      await typeConfirmed(a, seed)
      await expect.poll(async () => text(await readState(a, statePath))).toBe(seed)
      const baseline = await readState(a, statePath)
      await waitSaved(a, baseline.revision)
      await waitRendered(a, baseline.revision)
      await waitRendered(b, baseline.revision)
      const beforePeerImage = await stableGlyphImage(b, crops.peer)
      // Baseline must make the tested change observable.
      if (feature.character) expect(selectedProps(baseline, prefix.length, word.length).every(props => JSON.stringify(props[feature.character!.field]) === JSON.stringify(feature.character!.value))).toBe(false)
      if (feature.paragraph) expect(baseline.document.body[0].props?.[feature.paragraph.field]).not.toEqual(feature.paragraph.value)
      if (feature.group === 'lists') expect(baseline.document.body[0].props?.numbering).toBeUndefined()
      await selectSeedWord(a, prefix.length, word.length)
      const cursor = observer.cursor()
      await apply(a, feature)
      const command = await observer.commandSince(cursor, feature.command)
      expect(command.base_revision).toBe(baseline.revision)
      expect(command.target).toMatchObject({ block_id: baseline.ids[0], start: prefix.length, end: prefix.length + word.length, unit: 'utf8_bytes' })
      if (feature.parameters) expect(command.params).toEqual(feature.parameters)
      done('ui')
      stage('engine')
      const receipt = await observer.saved(command.op_id, command.transportId)
      expect(receipt.transportId).toBe(command.transportId)
      expect(receipt.transportURL).toBe(command.transportURL)
      await expect.poll(async () => (await readState(a, statePath)).revision).toBe(receipt.revision)
      const committed = await readState(a, statePath)
      verifyFeature(feature, committed, seed, word)
      if (feature.character) {
        for (const position of [...Array(prefix.length).keys(), ...Array.from({ length: suffix.length }, (_, index) => prefix.length + word.length + index)]) {
          expect(selectedProps(committed, position, 1)).toEqual(selectedProps(baseline, position, 1))
        }
      }
      expect(BigInt(committed.revision)).toBeGreaterThan(BigInt(baseline.revision))
      done('engine')
      stage('saved')
      await waitSaved(a, receipt.revision, command.op_id)
      done('saved')
      stage('other-clients')
      await waitRendered(a, receipt.revision)
      await waitRendered(b, receipt.revision)
      await focusDocument(a)
      await a.keyboard.press('Control+End') // Put the local caret outside the configured glyph crop.
      const writerImage = await stableGlyphImage(a, crops.writer)
      const peerImage = await stableGlyphImage(b, crops.peer)
      expect(peerImage.equals(beforePeerImage), 'The peer must visibly render the tested document change').toBe(false)
      expect(peerImage.equals(writerImage), 'Both clients must paint the same document glyphs').toBe(true)
      const peerState = await readState(b, statePath)
      expect(peerState.document).toEqual(committed.document)
      verifyFeature(feature, peerState, seed, word)
      await testInfo.attach(`${feature.id}-peer-render`, { body: await canvas(b).screenshot(), contentType: 'image/png' })
      done('other-clients')
      stage('reload')
      await Promise.all([a.reload(), b.reload()])
      for (const page of [a, b]) {
        await waitRendered(page, receipt.revision)
        const reloaded = await readState(page, statePath)
        expect(reloaded.document).toEqual(committed.document)
        verifyFeature(feature, reloaded, seed, word)
        expect((await glyphImage(page, page === a ? crops.writer : crops.peer)).equals(page === a ? writerImage : peerImage), 'Reload must preserve the rendered result').toBe(true)
        await waitSaved(page, receipt.revision)
      }
      done('reload')
      stage('undo')
      // B contributes after A's change. A's undo must leave B's independent text.
      const peerMarker = `PeerOwn${feature.id}`
      await focusDocument(b)
      await b.keyboard.press('Control+End')
      await pressConfirmed(b, 'Enter', 'text.newParagraph')
      await typeConfirmed(b, peerMarker)
      await expect.poll(async () => text(await readState(b, statePath))).toContain(peerMarker)
      const withPeer = await readState(b, statePath)
      await waitSaved(b, withPeer.revision)
      await waitRendered(a, withPeer.revision)
      expect(withPeer.document.body.slice(0, committed.document.body.length)).toEqual(committed.document.body)
      const peerTail = withPeer.document.body.slice(committed.document.body.length)
      expect(peerTail.length).toBeGreaterThan(0)
      await focusDocument(a)
      const undoCursor = observer.cursor()
      await a.keyboard.press('Control+z')
      const undo = await observer.commandSince(undoCursor, 'participant.undo')
      const undoReceipt = await observer.saved(undo.op_id, undo.transportId)
      expect(undoReceipt.transportId).toBe(undo.transportId)
      await waitSaved(a, undoReceipt.revision, undo.op_id)
      await Promise.all([waitRendered(a, undoReceipt.revision), waitRendered(b, undoReceipt.revision)])
      for (const page of [a, b]) {
        const restored = await readState(page, statePath)
        expect(restored.revision).toBe(undoReceipt.revision)
        for (const peerParagraph of peerTail) expect(referencedList(restored, peerParagraph)).toEqual(referencedList(withPeer, peerParagraph))
        expect(restored.document.body, 'Undo restores exactly A’s change and preserves the entire peer contribution').toEqual([...baseline.document.body, ...peerTail])
        expect(restored.document.body.find(paragraph => paragraph.text === peerMarker), 'Undo must preserve the other participant’s text and formatting').toEqual(withPeer.document.body.find(paragraph => paragraph.text === peerMarker))
        expect(text(restored), 'Undo must preserve the other participant’s change').toContain(peerMarker)
      }
      done('undo')
    } catch (error) { if (stages[current] !== 'passed') stages[current] = 'failed'; throw error }
    finally {
      await testInfo.attach('wordcraft-stage-coverage', { body: JSON.stringify({ feature: feature.id, group: feature.group, stages }), contentType: 'application/json' })
      await Promise.all(observers.map(observer => observer.close()))
      await Promise.all(editors?.contexts.map(context => context.close()) ?? [])
    }
  })
}
