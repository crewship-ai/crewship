import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'

declare global { interface Window { mermaidGate: { elements: () => { id: string; type: string; text?: string }[] } } }

test('pinned Mermaid converter prepares editable shapes and waits for explicit insertion', async ({ page }) => {
  const fixture = await startBoardFixture()
  const attempts: string[] = []
  page.on('request', request => {
    if (!request.url().startsWith(fixture.url) && !/^(data|blob):/.test(request.url())) attempts.push(request.url())
  })
  await page.route('https://**/*', route => route.abort())
  try {
    await page.goto(new URL('mermaid.html', fixture.url).href)
    await page.waitForFunction(() => !!window.mermaidGate)
    await page.getByRole('button', { name: 'Prepare diagram', exact: true }).click()
    await expect(page.locator('output')).toHaveText('Ready to insert')
    expect(await page.evaluate(() => window.mermaidGate.elements().length)).toBe(0)
    await page.getByRole('button', { name: 'Insert diagram', exact: true }).click()
    await expect(page.locator('output')).toHaveText('Inserted')
    const elements = await page.evaluate(() => window.mermaidGate.elements())
    expect(elements.filter(element => element.type === 'arrow')).toHaveLength(2)
    expect(elements.filter(element => element.type === 'text').map(element => element.text).sort()).toEqual(['Build', 'Plan', 'Review'])
    await expect(page.getByRole('button', { name: 'Insert diagram', exact: true })).toBeDisabled()
    expect(new Set(elements.map(element => element.id)).size).toBe(elements.length)
    await test.info().attach('converted-elements', { body: JSON.stringify(elements, null, 2), contentType: 'application/json' })
    expect(attempts).toEqual([])
  } finally { await page.close(); await fixture.close() }
})
