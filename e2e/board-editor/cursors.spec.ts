import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'

test('production editor renders collaborator pointers without changing drawing geometry', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  try {
    await page.goto(`${fixture.url}cursors.html`)
    await expect.poll(() => page.evaluate(() => window.boardCursors?.ready())).toBe(true)
    const canvas = page.locator('canvas.interactive')
    await expect(canvas).toBeVisible()
    const beforeScene = await page.evaluate(() => window.boardCursors.scene())
    const before = await canvas.screenshot()
    await page.evaluate(() => window.boardCursors.show())
    await expect.poll(async () => (await canvas.screenshot()).equals(before)).toBe(false)
    expect(await page.evaluate(() => window.boardCursors.scene())).toEqual(beforeScene)
  } finally { await fixture.close() }
})
