import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'
import type { PageBoard } from '../../hooks/use-page-board'

test('production Board manually saves real drawing, preserves uncertain identity and conflicts, and removes denied content', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  let board: PageBoard = { format: 'board/v1', seq: 0, can_write: true, records: [], placements: [] }
  let denied = false
  let lost = false
  let pinnedDenied = false
  const batches: Record<string, unknown>[] = []
  const committed = new Set<string>()
  try {
    // Explicit test-only API model. Does not establish production authentication,
    // database atomicity, sharing enforcement or websocket delivery.
    await page.route('**/api/v1/**', async route => {
      const request = route.request()
      const path = new URL(request.url()).pathname
      if (path.endsWith('/board/batches')) {
        const body = request.postDataJSON(); batches.push(body)
        if (!committed.has(body.mutation_id)) {
          if (body.base_seq !== board.seq) return route.fulfill({ status: 409, json: { error: 'board changed; reload before saving' } })
          board = { ...board, seq: board.seq + 1, records: body.upserts.map((item: { element: Record<string, unknown> & { id: string }; expected_rev: number }) => ({ element_id: item.element.id, element_rev: item.expected_rev + 1, element: item.element, deleted: false })) }
          committed.add(body.mutation_id)
        }
        if (lost) { lost = false; return route.abort('failed') }
        return route.fulfill({ json: { seq: board.seq, result_seq: board.seq } })
      }
      if (path.endsWith('/board')) return route.fulfill({ status: denied ? 403 : 200, json: denied ? { error: 'Board access denied' } : board })
      if (path.endsWith('/artifact')) return route.fulfill({ status: pinnedDenied ? 403 : 200, json: pinnedDenied ? { error: 'Source access denied' } : { head_rev: 1, revision: 1, html: '<h1>Authorized pinned report</h1>', revisions: [{ revision: 1 }], can_write: true } })
      return route.fulfill({ status: 404, json: { error: 'Fixture route not configured' } })
    })
    await page.goto(`${fixture.url}product.html`)
    await expect(page.locator('canvas.interactive')).toBeVisible()
    const draw = async () => {
      const canvas = page.locator('canvas.interactive')
      const box = (await canvas.boundingBox())!
      await canvas.click({ position: { x: 700, y: 350 } })
      await page.keyboard.press('r')
      await page.mouse.move(box.x + 350, box.y + 170)
      await page.mouse.down(); await page.mouse.move(box.x + 520, box.y + 270, { steps: 8 }); await page.mouse.up()
      await expect(page.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    }
    await draw()
    expect(batches).toHaveLength(0)
    lost = true
    await page.getByRole('button', { name: 'Save board', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('unsaved drawing is retained')
    await page.getByRole('button', { name: 'Save board', exact: true }).click()
    await expect(page.getByText('Board · Saved sequence 1')).toBeVisible()
    expect(batches).toHaveLength(2)
    expect(batches[0]).toEqual(batches[1])
    expect(board.records).toHaveLength(1)
    await draw()
    board = { ...board, seq: 2 }
    await page.evaluate(() => window.boardProduct.refresh())
    await expect(page.getByText(/New changes are available/)).toBeVisible()
    await page.getByRole('button', { name: 'Save board', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('board changed')
    expect(batches[2].base_seq).toBe(1)
    await page.getByRole('button', { name: 'Reload latest', exact: true }).click()
    await expect(page.getByRole('dialog')).toContainText('Discard unsaved drawing?')
    await page.getByRole('button', { name: 'Discard and reload', exact: true }).click()
    await expect(page.getByText('Board · Saved sequence 2')).toBeVisible()
    pinnedDenied = true
    board = { ...board, seq: 3, placements: [{ id: 'pin', x: 350, y: 180, width: 400, height: 250, source: { page_slug: 'report', revision: 1, title: 'Report' } }] }
    await page.evaluate(() => window.boardProduct.refresh())
    await expect(page.getByText('Artifact unavailable', { exact: true }).first()).toBeVisible()
    await expect(page.locator('iframe[title="Static artifact: Report"]')).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Open artifact', exact: true })).toHaveCount(0)
    denied = true
    await page.evaluate(() => window.boardProduct.refresh())
    await expect(page.locator('canvas.interactive')).toHaveCount(0)
    await expect(page.getByRole('alert')).toContainText('Board access denied')
  } finally { await fixture.close() }
})
