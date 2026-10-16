import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'
import type { PageBoard } from '../../hooks/use-page-board'

test('touch drawing and static pin selection stay on the production Board; curated library works offline', async ({ browser, browserName }) => {
  test.skip(browserName !== 'chromium', 'Chromium CDP supplies real touch input for this focused gesture contract')
  // Production React Board/editor, simulated API, no authenticated-server claim.
  // Static iframe interaction is deliberately disabled: this tests touch
  // passthrough, not activation of the hard-OFF interactive runtime.
  const fixture = await startBoardFixture({ product: true })
  const context = await browser.newContext({ hasTouch: true, viewport: { width: 1280, height: 900 } })
  const page = await context.newPage()
  page.on('pageerror', error => console.error('Touch fixture page error:', error.message))
  let board: PageBoard = { format: 'crewship-board-excalidraw-0.18.1-v1', seq: 0, can_write: true, records: [], placements: [{ id: 'touch-pin', placement_rev: 1, mode: 'fixed', x: 100, y: 100, width: 350, height: 250, source: { page_slug: 'report', revision: 1, pinned_revision: 1, title: 'Touch report' } }] }
  const batches: Record<string, unknown>[] = []
  const outsideFinished: string[] = []
  const outsideObserved: string[] = []
  const outsideFailures: { url: string; error: string | null }[] = []
  page.on('request', request => { if (!request.url().startsWith(fixture.url) && !/^(data|blob):/.test(request.url())) outsideObserved.push(request.url()) })
  page.on('requestfailed', request => { if (!request.url().startsWith(fixture.url) && !/^(data|blob):/.test(request.url())) outsideFailures.push({ url: request.url(), error: request.failure()?.errorText ?? null }) })
  page.on('requestfinished', request => { if (!request.url().startsWith(fixture.url) && !/^(data|blob):/.test(request.url())) outsideFinished.push(request.url()) })
  await page.route('https://**/*', route => route.abort())
  await page.route('**/api/v1/**', async route => {
    const request = route.request(); const path = new URL(request.url()).pathname
    if (path.endsWith('/board/batches')) {
      const body = request.postDataJSON(); batches.push(body)
      const records = new Map(board.records.map(record => [record.element_id, record]))
      for (const item of body.upserts) records.set(item.element.id, { element_id: item.element.id, element_rev: board.seq + 1, element: item.element, deleted: false })
      for (const item of body.placement_updates ?? []) board.placements = board.placements.map(pin => pin.id === item.id ? { ...pin, ...item, placement_rev: (pin.placement_rev ?? 0) + 1 } : pin)
      board = { ...board, seq: board.seq + 1, records: [...records.values()] }
      return route.fulfill({ json: { seq: board.seq, result_seq: board.seq, element_revs: Object.fromEntries(board.records.map(record => [record.element_id, record.element_rev])), placement_revs: Object.fromEntries(board.placements.map(pin => [pin.id, pin.placement_rev])), undo_expires_at: new Date(Date.now() + 86400000).toISOString() } })
    }
    if (path.endsWith('/board')) return route.fulfill({ json: board })
    if (path.endsWith('/artifact')) return route.fulfill({ json: { head_rev: 1, revision: 1, html: '<button>Inert preview control</button><h1>Touch report</h1>', revisions: [], can_write: false } })
    if (path.endsWith('/board/requests')) return route.fulfill({ json: { requests: [], eligible_chats: [], can_dispatch: false } })
    if (path.endsWith('/board/threads')) return route.fulfill({ json: { threads: [], can_comment: false, can_resolve: false } })
    return route.fulfill({ status: 404, json: { error: 'Test route unavailable' } })
  })
  try {
    await page.goto(`${fixture.url}product.html`)
    const canvas = page.locator('canvas.interactive')
    await expect(canvas).toBeVisible()
    const frame = page.locator('iframe[title="Static artifact: Touch report"]')
    await expect(frame).toBeVisible()
    expect(await frame.evaluate(node => getComputedStyle(node).pointerEvents)).toBe('none')
    const cdp = await context.newCDPSession(page)
    const drag = async (x: number, y: number, dx: number, dy: number) => {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y, id: 1 }] })
      for (let step = 1; step <= 8; step++) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x + dx * step / 8, y: y + dy * step / 8, id: 1 }] })
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
      // CDP sends the gesture much faster than human movement. Allow the
      // browser's post-touch compatibility-click suppression window to end
      // before a separate toolbar tap (not a retry of a missing save).
      await page.waitForTimeout(400)
    }
    const box = (await canvas.boundingBox())!
    await canvas.tap({ position: { x: 750, y: 320 } })
    await page.keyboard.press('r')
    await drag(box.x + 700, box.y + 200, 120, 90)
    await expect(page.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: 'Save board', exact: true }).tap()
    await expect(page.getByText('Board · Saved sequence 1')).toBeVisible()
    expect(board.records.filter(record => record.element?.type === 'rectangle')).toHaveLength(1)
    // Touch over iframe content must select/move its Board placement instead
    // of delivering a gesture into the inert preview document.
    await page.keyboard.press('v')
    const pinBox = (await frame.boundingBox())!
    await drag(pinBox.x + pinBox.width / 2, pinBox.y + 35, 55, 45)
    await expect(page.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: 'Save board', exact: true }).tap()
    await expect(page.getByText('Board · Saved sequence 2')).toBeVisible()
    expect(batches[1].placement_updates).toEqual(expect.arrayContaining([expect.objectContaining({ id: 'touch-pin' })]))
    expect(board.records).toHaveLength(1)
    const libraryAttemptStart = outsideObserved.length
    await page.locator('.sidebar-trigger.default-sidebar-trigger').tap()
    await expect(page.getByText('Crewship library', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Person', exact: true }).tap()
    await expect(page.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: 'Save board', exact: true }).tap()
    await expect(page.getByText('Board · Saved sequence 3')).toBeVisible()
    expect(board.records).toHaveLength(6)
    expect(outsideObserved.slice(libraryAttemptStart)).toEqual([])
    expect(outsideFinished).toEqual([])
    expect(outsideFailures).toHaveLength(outsideObserved.length)
    for (const failure of outsideFailures) {
      expect(failure.url).toMatch(/^https:\/\/esm\.sh\/@excalidraw\/excalidraw@0\.18\.1\/dist\/prod\/fonts\/[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+\.woff2$/)
      expect(failure.error).toMatch(/csp|content.security.policy|NS_ERROR_CONTENT_BLOCKED/i)
    }
    await test.info().attach('touch-offline-network-evidence', { contentType: 'application/json', body: JSON.stringify({ outsideObserved, outsideFailures, outsideFinished, limitation: 'External HTTPS is deliberately blocked. Failed upstream font fallback attempts are recorded, not claimed absent. Static iframe pointer interaction is disabled; no runtime activation or production authentication is proved.' }, null, 2) })
  } finally { await context.close(); await fixture.close() }
})
