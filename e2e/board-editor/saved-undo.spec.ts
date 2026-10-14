import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'
import type { PageBoard } from '../../hooks/use-page-board'
import type { CollaborativeBatch } from '../../lib/boards/collaboration-types'

// Real production Board/editor with a deterministic CAS/receipt API model.
// Production persistence and live authorization are covered by server tests.
test('saved undo walks repeated edits and still refuses a foreign same-value revision', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  let board: PageBoard = { format: 'crewship-board-excalidraw-0.18.1-v1', seq: 0, can_write: true, records: [], placements: [] }
  const batches: CollaborativeBatch[] = []
  await page.route('**/api/v1/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (path.endsWith('/board/batches')) {
      const body = request.postDataJSON() as CollaborativeBatch
      const records = new Map(board.records.map(item => [item.element_id, item]))
      if (body.base_seq !== board.seq || body.upserts.some(item => (records.get(item.element.id)?.element_rev ?? 0) !== item.expected_rev) || body.deletes.some(item => records.get(item.id)?.element_rev !== item.expected_rev)) return route.fulfill({ status: 409, json: { error: 'Board authority changed' } })
      batches.push(body)
      for (const item of body.upserts) records.set(item.element.id, { element_id: item.element.id, element_rev: item.expected_rev + 1, element: item.element, deleted: false })
      for (const item of body.deletes) records.set(item.id, { element_id: item.id, element_rev: item.expected_rev + 1, element: null, deleted: true })
      board = { ...board, seq: board.seq + 1, records: [...records.values()] }
      return route.fulfill({ json: { seq: board.seq, result_seq: board.seq, element_revs: Object.fromEntries(board.records.map(item => [item.element_id, item.element_rev])), undo_expires_at: new Date(Date.now() + 86400000).toISOString() } })
    }
    if (path.endsWith('/board')) return route.fulfill({ json: board })
    if (path.endsWith('/board/threads')) return route.fulfill({ json: { threads: [], can_comment: false, can_resolve: false } })
    if (path.endsWith('/board/requests')) return route.fulfill({ json: { requests: [], eligible_chats: [], can_dispatch: false } })
    return route.fulfill({ status: 404, json: { error: 'Fixture route unavailable' } })
  })
  try {
    await page.goto(`${fixture.url}product.html`)
    const canvas = page.locator('canvas.interactive')
    await expect(canvas).toBeVisible()
    const box = (await canvas.boundingBox())!
    await canvas.click({ position: { x: 700, y: 350 } })
    await page.keyboard.press('r')
    await page.mouse.move(box.x + 350, box.y + 170)
    await page.mouse.down(); await page.mouse.move(box.x + 520, box.y + 270, { steps: 8 }); await page.mouse.up()
    const save = page.getByRole('button', { name: 'Save board', exact: true })
    await expect(save).toBeEnabled(); await save.click()
    await expect(page.getByText('Board · Saved sequence 1')).toBeVisible()
    const originalX = Number(board.records[0].element!.x)
    for (let seq = 2; seq <= 3; seq++) {
      await page.locator('canvas.interactive').click({ position: { x: 350 + seq - 2, y: 220 } })
      await page.keyboard.press('v'); await page.keyboard.press('ArrowRight')
      await expect(save).toBeEnabled(); await save.click()
      await expect(page.getByText(`Board · Saved sequence ${seq}`)).toBeVisible()
    }
    expect(board.records).toHaveLength(1)
    expect(Number(board.records[0].element!.x)).toBeGreaterThan(originalX)
    const undo = page.getByRole('button', { name: 'Undo last saved change', exact: true })
    for (let seq = 4; seq <= 5; seq++) {
      await undo.click()
      await expect(page.getByText(`Board · Saved sequence ${seq}`)).toBeVisible()
    }
    expect(batches[3].upserts[0].expected_rev).toBe(3)
    expect(batches[4].upserts[0].expected_rev).toBe(4)
    expect(board.records[0].element!.x).toBe(originalX)
    board = { ...board, seq: 6, records: [{ ...board.records[0], element_rev: 6 }] }
    await page.evaluate(() => window.boardProduct.refresh())
    await expect(page.getByText('Board · Saved sequence 6')).toBeVisible()
    await undo.click()
    await expect(page.getByRole('alert')).toContainText('connected content changed remotely')
    expect(batches).toHaveLength(5)
    expect(board.records[0].deleted).toBe(false)
  } finally { await fixture.close() }
})
