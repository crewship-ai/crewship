import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'

// Real Excalidraw history and keyboard behavior against a test-only CAS store.
// Production persistence and permission checks are separate server gates.
test('remote changes clear undo history, so mixed remote geometry cannot be overwritten by local undo', async ({ page }) => {
  const fixture = await startBoardFixture()
  try {
    await page.goto(fixture.url)
    await expect.poll(() => page.evaluate(() => window.boardGate?.ready())).toBe(true)
    const move = async (editor: number, x: number, y?: number) => {
      const seq = await page.evaluate(() => window.boardGate.snapshot().seq)
      await page.evaluate(({ editor, x, y }) => window.boardGate.move(editor, 'shape', x, y), { editor, x, y })
      await expect.poll(() => page.evaluate(() => window.boardGate.snapshot().seq)).toBe(seq + 1)
    }
    await move(1, 200) // Editor 0 has no local history. Remote NEVER must not undo.
    await page.locator('[data-editor="0"] canvas.interactive').click({ position: { x: 500, y: 350 } })
    await page.keyboard.press('Control+z')
    await page.waitForTimeout(200)
    expect(await page.evaluate(() => ({seq:window.boardGate.snapshot().seq,journal:window.boardGate.journal()}))).toMatchObject({seq:2})
    expect(await page.evaluate(() => window.boardGate.scene(0).find(item => item.id === 'shape')!.x)).toBe(200)
    await move(0, 300)
    // Editor 1 explicitly reviews the remote state and abandons older undo.
    await page.evaluate(() => window.boardGate.acknowledge(1))
    await move(1, 400, 180)
    await page.locator('[data-editor="0"] canvas.interactive').click({ position: { x: 500, y: 350 } })
    await page.keyboard.press('Control+z')
    await page.waitForTimeout(200)
    const result = await page.evaluate(() => ({ snapshot: window.boardGate.snapshot(), proposal: window.boardGate.proposal(0), scene: window.boardGate.scene(0), clears: window.boardGate.clears() }))
    expect(result.snapshot.seq).toBe(4)
    expect(result.snapshot.records.find(item => item.element.id === 'shape')!.element.y).toBe(180)
    expect(result.snapshot.records.find(item => item.element.id === 'shape')!.element.x).toBe(400)
    expect(result.proposal).toBeNull()
    expect(await page.evaluate(() => window.boardGate.rejected().length)).toBe(0)
    expect(result.scene.find(item => item.id === 'shape')!.x).toBe(400)
    expect(result.clears).toBeGreaterThanOrEqual(3)
    await page.keyboard.press('Control+z')
    await page.waitForTimeout(100)
    expect(await page.evaluate(() => window.boardGate.snapshot().seq)).toBe(4)
  } finally { await fixture.close() }
})

test('rejected grouped deletion is atomic and retains the entire proposal', async ({ page }) => {
  const fixture = await startBoardFixture()
  try {
    await page.goto(fixture.url)
    await expect.poll(() => page.evaluate(() => window.boardGate?.ready())).toBe(true)
    await page.evaluate(() => { window.boardGate.deny(); window.boardGate.deleteGroup(0) })
    await expect.poll(() => page.evaluate(() => window.boardGate.rejected().length)).toBe(1)
    expect(await page.evaluate(() => window.boardGate.proposal(0)!.deletes.map(item => item.id).sort())).toEqual(['other', 'shape'])
    expect(await page.evaluate(() => window.boardGate.snapshot().records.filter(item => !item.element.isDeleted).length)).toBe(2)
    await expect.poll(() => page.evaluate(() => window.boardGate.scene(0).filter(item => !item.isDeleted).length)).toBe(2)
    await page.evaluate(() => window.boardGate.deleteGroup(0))
    await page.waitForTimeout(200)
    expect(await page.evaluate(() => ({seq:window.boardGate.snapshot().seq,journal:window.boardGate.journal()}))).toMatchObject({seq:2})
    expect(await page.evaluate(() => window.boardGate.snapshot().records.filter(item => !item.element.isDeleted).length)).toBe(0)
  } finally { await fixture.close() }
})

test('public converter produces supported geometry for the server compatibility fixture', async ({ page }) => {
  const fixture = await startBoardFixture()
  try {
    await page.goto(fixture.url)
    await expect.poll(() => page.evaluate(() => window.boardGate?.ready())).toBe(true)
    const elements = await page.evaluate(() => window.boardGate.compatibility())
    expect(elements.map(item => item.type)).toEqual(expect.arrayContaining(['rectangle', 'ellipse', 'diamond', 'text', 'line', 'arrow', 'frame']))
    expect(elements.every(item => item.link === null && item.customData == null)).toBe(true)
    if (process.env.UPDATE_BOARD_COMPAT_FIXTURE === '1') {
      const { writeFileSync } = await import('node:fs')
      writeFileSync('internal/pages/testdata/board-excalidraw-0.18.1.json', JSON.stringify(elements, null, 2) + '\n')
    }
  } finally { await fixture.close() }
})

test('native local undo works before remote history reset', async ({ page }) => {
  const fixture = await startBoardFixture()
  try {
    await page.goto(fixture.url)
    await expect.poll(() => page.evaluate(() => window.boardGate?.ready())).toBe(true)
    await page.evaluate(() => window.boardGate.move(0, 'shape', 150))
    await expect.poll(() => page.evaluate(() => window.boardGate.snapshot().seq)).toBe(2)
    await page.locator('[data-editor="0"] canvas.interactive').click({ position: { x: 500, y: 350 } })
    await page.keyboard.press('Control+z')
    await expect.poll(() => page.evaluate(() => window.boardGate.snapshot().seq)).toBe(3)
    expect(await page.evaluate(() => window.boardGate.snapshot().records.find(item => item.element.id === 'shape')!.element.x)).toBe(50)
  } finally { await fixture.close() }
})
