import { test, expect } from '@playwright/test'
import { resolve } from 'node:path'
import { startBoardFixture } from './fixture'

test('real editor export creates a bounded PNG of the saved selection without exporting pin content', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  try {
    await page.goto(`${fixture.url}cursors.html`)
    await expect.poll(() => page.evaluate(() => window.boardCursors?.ready())).toBe(true)
    const result = await page.evaluate(async moduleURL => {
      const { renderSelectionPreview } = await import(/* @vite-ignore */ moduleURL)
      const scene = window.boardCursors.scene()
      const board = { format: 'board/v1', seq: 7, can_write: true,
        records: scene.map(element => ({ element_id: element.id, element_rev: 1, deleted: false, element })),
        placements: [{ id: 'pin', x: 150, y: 30, width: 200, height: 100, source: { page_slug: 'private-source', revision: 1, title: 'Secret pin' } }],
      }
      const png = await renderSelectionPreview(board, [scene[0].id, 'board-placement:pin'], [], new AbortController().signal)
      const blob = new Blob([Uint8Array.from(atob(png), char => char.charCodeAt(0))], { type: 'image/png' })
      const bitmap = await createImageBitmap(blob)
      const size = { width: bitmap.width, height: bitmap.height }; bitmap.close()
      const bytes = new Uint8Array(await blob.arrayBuffer())
      return { ...size, bytes: blob.size, signature: [...bytes.slice(0, 8)], containsScene: new TextDecoder().decode(bytes).includes('excalidraw') }
    }, `/@fs/${resolve('lib/boards/selection-preview.ts')}`)
    expect(result.signature).toEqual([137, 80, 78, 71, 13, 10, 26, 10])
    expect(result.width).toBeGreaterThan(0); expect(result.height).toBeGreaterThan(0)
    expect(Math.max(result.width, result.height)).toBeLessThanOrEqual(1024)
    expect(result.bytes).toBeLessThanOrEqual(1024 * 1024)
    expect(result.containsScene).toBe(false)
  } finally { await fixture.close() }
})
