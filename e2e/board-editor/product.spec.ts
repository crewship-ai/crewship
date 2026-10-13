import { test, expect } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { startBoardFixture } from './fixture'
import type { PageBoard } from '../../hooks/use-page-board'

test('production Board manually saves real drawing, preserves uncertain identity and conflicts, and removes denied content', async ({ page, browserName }) => {
  const fixture = await startBoardFixture({ product: true })
  let board: PageBoard = { format: 'crewship-board-excalidraw-0.18.1-v1', seq: 0, can_write: true, records: [], placements: [] }
  let denied = false
  let lost = false
  let pinnedDenied = false
  let imageBytes: Buffer | null = null
  const jobs: Record<string, unknown>[] = []
  const batches: Record<string, unknown>[] = []
  const committed = new Set<string>()
  const converterOrigin = 'https://board-converter.crewship-test.net'
  const converterUrl = `${converterOrigin}/api/v1/pages/runtime/board-converter`
  try {
    const runner = await readFile('e2e/board-editor/public/board-converter/runner.js')
    await page.route(`${converterOrigin}/**`, async route => {
      expect(route.request().headers()['authorization']).toBeUndefined()
      expect(route.request().headers()['cookie']).toBeUndefined()
      if (new URL(route.request().url()).pathname === '/board-converter/runner.js') return route.fulfill({ contentType: 'application/javascript; charset=utf-8', headers: { 'Access-Control-Allow-Origin': '*', 'Cross-Origin-Resource-Policy': 'cross-origin' }, body: runner })
      const parentOrigin = new URL(fixture.url).origin
      return route.fulfill({ contentType: 'text/html; charset=utf-8', headers: { 'Content-Security-Policy': "default-src 'none'; script-src 'nonce-fixture'; style-src 'unsafe-inline'; font-src data:; img-src data:; connect-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'" }, body: `<!doctype html><html><head><meta charset="utf-8"></head><body><script nonce="fixture">window.__CREWSHIP_BOARD_CONVERTER_PARENT__=${JSON.stringify(parentOrigin)}</script><script crossorigin="anonymous" nonce="fixture" src="${converterOrigin}/board-converter/runner.js"></script></body></html>` })
    })
    // Explicit test-only API model. Does not establish production authentication,
    // database atomicity, sharing enforcement or websocket delivery.
    await page.route('**/api/v1/**', async route => {
      if (new URL(route.request().url()).origin === converterOrigin) return route.fallback()
      const request = route.request()
      const path = new URL(request.url()).pathname
      if (path.endsWith('/board/threads')) return route.fulfill({ json: { threads: [], can_comment: true, can_resolve: true } })
      if (path.endsWith('/board/files') && request.method() === 'POST') {
        imageBytes = request.postDataBuffer()
        board = { ...board, seq: board.seq + 1 }
        return route.fulfill({ json: { file_id: 'fixture_image', content_type: 'image/png', width: 40, height: 30, bytes: imageBytes!.length, seq: board.seq, result_seq: board.seq } })
      }
      if (path.endsWith('/board/files/fixture_image')) return route.fulfill({ contentType: 'image/png', body: imageBytes! })
      if (path.endsWith('/board/requests')) {
        if (request.method() === 'POST') {
          const body = request.postDataJSON()
          jobs.push({ id: 'job', kind: 'diagram', status: 'ready_to_insert', base_seq: body.base_seq, instruction: body.instruction, target: body.target, agent_id: 'agent', agent_name: 'Fixture agent', can_insert: true, can_cancel: false, dialect: 'flowchart-v1', converter_version: '2.2.2', source_hash: 'fixture_digest' })
          return route.fulfill({ status: 202, json: { id: 'job' } })
        }
        return route.fulfill({ json: { converter_url: converterUrl, can_dispatch: true, eligible_chats: [{ chat_id: 'private', title: 'Fixture private chat', agent_name: 'Fixture agent' }], requests: jobs } })
      }
      if (path.endsWith('/board/requests/job')) return route.fulfill({ json: { ...jobs[0], mermaid_source: 'flowchart TD\nA[Plan] --> B[Build]', inserted_element_ids: [], edited_since_insert: false } })
      if (path.endsWith('/board/requests/job/insert')) {
        const body = request.postDataJSON()
        expect(body.base_seq).toBe(board.seq)
        expect(body.source_hash).toBe('fixture_digest')
        expect(body.upserts.length).toBeGreaterThan(2)
        expect(body.upserts.every((item: {expected_rev:number;element:{type:string;link?:unknown}}) => item.expected_rev === 0 && ['rectangle','text','arrow'].includes(item.element.type) && item.element.link == null)).toBe(true)
        board = { ...board, seq: board.seq + 1, records: [...board.records, ...body.upserts.map((item: {element:{id:string}}) => ({ element_id: item.element.id, element_rev: 1, deleted: false, element: item.element }))] }
        jobs[0] = { ...jobs[0], status: 'inserted', can_insert: false, result_seq: board.seq }
        return route.fulfill({ json: { seq: board.seq, result_seq: board.seq, element_revs: Object.fromEntries(board.records.map(item => [item.element_id, item.element_rev])), undo_expires_at: new Date(Date.now() + 86400000).toISOString() } })
      }
      if (path.endsWith('/board/batches')) {
        const body = request.postDataJSON(); batches.push(body)
        if (!committed.has(body.mutation_id)) {
          if (body.base_seq !== board.seq) return route.fulfill({ status: 409, json: { error: 'board changed; reload before saving' } })
          const records = new Map(board.records.map(record => [record.element_id, record]))
          for (const item of body.upserts as { element: Record<string, unknown> & { id: string }; expected_rev: number }[]) records.set(item.element.id, { element_id: item.element.id, element_rev: item.expected_rev + 1, element: item.element, deleted: false })
          for (const item of body.deletes as { id: string; expected_rev: number }[]) records.set(item.id, { element_id: item.id, element_rev: item.expected_rev + 1, element: null, deleted: true })
          board = { ...board, seq: board.seq + 1, records: [...records.values()] }
          committed.add(body.mutation_id)
        }
        if (lost) { lost = false; return route.abort('failed') }
        return route.fulfill({ json: { seq: board.seq, result_seq: board.seq, element_revs: Object.fromEntries(board.records.map(item => [item.element_id, item.element_rev])), undo_expires_at: new Date(Date.now() + 86400000).toISOString() } })
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
    await expect(page.getByText('Board · Saved sequence 2')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    expect(batches).toHaveLength(2)
    board = { ...board, seq: 3 }
    await page.getByRole('button', { name: 'Save board', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('board changed')
    expect(batches[2].base_seq).toBe(2)
    await page.getByRole('button', { name: 'Reload latest', exact: true }).click()
    await expect(page.getByRole('dialog')).toContainText('Discard unsaved drawing?')
    await page.getByRole('button', { name: 'Discard and reload', exact: true }).click()
    await expect(page.getByText('Board · Saved sequence 3')).toBeVisible()
    await page.getByRole('button', { name: 'Add image', exact: true }).click()
    const png = await page.evaluate(() => { const canvas = document.createElement('canvas'); canvas.width = 40; canvas.height = 30; const context = canvas.getContext('2d')!; context.fillStyle = 'red'; context.fillRect(0, 0, 40, 30); return canvas.toDataURL().split(',')[1] })
    await page.getByLabel('Image file', { exact: true }).setInputFiles({ name: 'private.png', mimeType: 'image/png', buffer: Buffer.from(png, 'base64') })
    await page.getByRole('button', { name: 'Upload and place', exact: true }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: 'Save board', exact: true }).click()
    await expect(page.getByText('Board · Saved sequence 5')).toBeVisible()
    expect(board.records.some(record => record.element?.type === 'image' && record.element.fileId === 'fixture_image')).toBe(true)
    expect(JSON.stringify(batches.at(-1))).not.toContain('data:image')
    await page.getByRole('button', { name: 'Generate diagram', exact: true }).click()
    await page.getByLabel('Private chat for board request').selectOption('private')
    await page.getByLabel('Board request instructions').fill('Create a plan and build diagram')
    await page.getByRole('button', { name: 'Send request', exact: true }).click()
    if (browserName === 'chromium') {
      await page.getByRole('button', { name: 'Insert diagram', exact: true }).click()
      await expect(page.getByText('Board · Saved sequence 6')).toBeVisible()
      expect(jobs[0].status).toBe('inserted')
      await expect(page.locator('iframe[title="Isolated diagram conversion"]')).toHaveCount(0)
    } else {
      await expect(page.getByRole('button', { name: 'Insert diagram', exact: true })).toBeDisabled()
      await expect(page.getByText(/Isolated insertion is unavailable/)).toBeVisible()
      await expect(page.locator('iframe[title="Isolated diagram conversion"]')).toHaveCount(0)
    }
    pinnedDenied = true
    board = { ...board, seq: 7, placements: [{ id: 'pin', x: 350, y: 180, width: 400, height: 250, source: { page_slug: 'report', revision: 1, title: 'Report' } }] }
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
