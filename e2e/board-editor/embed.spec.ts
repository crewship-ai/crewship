import { test, expect, type Request } from '@playwright/test'
import { startBoardFixture } from './fixture'

type EmbedAPI = {
  scene: () => { id: string; x: number; y: number; width: number; height: number }[]
  select: (ids: string[]) => void
  selected: () => string[]
  exportSelection: () => Promise<{ svg: string; blobType: string; blobBytes: number; selectedCount: number }>
}
declare global { interface Window { boardEmbed: EmbedAPI } }

test('safe artifact frames export placeholders and host CSP blocks known font fallback attempts', async ({ page }, testInfo) => {
  const fixture = await startBoardFixture()
  const requests = new Map<Request, { url: string; outside: boolean; failure: string | null; finished: boolean }>()
  const routedOutside: string[] = []
  const externalSockets: string[] = []
  const outside = (url: string) => !url.startsWith('data:') && !url.startsWith('blob:') && new URL(url).origin !== new URL(fixture.url).origin
  page.on('websocket', socket => {
    if (new URL(socket.url()).host !== new URL(fixture.url).host) externalSockets.push(socket.url())
  })
  page.on('request', request => requests.set(request, { url: request.url(), outside: outside(request.url()), failure: null, finished: false }))
  page.on('requestfailed', request => {
    const record = requests.get(request)
    if (record) record.failure = request.failure()?.errorText ?? 'unknown failure'
  })
  page.on('requestfinished', request => {
    const record = requests.get(request)
    if (record) record.finished = true
  })
  // CSP-blocked attempts are observable, but must never reach this boundary.
  // Abort unexpected external traffic rather than depending on public DNS.
  await page.route('**/*', route => {
    if (outside(route.request().url())) {
      routedOutside.push(route.request().url())
      return route.abort()
    }
    return route.continue()
  })
  try {
    await page.goto(new URL("embed.html", fixture.url).href)
    await page.waitForFunction(() => !!window.boardEmbed)
    const first = page.frameLocator('iframe[title="Artifact first"]')
    const second = page.frameLocator('iframe[title="Artifact second"]')
    await expect(first.getByRole('heading', { name: 'Shared source secret marker' })).toBeVisible()
    await expect(second.getByRole('heading', { name: 'Shared source secret marker' })).toBeVisible()
    await expect(first.locator('script,[src],[href]')).toHaveCount(0)
    await expect(page.locator('iframe[src]')).toHaveCount(0)
    await expect(page.locator('iframe[title="Artifact external"]')).toHaveCount(0)
    await expect(page.locator('iframe')).toHaveCount(2)
    await expect(page.locator('iframe[title="Artifact first"]')).toHaveAttribute('sandbox', '')
    await page.evaluate(() => window.boardEmbed.select(['first', 'second']))
    await expect.poll(() => page.evaluate(() => window.boardEmbed.selected().sort())).toEqual(['first', 'second'])
    const exported = await page.evaluate(() => window.boardEmbed.exportSelection())
    expect(exported.selectedCount).toBe(2)
    expect(exported.svg).toContain('Artifact preview')
    expect(exported.svg).not.toMatch(/Shared source secret marker|blocked\.example|artifact\.invalid|<iframe/i)
    expect(exported.blobType).toBe('image/png')
    expect(exported.blobBytes).toBeGreaterThan(100)
    // A real pointer drag on a selected frame moves Board geometry while both
    // placements still display the unchanged shared source.
    await page.evaluate(() => window.boardEmbed.select(['first']))
    const before = await page.evaluate(() => window.boardEmbed.scene().find(element => element.id === 'first')!)
    const bounds = await page.locator('iframe[title="Artifact first"]').boundingBox()
    expect(bounds).not.toBeNull()
    await page.mouse.move(bounds!.x + bounds!.width / 2, bounds!.y + 10)
    await page.mouse.down()
    await page.mouse.move(bounds!.x + bounds!.width / 2 + 50, bounds!.y + 40, { steps: 8 })
    await page.mouse.up()
    await expect.poll(() => page.evaluate(() => window.boardEmbed.scene().find(element => element.id === 'first')!.x)).not.toBe(before.x)
    await expect(first.getByRole('heading', { name: 'Shared source secret marker' })).toBeVisible()
    await expect(second.getByRole('heading', { name: 'Shared source secret marker' })).toBeVisible()
    await page.waitForTimeout(300)
    const observedOutside = [...requests.values()].filter(record => record.outside)
    await testInfo.attach('board-embed-evidence', { body: JSON.stringify({
      editorVersion: '0.18.1', staticPlacements: 2, exportedSelectionCount: exported.selectedCount,
      pngBytes: exported.blobBytes, svgBytes: exported.svg.length,
      knownLimitation: 'Upstream font definitions include esm.sh fallback URLs; host CSP blocks these observable attempts before routing. This is not zero attempted egress.',
      requests: [...requests.values()], routedOutside, externalSockets,
    }, null, 2), contentType: 'application/json' })
    expect(routedOutside).toEqual([])
    expect(externalSockets).toEqual([])
    // Keep the observed upstream limitation explicit instead of hiding attempts.
    if (testInfo.project.name === "chromium") expect(observedOutside.length).toBeGreaterThan(0)
    for (const record of observedOutside) {
      expect(record.url).toMatch(/^https:\/\/esm\.sh\/@excalidraw\/excalidraw@0\.18\.1\/dist\/prod\/fonts\/[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+\.woff2$/)
      expect(record.finished).toBe(false)
      expect(record.failure).toMatch(/csp|content.security.policy|NS_ERROR_CONTENT_BLOCKED/i)
    }

  } finally { await page.close(); await fixture.close() }
})
