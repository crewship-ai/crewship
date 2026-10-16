import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'
test('production follow cursor pans only after explicit selection and stops on interaction or disconnect without geometry writes', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  try {
    await page.goto(`${fixture.url}cursors.html`)
    await expect.poll(() => page.evaluate(() => window.boardCursors?.ready())).toBe(true)
    const scene = await page.evaluate(() => JSON.stringify(window.boardCursors.scene()))
    await page.evaluate(() => window.boardCursors.show())
    const select = page.getByLabel('Follow a live viewer cursor')
    await expect(select).toHaveValue('')
    await select.selectOption('verified-peer')
    await expect(page.getByRole('status')).toContainText('Following Fixture teammate')
    await expect.poll(() => page.evaluate(() => window.boardCursors.viewport())).not.toBeNull()
    const first = (await page.evaluate(() => window.boardCursors.viewport()))!
    await page.evaluate(() => window.boardCursors.move(400, 300))
    await expect.poll(() => page.evaluate(() => window.boardCursors.viewport()?.x)).not.toBe(first.x)
    await page.locator('canvas.interactive').click({ position: { x: 650, y: 250 } })
    await expect(select).toHaveValue('')
    const stopped = await page.evaluate(() => window.boardCursors.viewport())
    await page.evaluate(() => window.boardCursors.move(700, 600))
    await expect.poll(() => page.evaluate(() => window.boardCursors.viewport())).toEqual(stopped)
    await select.selectOption('verified-peer'); await page.keyboard.press('Escape'); await expect(select).toHaveValue('')
    await select.selectOption('verified-peer'); await page.evaluate(() => window.boardCursors.disconnect()); await expect(select).toHaveValue('')
    expect(await page.evaluate(() => JSON.stringify(window.boardCursors.scene()))).toBe(scene)
  } finally { await fixture.close() }
})
