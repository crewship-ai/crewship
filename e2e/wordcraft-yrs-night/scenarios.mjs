import assert from 'node:assert/strict'
import { writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { performance } from 'node:perf_hooks'
import { observeKeys, classifyClockWindow } from './latency-observer.mjs'
import { InconclusiveMeasurement, MissingContract } from './run.mjs'

const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
const focus = async (s, page) => { await s.canvas(page).click() }
function formats(paragraph) {
  if (!Array.isArray(paragraph.runs)) throw new MissingContract('Full native run formatting projection is required for split/join preservation')
  const result = []
  for (const run of paragraph.runs) {
    const { len, ...properties } = run
    assert.ok(Number.isSafeInteger(len) && len >= 0)
    for (let index = 0; index < len; index++) result.push(properties)
  }
  assert.equal(result.length, Buffer.byteLength(paragraph.text, 'utf8'))
  return result
}
async function typeAt(s, page, paragraph, value, h) {
  await h.requireHook(s, 'selectParagraph')(page, paragraph, 'end')
  return h.saved(s, page, () => page.keyboard.type(value))
}
async function metrics(s, h) {
  const m = await h.requireHook(s, 'metrics')()
  for (const key of ['documentBytes', 'snapshotCount', 'snapshotBytes', 'journalBytes', 'receiptBytes', 'historyBytes', 'sqlBytes', 'walBytes', 'rssBytes', 'revision']) {
    assert.ok(Number.isSafeInteger(m[key]) && m[key] >= 0, `Missing real metric ${key}`)
  }
  return { at: new Date().toISOString(), ...m }
}
async function atomicRecords(s, h, opId, expectedCount = 1) {
  const records = await h.requireHook(s, 'operationRecords')(opId)
  assert.equal(records.method, 'read-only-sql')
  for (const name of ['changes', 'receipts', 'audit']) {
    assert.equal(records[name].length, expectedCount, `${name} not atomic for ${opId}`)
    for (const row of records[name]) assert.equal(row.op_id, opId)
  }
  if (expectedCount) {
    for (const row of [...records.changes, ...records.audit]) {
      assert.equal(row.revision, records.receipts[0].revision)
      assert.equal(row.principal, records.receipts[0].principal)
    }
  }
  return records
}
async function pixels(page, crop) {
  return page.screenshot({ clip: crop, type: 'png', scale: 'css', animations: 'disabled', caret: 'hide' })
}
async function untilGlyph(page, crop, reference, timeout = 30_000) {
  const deadline = performance.now() + timeout
  do {
    if ((await pixels(page, crop)).equals(reference)) return performance.now()
    await pause(5)
  } while (performance.now() < deadline)
  throw new Error('Concrete W glyph pixels did not match the independently confirmed reference')
}

export const scenarios = {
  1: async (s, h) => {
    const a = s.pages[0]
    h.requireHook(s, 'resetBlank')
    h.requireHook(s, 'glyphCrop')
    const confirmedText = async expected => {
      const state = await h.requireHook(s, 'readConfirmedGlyphText')(a)
      assert.equal(state.method, 'authority-read')
      assert.equal(state.paragraphCount, 1)
      assert.equal(state.text, expected)
      return state
    }
    const count = Number(process.env.WORDCRAFT_NIGHT_SAMPLES ?? 30)
    assert.ok(Number.isSafeInteger(count) && count >= 20 && count <= 1000)
    // Independent qualification of the glyph reference is mandatory. A crop
    // from this measured editor is not its own proof that a complete w rendered.
    const crop = await s.glyphCrop(a)
    const bounds = await s.canvas(a).boundingBox()
    assert.ok(bounds && crop.width > 0 && crop.height > 0 && crop.x >= bounds.x && crop.y >= bounds.y && crop.x + crop.width <= bounds.x + bounds.width && crop.y + crop.height <= bounds.y + bounds.height)
    const oracle = await h.requireHook(s, 'glyphReference')({ glyph: 'w', crop })
    assert.ok(['standalone-pinned-engine', 'reviewed-pixel-oracle'].includes(oracle.method))
    assert.equal(oracle.glyph, 'w')
    assert.equal(oracle.engineSHA, s.engineSHA)
    assert.ok(/^[a-f0-9]{40}$/.test(s.engineSHA))
    assert.ok(oracle.producerId && s.producerId && oracle.producerId !== s.producerId)
    assert.equal(oracle.includesEntireGlyph, true)
    assert.ok(oracle.source?.path && oracle.source?.line && oracle.source?.sha)
    const reference = oracle.png
    assert.ok(Buffer.isBuffer(reference) && reference.length > 8)
    await writeFile(resolve(h.output, 'glyph-w-reference.png'), reference)
    await s.resetBlank()
    await confirmedText('')
    assert.ok(!(await pixels(a, crop)).equals(reference), 'Blank and concrete glyph must differ')
    // Explicit negative oracle before real COMMIT, followed by positive
    // original-operation evidence. This phase is excluded from latency samples.
    const gate = await h.requireHook(s, 'holdBeforeCommit')()
    const statusCursor = await h.requireHook(s, 'savedTransitionCursor')(a)
    const receiptCursor = await s.receiptCursor(a)
    await focus(s, a)
    await a.keyboard.press('KeyW')
    const pending = await gate.waitBeforeCommit()
    await atomicRecords(s, h, pending.op_id, 0)
    const premature = await h.requireHook(s, 'savedTransitionsSince')(a, statusCursor)
    assert.ok(!premature.some(event => event.state === 'Saved'), 'Host showed Saved before actual COMMIT')
    await gate.release()
    await h.verifySaved(s, a, await s.waitSaved(a, receiptCursor))
    await untilGlyph(a, crop, reference)
    await s.resetBlank()
    const keys = await observeKeys(s.canvas(a))
    const samples = []
    for (let i = 0; i < count; i++) {
      await focus(s, a)
      const cursor = await h.requireHook(s, 'receiptCursor')(a)
      const keyCursor = await keys.cursor()
      const start = performance.now()
      // Observation starts before dispatch; screenshot completion gives an
      // upper bound, with no RAF used as a proxy for rendering.
      const glyph = untilGlyph(a, crop, reference)
      const committed = s.waitSaved(a, cursor).then(receipt => ({ receipt, time: performance.now() }))
      const [visible, result] = await Promise.all([glyph, committed, a.keyboard.press('KeyW')])
      const events = (await keys.since(keyCursor)).filter(event => event.code === 'KeyW')
      assert.equal(events.length, 1, 'Exactly one actual target-frame keydown required')
      const interval = keys.nodeInterval(events[0])
      const clockWindow = classifyClockWindow(interval, start, visible)
      const verified = await h.verifySaved(s, a, result.receipt)
      assert.ok(verified.savedObservedAt >= start)
      assert.equal(result.receipt.durability, 'committed')
      assert.ok(result.receipt.op_id)
      await confirmedText('w')
      samples.push({ event: events[0], eventNodeInterval: interval, clockWindow, apiDispatchToGlyphMs: visible - start, keyToGlyphUpperMs: visible - interval.earliest, keyToSavedUpperMs: verified.savedObservedAt - interval.earliest, op_id: result.receipt.op_id })
      await s.resetBlank()
      await confirmedText('')
      assert.ok(!(await pixels(a, crop)).equals(reference))
    }
    const report = { method: 'trusted target-frame DOM keydown → independently qualified glyph PNG, calibrated observation upper bound', glyph: 'w', oracle: { ...oracle, png: undefined }, clockCalibration: keys.calibration, samples, keyToGlyphUpperMs: h.summarize(samples.map(v => v.keyToGlyphUpperMs)), keyToSavedUpperMs: h.summarize(samples.map(v => v.keyToSavedUpperMs)), apiDispatchToGlyphMs: h.summarize(samples.map(v => v.apiDispatchToGlyphMs)) }
    await writeFile(resolve(h.output, 'latency.json'), JSON.stringify(report, null, 2))
    if (samples.some(sample => sample.clockWindow !== 'consistent')) throw new InconclusiveMeasurement('Clock calibration interval overlaps the observation boundary or is inconsistent; raw samples retained in latency.json')
    if (report.keyToGlyphUpperMs.p95 > 50 || report.keyToSavedUpperMs.p95 > 300) throw new InconclusiveMeasurement('Observation upper bounds exceed SLO; this instrument cannot establish the actual latency SLO violation')
    return report
  },
  2: async (s, h) => {
    const mcp = h.requireHook(s, 'mcp')
    const initial = await h.convergence(s)
    assert.ok(initial.paragraphs.length >= 2, 'Fixture needs two paragraphs')
    const receipts = []
    for (const mode of ['same', 'different']) {
      const beforeMode = await h.convergence(s)
      const markers = [`Jana_${mode}_`, `Petr_${mode}_`, `Agent_${mode}_`]
      const [a, b] = initial.paragraphs.map(p => p.id)
      const results = await Promise.all([
        typeAt(s, s.pages[0], a, markers[0], h),
        typeAt(s, s.pages[1], mode === 'same' ? a : b, markers[1], h),
        mcp({ command: 'text.insert', paragraph: mode === 'same' ? a : b, text: markers[2] }),
      ])
      assert.equal(results[2].transport, 'mcp')
      assert.ok(results[2].principal && !s.identities.includes(results[2].principal))
      assert.equal(results[2].durability, 'committed')
      assert.ok(results[2].mandate_id && results[2].authorship?.principal === results[2].principal)
      assert.equal(results[2].authorship.mandate_id, results[2].mandate_id)
      const agentRows = await atomicRecords(s, h, results[2].op_id)
      assert.equal(agentRows.audit[0].principal, results[2].principal)
      assert.equal(agentRows.audit[0].mandate_id, results[2].mandate_id)
      const after = await h.convergence(s)
      const counts = text => [...text.replaceAll('\n', '')].reduce((result, character) => { result[character] = (result[character] ?? 0) + 1; return result }, {})
      assert.deepEqual(counts(h.text(after)), counts(h.text(beforeMode) + markers.join('')), 'Concurrent input lost or duplicated characters')
      // Concurrent per-character inserts may interleave. Check each person's
      // original stream as a subsequence plus counts, rather than contiguous.
      for (const [index, marker] of markers.entries()) {
        const id = index === 0 || mode === 'same' ? a : b
        const value = after.paragraphs.find(p => p.id === id).text
        let cursor = 0
        for (const character of value) if (character === marker[cursor]) cursor++
        assert.equal(cursor, marker.length, `Participant ${index} input lost/reordered`)
      }
      receipts.push(results)
    }
    // Split while a second participant inserts into the original stable block.
    // The fixture's UI selector anchors the split at the original paragraph end;
    // do not infer paragraph identity from positional indices after the split.
    const beforeSplit = await h.convergence(s)
    const policy = await h.requireHook(s, 'splitJoinPolicy')()
    assert.ok(['left', 'right'].includes(policy.concurrentInsertionAtSplit))
    assert.equal(policy.retainedSplitId, 'left')
    assert.equal(policy.joinSurvivor, 'left')
    assert.ok(policy.source?.path && policy.source?.line && policy.source?.sha)
    const original = beforeSplit.paragraphs[0]
    await Promise.all(s.pages.map(page => s.selectParagraph(page, original.id, 'end')))
    await Promise.all([
      h.saved(s, s.pages[0], () => s.pages[0].keyboard.press('Enter')),
      h.saved(s, s.pages[1], () => s.pages[1].keyboard.press('KeyP')),
    ])
    const split = await h.convergence(s)
    assert.equal(split.paragraphs.length, beforeSplit.paragraphs.length + 1)
    assert.ok(split.paragraphs.some(p => p.id === original.id), 'Split discarded original paragraph ID')
    const originalIndex = split.paragraphs.findIndex(p => p.id === original.id)
    const next = split.paragraphs[originalIndex + 1]
    assert.ok(next, 'Split must create a following stable block')
    assert.equal(split.paragraphs[originalIndex].text, original.text + (policy.concurrentInsertionAtSplit === 'left' ? 'p' : ''))
    assert.equal(next.text, policy.concurrentInsertionAtSplit === 'right' ? 'p' : '')
    assert.deepEqual(formats(split.paragraphs[originalIndex]).slice(0, Buffer.byteLength(original.text, 'utf8')), formats(original), 'Split changed confirmed character formatting')
    const properties = paragraph => Object.fromEntries(Object.entries(paragraph).filter(([key]) => !['id', 'text', 'runs'].includes(key)))
    assert.deepEqual(properties(split.paragraphs[originalIndex]), properties(original), 'Split changed original paragraph formatting')
    for (const paragraph of beforeSplit.paragraphs.slice(1)) assert.deepEqual(split.paragraphs.find(p => p.id === paragraph.id), paragraph, 'Split damaged another paragraph')
    await s.selectParagraph(s.pages[0], next.id, 'start')
    await h.saved(s, s.pages[0], () => s.pages[0].keyboard.press('Backspace'))
    const joined = await h.convergence(s)
    assert.equal(joined.paragraphs.length, beforeSplit.paragraphs.length)
    assert.ok(joined.paragraphs.some(p => p.id === original.id), 'Join discarded original stable paragraph ID')
    assert.equal(h.text(joined).replaceAll('\n', ''), h.text(split).replaceAll('\n', ''), 'Join lost or reordered text')
    const joinedParagraph = joined.paragraphs.find(p => p.id === original.id)
    assert.deepEqual(formats(joinedParagraph), [...formats(split.paragraphs[originalIndex]), ...formats(next)], 'Join changed confirmed character formatting')
    assert.deepEqual(properties(joinedParagraph), properties(original))
    return { receipts, state: await h.convergence(s) }
  },
  3: async (s, h) => {
    const format = h.requireHook(s, 'formatSelection')
    const paragraph = (await h.convergence(s)).paragraphs[0].id
    await s.selectParagraph(s.pages[0], paragraph, 'all')
    await h.saved(s, s.pages[0], () => format(s.pages[0], { color: '#ff0000' }))
    await h.convergence(s)
    await s.selectParagraph(s.pages[1], paragraph, 'all')
    await h.saved(s, s.pages[1], () => format(s.pages[1], { color: '#0000ff' }))
    const blue = await h.convergence(s)
    assert.ok(blue.paragraphs[0].runs.length > 0 && blue.paragraphs[0].runs.every(run => run.color === '#0000ff'))
    await focus(s, s.pages[0])
    await h.saved(s, s.pages[0], () => s.pages[0].keyboard.press('Control+z'))
    const undone = await h.convergence(s)
    assert.deepEqual(h.document(undone), h.document(blue), 'Jana undo overwrote Petr blue')
    return { preservedPetrBlue: true, state: undone }
  },
  4: async (s, h) => {
    const prepare = h.requireHook(s, 'rejectDependency')
    const before = await h.convergence(s)
    // This hook changes a real mandate/role, then holds an actual wire write
    // before server authorization. It must not fabricate an authority reply.
    const control = await prepare()
    await focus(s, s.pages[0])
    await s.pages[0].keyboard.type('ForbiddenA')
    await control.waitPending('A')
    await s.pages[0].keyboard.type('DependentB')
    await control.waitPending('B')
    await control.release()
    const rejection = await control.rejected()
    assert.equal(rejection.reason, 'outside_mandate')
    assert.equal(rejection.authority, 'product')
    assert.deepEqual(h.document(await h.convergence(s)), h.document(before))
    const draft = await h.requireHook(s, 'retainedDraft')(s.pages[0])
    assert.ok(draft.includes('ForbiddenA') && draft.includes('DependentB'), 'A and dependent B must remain accessible as a proposal')
    return { rejected: true, draftPreserved: true }
  },
  5: async (s, h) => {
    const control = await h.requireHook(s, 'holdBeforeAuthority')()
    const before = await h.convergence(s)
    await focus(s, s.pages[0])
    await s.pages[0].keyboard.type('RevokedPending')
    await control.waitPending()
    const revoke = await h.requireHook(s, 'revokeWriter')(s.identities[0])
    assert.equal(revoke.authority, 'product')
    await control.release()
    assert.equal((await control.rejected()).reason, 'forbidden')
    assert.deepEqual(h.document(await h.convergence(s)), h.document(before))
    assert.match(await s.retainedDraft(s.pages[0]), /RevokedPending/)
    assert.equal(await h.requireHook(s, 'writeMode')(s.pages[0]), 'read-only')
    await focus(s, s.pages[1])
    await h.saved(s, s.pages[1], () => s.pages[1].keyboard.type('ReadableAfterWriteRevoke_'))
    assert.match(h.text(await h.convergence(s)), /ReadableAfterWriteRevoke_/)
    const delivery = await h.requireHook(s, 'deliveryCursor')(s.pages[0])
    const readRevoke = await h.requireHook(s, 'revokeReader')(s.identities[0])
    assert.equal(readRevoke.authority, 'product')
    await h.saved(s, s.pages[1], () => s.pages[1].keyboard.type('MustNotReachRevokedReader_'))
    await h.requireHook(s, 'deliveryFence')(s.pages[1])
    assert.deepEqual(await h.requireHook(s, 'contentDeliveriesSince')(s.pages[0], delivery), [], 'Read revocation leaked new content')
    assert.equal(await h.requireHook(s, 'readMode')(s.pages[0]), 'denied')
    // Offline scenario uses a separate fresh writable fixture, preserving this
    // test's revoked identity rather than silently granting it access again.
    const offline = await h.requireHook(s, 'offlineProposalFixture')()
    try {
      const base = await offline.readCommitted()
      await offline.disconnect()
      await offline.canvas.click()
      await offline.page.keyboard.type('OfflineUnknownProposal_')
      const revokeOffline = await offline.revokeWriter()
      assert.equal(revokeOffline.authority, 'product')
      await offline.reconnect()
      assert.equal((await offline.reauthorization()).write, false)
      assert.equal(await offline.writeMode(), 'read-only')
      assert.deepEqual(await offline.readCommitted(), base, 'Unknown offline writes were automatically replayed')
      assert.match(await offline.retainedDraft(), /OfflineUnknownProposal_/)
      assert.deepEqual(await offline.submissionsAfterReconnect(), [], 'Reconnect replayed an unconfirmed old source')
    } finally { await offline.close() }
    return { revokedInFlight: true, writerBecameReader: true, readerDeliveryStopped: true, offlineProposalPreserved: true }
  },
  6: async (s, h) => {
    const control = await h.requireHook(s, 'dropNextCommittedReply')()
    const before = await h.convergence(s)
    const countsBefore = await h.requireHook(s, 'documentOperationCounts')()
    await s.selectParagraph(s.pages[0], before.paragraphs[0].id, 'end')
    await s.pages[0].keyboard.press('KeyL')
    const original = await control.committed()
    assert.ok(original.op_id)
    const rowsBefore = await atomicRecords(s, h, original.op_id)
    await control.retryOriginal()
    const receipt = await control.recovered()
    assert.equal(receipt.op_id, original.op_id)
    assert.equal(receipt.revision, original.revision)
    assert.deepEqual(receipt.originalResult, original.originalResult, 'Retry changed original operation result')
    assert.ok(receipt.originalResult)
    assert.deepEqual(await atomicRecords(s, h, original.op_id), rowsBefore, 'Retry duplicated or changed SQL change/receipt/audit')
    const expected = before.paragraphs.map(p => [p.id, p.text])
    expected[0][1] += 'l'
    assert.deepEqual((await h.convergence(s)).paragraphs.map(p => [p.id, p.text]), expected)
    const countsAfter = await s.documentOperationCounts()
    for (const name of ['changes', 'receipts', 'audit']) assert.equal(countsAfter[name], countsBefore[name] + 1, `Lost ACK duplicated ${name}`)
    return { op_id: original.op_id, duplicateWrites: 0 }
  },
  7: async (s, h) => {
    const restart = h.requireHook(s, 'coldRestart')
    await focus(s, s.pages[0])
    const receipt = await h.saved(s, s.pages[0], () => s.pages[0].keyboard.type('ConfirmedBeforeRestart_'))
    const before = await h.convergence(s)
    const started = performance.now()
    const control = await restart()
    assert.notEqual(control.beforePID, control.afterPID)
    assert.equal(control.cleanShutdown, false, 'This scenario requires cold restart')
    const after = await h.convergence(s)
    assert.deepEqual(h.document(after), h.document(before))
    assert.ok(BigInt(after.revision) >= BigInt(receipt.revision))
    const recoveryMs = performance.now() - started
    await atomicRecords(s, h, receipt.op_id)
    // The crash barrier is instrumentation at the actual transaction boundary;
    // no receipt or peer frame is synthesized by the test.
    const crash = await h.requireHook(s, 'crashAfterCommitBeforeBroadcast')()
    const peerBefore = await h.requireHook(s, 'readClient')(s.pages[1])
    await s.selectParagraph(s.pages[0], after.paragraphs[0].id, 'end')
    await s.pages[0].keyboard.press('KeyQ')
    const committed = await crash.waitCommittedAndKilled()
    assert.equal(committed.broadcastCount, 0)
    assert.equal(committed.savedReplyCount, 0)
    const durable = await atomicRecords(s, h, committed.op_id)
    assert.deepEqual(await s.readClient(s.pages[1]), peerBefore, 'Peer received data before post-commit crash barrier')
    await crash.restart()
    const caughtUp = await h.convergence(s)
    const expected = structuredClone(after.paragraphs)
    expected[0].text += 'q'
    assert.deepEqual(caughtUp.paragraphs.map(p => [p.id, p.text]), expected.map(p => [p.id, p.text]))
    assert.ok(BigInt(caughtUp.revision) >= BigInt(durable.receipts[0].revision))
    const timing = await h.requireHook(s, 'candidateTimings')(committed.op_id)
    assert.equal(timing.clock, 'server-monotonic')
    assert.ok(Number.isFinite(timing.creationMs) && timing.creationMs >= 0)
    assert.ok(Number.isFinite(timing.validationMs) && timing.validationMs >= 0)
    assert.ok(timing.source?.path && timing.source?.line && timing.source?.sha)
    const precommit = await h.requireHook(s, 'crashBeforeCommit')()
    await s.selectParagraph(s.pages[0], caughtUp.paragraphs[0].id, 'end')
    await s.pages[0].keyboard.press('KeyR')
    const rolledBack = await precommit.waitKilled()
    await atomicRecords(s, h, rolledBack.op_id, 0)
    await precommit.restart()
    assert.deepEqual(h.document(await h.convergence(s)), h.document(caughtUp), 'Pre-COMMIT crash left a partial durable transaction')
    return { recoveryMs, control, crashGapCaughtUp: true, precommitRolledBack: true, atomicRows: durable, candidateTiming: timing }
  },
  8: async (s, h) => {
    h.requireHook(s, 'coldRestart')
    h.requireHook(s, 'metrics')
    const samples = [await metrics(s, h)]
    const restartTimes = []
    const operations = new Set()
    const expectedReceipts = new Map()
    const a = s.pages[0]
    const initial = await h.convergence(s)
    const expected = initial.paragraphs.map(p => ({ id: p.id, text: p.text }))
    await h.requireHook(s, 'selectParagraph')(a, expected[0].id, 'end')
    h.requireHook(s, 'receiptLedger')
    h.requireHook(s, 'retryCommittedOriginal')
    let lastRevision = BigInt(initial.revision)
    const assertExpected = async () => {
      const actual = await h.convergence(s)
      assert.deepEqual(actual.paragraphs.map(p => ({ id: p.id, text: p.text })), expected, 'Saved operation lost/duplicated text')
      return actual
    }
    const assertLedger = async () => {
      const ledger = await s.receiptLedger()
      for (const [opId, revision] of expectedReceipts) {
        const rows = ledger.filter(row => row.op_id === opId)
        assert.equal(rows.length, 1, 'Receipt ledger duplicate or missing operation')
        assert.equal(rows[0].revision, revision)
      }
    }
    for (let index = 1; index <= 5000; index++) {
      const receipt = await h.saved(s, a, () => a.keyboard.press('KeyX'))
      assert.ok(!operations.has(receipt.op_id), '5000 distinct durable operations required')
      assert.ok(BigInt(receipt.revision) > lastRevision, 'Receipt revision failed to advance')
      lastRevision = BigInt(receipt.revision)
      operations.add(receipt.op_id)
      expectedReceipts.set(receipt.op_id, receipt.revision)
      expected[0].text += 'x'
      assert.equal((await assertExpected()).revision, receipt.revision)
      if (index % 500 === 0) {
        const before = await h.convergence(s)
        const started = performance.now()
        const result = await s.coldRestart()
        assert.notEqual(result.beforePID, result.afterPID)
        assert.equal(result.cleanShutdown, false)
        assert.deepEqual(h.document(await assertExpected()), h.document(before))
        await assertLedger()
        const rowsBefore = await atomicRecords(s, h, receipt.op_id)
        const duplicate = await s.retryCommittedOriginal(receipt.op_id)
        assert.equal(duplicate.op_id, receipt.op_id)
        assert.equal(duplicate.revision, receipt.revision)
        assert.deepEqual(await atomicRecords(s, h, receipt.op_id), rowsBefore)
        await assertLedger()
        await assertExpected()
        restartTimes.push(performance.now() - started)
        samples.push(await metrics(s, h))
        await writeFile(resolve(h.output, 'capacity-progress.json'), JSON.stringify({ operations: index, restartTimes, samples }, null, 2))
        await s.selectParagraph(a, expected[0].id, 'end')
      }
    }
    assert.ok(samples.at(-1).snapshotCount - samples[0].snapshotCount < 5000, 'A full snapshot was retained for every operation')
    const soakStart = performance.now()
    const duration = Number(process.env.WORDCRAFT_NIGHT_SOAK_MS ?? 3_600_000)
    assert.ok(duration >= 3_600_000, 'Qualification soak cannot be shortened below 60 minutes')
    const pid = samples.at(-1).pid
    assert.ok(Number.isInteger(pid) && pid > 0)
    while (performance.now() - soakStart < duration) {
      const receipt = await h.saved(s, a, () => a.keyboard.press('KeyS'))
      expected[0].text += 's'
      expectedReceipts.set(receipt.op_id, receipt.revision)
      await assertExpected()
      const sample = await metrics(s, h)
      assert.equal(sample.pid, pid, 'Soak must be uninterrupted in the same server process')
      samples.push(sample)
      await writeFile(resolve(h.output, 'soak-progress.json'), JSON.stringify({ elapsedMs: performance.now() - soakStart, samples }, null, 2))
      await pause(Math.min(10_000, Math.max(0, duration - (performance.now() - soakStart))))
    }
    return { operations: operations.size, restarts: restartTimes.length, recoveryMs: h.summarize(restartTimes), soakMs: performance.now() - soakStart, samples }
  },
  9: async (s, h) => {
    const a = s.pages[0]
    await h.requireHook(s, 'resetBlank')()
    await focus(s, a)
    const cdp = await a.context().newCDPSession(a)
    try {
      // CDP composition reaches Chromium's native input path; dispatching DOM
      // composition events would not prove the engine consumed IME input.
      const compositionCursor = await h.requireHook(s, 'compositionCursor')(a)
      const before = await h.requireHook(s, 'documentOperationCounts')()
      const assertPreedit = async expected => {
        const state = await h.requireHook(s, 'compositionState')(a)
        assert.equal(state.source, 'native-editor')
        assert.equal(state.active, true)
        assert.equal(state.text, expected)
        assert.equal(state.visible, true)
        assert.deepEqual(await s.documentOperationCounts(), before, 'Preedit became durable before composition commit')
      }
      await cdp.send('Input.imeSetComposition', { text: 'zrusit', selectionStart: 6, selectionEnd: 6 })
      await assertPreedit('zrusit')
      await cdp.send('Input.imeSetComposition', { text: '', selectionStart: 0, selectionEnd: 0 })
      assert.equal((await s.compositionState(a)).active, false)
      assert.deepEqual(await s.documentOperationCounts(), before)
      assert.equal(h.text(await h.convergence(s)), '')
      await cdp.send('Input.imeSetComposition', { text: 'prilis', selectionStart: 6, selectionEnd: 6 })
      await assertPreedit('prilis')
      const finalText = 'příliš žluťoučký kůň'
      await cdp.send('Input.imeSetComposition', { text: finalText, selectionStart: finalText.length, selectionEnd: finalText.length })
      await assertPreedit(finalText)
      await h.saved(s, a, () => cdp.send('Input.insertText', { text: finalText }))
      assert.equal(h.text(await h.convergence(s)), 'příliš žluťoučký kůň')
      assert.equal((await s.compositionState(a)).active, false)
      const events = await h.requireHook(s, 'compositionEventsSince')(a, compositionCursor)
      assert.ok(events.length && events.every(event => event.isTrusted === true))
      assert.ok(events.some(event => event.type === 'compositionstart'))
      assert.ok(events.some(event => event.type === 'compositionupdate' && event.data === 'prilis'))
      assert.ok(events.some(event => event.type === 'compositionend' && event.data === finalText))
      const after = await s.documentOperationCounts()
      for (const name of ['changes', 'receipts', 'audit']) assert.equal(after[name], before[name] + 1, 'Composition must commit one final transaction')
      await writeFile(resolve(h.output, 'chromium-ime.json'), JSON.stringify({ status: 'passed', method: 'CDP composition + observed native preedit/update/cancel + trusted composition trace + single final durable transaction', text: finalText, events, deadKeys: 'not-yet-tested' }, null, 2))
      // Native dead-key semantics need an actual platform keyboard layout.
      // Merely inserting the precomposed string is explicitly insufficient.
      await h.requireHook(s, 'deadKeyInput')(a, { deadKey: 'ˇ', baseKey: 'c', expected: 'č' })
      assert.equal(h.text(await h.convergence(s)), 'příliš žluťoučký kůňč')
      return { chromiumComposition: true, platformDeadKey: true }
    } finally { await cdp.detach() }
  },
}
