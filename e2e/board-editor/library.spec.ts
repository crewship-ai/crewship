import { writeFile } from 'node:fs/promises'
import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'
import { BOARD_LIBRARY } from '../../lib/boards/library'

test('local library inserts editable fresh-ID groups without mounting external library controls', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  try {
    await page.goto(`${fixture.url}cursors.html`)
    await expect.poll(() => page.evaluate(() => window.boardCursors?.ready())).toBe(true)
    await page.locator('.sidebar-trigger.default-sidebar-trigger').click()
    await expect(page.getByText('Crewship library', { exact: true })).toBeVisible()
    await expect(page.getByText('Browse libraries', { exact: true })).toHaveCount(0)
    for (const item of BOARD_LIBRARY) {
      const before = await page.evaluate(() => window.boardCursors.scene().length)
      await page.getByRole('button', { name: item.name, exact: true }).click()
      await expect.poll(() => page.evaluate(() => window.boardCursors.scene().length)).toBe(before + item.elements.length)
    }
    // Re-inserting a template generates independent drawing and group IDs.
    const before = await page.evaluate(() => window.boardCursors.scene().length)
    await page.getByRole('button', { name: 'Person', exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.boardCursors.scene().length)).toBe(before + 5)
    const scene = await page.evaluate(() => window.boardCursors.scene())
    expect(new Set(scene.map(item => item.id)).size).toBe(scene.length)
    const groups = new Set(scene.flatMap(item => item.groupIds))
    expect(groups.size).toBe(BOARD_LIBRARY.length + 1)
    expect(scene.every(item => !item.link && !item.customData && !['image', 'embeddable', 'iframe'].includes(item.type))).toBe(true)
    if (process.env.CREWSHIP_CAPTURE_LIBRARY) await writeFile(process.env.CREWSHIP_CAPTURE_LIBRARY, JSON.stringify(scene, null, 2))
  } finally { await fixture.close() }
})
