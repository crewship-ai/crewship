import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'

test('gallery renders one sanitized on-demand preview and removes it on denied refresh', async ({ page }) => {
  const fixture = await startBoardFixture({ product: true })
  let denied = false
  const requests: string[] = []
  try {
    // Explicit fixture API: tests the production components and real sanitizer,
    // not authentication or the server's authorization implementation.
    await page.route('**/api/v1/pages/*/artifact?*', async route => {
      requests.push(new URL(route.request().url()).pathname)
      if (denied) return route.fulfill({ status: 403, json: { error: 'Access revoked' } })
      return route.fulfill({ json: { revision: 1, head_rev: 1, revisions: [], can_write: false, html: '<h1>Safe report</h1><script>document.body.textContent="SCRIPT EXECUTED"</script><a href="https://sink.invalid">external</a><iframe src="https://sink.invalid"></iframe>' } })
    })
    await page.goto(`${fixture.url}gallery.html`)
    await expect(page.getByRole('heading', { name: 'Artifacts', exact: true })).toBeVisible()
    expect(requests).toHaveLength(0)
    await page.getByRole('button', { name: 'Preview of one', exact: true }).click()
    const iframe = page.locator('iframe[title="Static preview of one"]')
    await expect(iframe).toHaveAttribute('sandbox', '')
    await expect(iframe.contentFrame().getByRole('heading', { name: 'Safe report' })).toBeVisible()
    await expect(iframe.contentFrame().locator('script, iframe, [href]')).toHaveCount(0)
    await page.getByRole('button', { name: 'Preview of two', exact: true }).click()
    await expect(page.locator('iframe')).toHaveCount(1)
    await expect(page.locator('iframe')).toHaveAttribute('title', 'Static preview of two')
    denied = true
    await page.evaluate(() => window.artifactGallery.refresh())
    await expect(page.getByRole('alert')).toContainText('Preview unavailable')
    await expect(page.locator('iframe')).toHaveCount(0)
  } finally { await fixture.close() }
})
