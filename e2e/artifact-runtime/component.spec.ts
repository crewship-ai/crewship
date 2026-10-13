import { test, expect } from '@playwright/test'
import { startComponentFixture } from './component-fixture'

// Actual production component/channel/bootstrap; simulated host lease only.
// This is not product authorization, domain admission or runtime activation.
test('actual runtime component stops, restarts and replaces the exact revision', async ({ browser }) => {
  const lab = await startComponentFixture(browser)
  const { page } = lab
  try {
    const frame = page.frameLocator('iframe[title="Interactive artifact preparation"]')
    await expect(frame.getByRole('heading', { name: 'Exact revision 1' })).toBeVisible()
    await expect(page.getByRole('status')).toHaveText('Running')
    const aborted = await page.evaluate(() => window.runtimeComponent.aborted)
    await page.getByRole('button', { name: 'Stop', exact: true }).click()
    await expect(page.locator('iframe')).toHaveCount(0)
    await expect(page.getByRole('status')).toHaveText('Stopped')
    expect(await page.evaluate(() => window.runtimeComponent.aborted)).toBeGreaterThan(aborted)
    await page.getByRole('button', { name: 'Restart', exact: true }).click()
    await expect(frame.getByRole('heading', { name: 'Exact revision 1' })).toBeVisible()
    const previous = await page.locator('iframe').getAttribute('src')
    await page.getByRole('button', { name: 'Load revision 2', exact: true }).click()
    await expect(frame.getByRole('heading', { name: 'Exact revision 2' })).toBeVisible()
    await expect(frame.getByRole('heading', { name: 'Exact revision 1' })).toHaveCount(0)
    expect(await page.locator('iframe').getAttribute('src')).not.toBe(previous)
    expect((await page.evaluate(() => window.runtimeComponent.leases)).at(-1)?.revision).toBe(2)
    await expect(page.locator('iframe')).toHaveCount(1)
    expect(lab.unexpected).toEqual([])
  } finally { await lab.close() }
})

for (const mode of ['denied', 'expired', 'hung'] as const) test(`actual runtime component removes the frame on ${mode} lease renewal`, async ({ browser }) => {
  const lab = await startComponentFixture(browser)
  try {
    const { page } = lab
    await expect(page.getByRole('status')).toHaveText('Running')
    await expect(page.locator('iframe')).toHaveCount(1)
    const aborted = await page.evaluate(() => window.runtimeComponent.aborted)
    await page.getByRole('button', { name: `Lease ${mode}`, exact: true }).click()
    await expect(page.locator('iframe')).toHaveCount(0)
    await expect(page.getByRole('status')).toHaveText(mode === 'hung' ? 'Access lease expired' : 'Access could not be verified')
    expect(await page.evaluate(() => window.runtimeComponent.aborted)).toBeGreaterThan(aborted)
    expect(lab.unexpected).toEqual([])
  } finally { await lab.close() }
})

test('actual runtime component host Stop remains responsive during an infinite iframe loop', async ({ browser }) => {
  const lab = await startComponentFixture(browser)
  try {
    const { page } = lab
    await expect(page.getByRole('status')).toHaveText('Running')
    const started = page.waitForEvent('console', { predicate: event => event.text() === 'component-loop-started' })
    await page.getByRole('button', { name: 'Run infinite artifact', exact: true }).click()
    await started
    const ticks = await page.evaluate(() => window.runtimeComponent.ticks)
    await expect.poll(() => page.evaluate(() => window.runtimeComponent.ticks)).toBeGreaterThan(ticks + 5)
    await page.getByRole('button', { name: 'Stop', exact: true }).click({ timeout: 2000 })
    await expect(page.locator('iframe')).toHaveCount(0)
    await expect(page.getByRole('status')).toHaveText('Stopped')
    const after = await page.evaluate(() => window.runtimeComponent.ticks)
    await expect.poll(() => page.evaluate(() => window.runtimeComponent.ticks)).toBeGreaterThan(after + 5)
    await page.getByRole('button', { name: 'Load revision 2', exact: true }).click()
    await page.getByRole('button', { name: 'Restart', exact: true }).click()
    await expect(page.frameLocator('iframe').getByRole('heading', { name: 'Exact revision 2' })).toBeVisible()
    expect(lab.unexpected).toEqual([])
  } finally { await lab.close() }
})
