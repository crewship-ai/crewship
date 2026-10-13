import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { transpileModule, ModuleKind, ScriptTarget } from 'typescript'
import { startFixture, studio, sink } from './fixture'

// Compile the production sanitizer unchanged except its package binding, which
// comes from the installed DOMPurify browser build in this independent fixture.
const require = createRequire(import.meta.url)
const sanitizer = transpileModule(
  readFileSync('lib/pages/artifact-static.ts', 'utf8')
    .replace('import DOMPurify from "dompurify"', 'const DOMPurify = window.DOMPurify')
    .replace('export function', 'function') + '\nwindow.staticArtifactDocument = staticArtifactDocument',
  { compilerOptions: { module: ModuleKind.None, target: ScriptTarget.ES2020 } },
).outputText

test('production static preview strips navigation and blocks resource traffic', async ({ browser }) => {
  const fixture = await startFixture()
  const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
  try {
    const page = await context.newPage()
    await page.goto(`${sink}/control`)
    expect(fixture.hits).toContain('/control')
    await page.goto(studio)
    fixture.hits.length = 0
    await page.addScriptTag({ path: require.resolve('dompurify/dist/purify.js') })
    await page.addScriptTag({ content: sanitizer })
    const hostile = `<meta http-equiv="refresh" content="0;url=${sink}/refresh"><base href="${sink}"><link rel="preconnect" href="${sink}"><script>fetch('${sink}/script')</script><svg><foreignObject><iframe src="${sink}/svg"></iframe></foreignObject></svg><form action="${sink}/form"><input name="secret"></form><a href="${sink}/anchor" ping="${sink}/ping">Navigation disabled</a><img src="${sink}/image"><style>@import url('${sink}/css');h1{color:rgb(12,34,56)}body{background-image:url('${sink}/background')}</style><h1 style="font-size:28px">Static report</h1><table><tr><td>42</td></tr></table>`
    await page.evaluate(html => {
      const win = window as unknown as { DOMPurify: { isSupported: boolean }; staticArtifactDocument: (html: string) => string }
      if (!win.DOMPurify.isSupported) throw new Error('Real browser sanitizer unexpectedly unsupported')
      const iframe = document.createElement('iframe')
      iframe.title = 'Static preview'; iframe.setAttribute('sandbox', '')
      iframe.srcdoc = win.staticArtifactDocument(html); document.body.append(iframe)
    }, hostile)
    const frame = page.frameLocator('iframe')
    await expect(frame.getByRole('heading', { name: 'Static report' })).toBeVisible()
    await expect(frame.getByRole('heading')).toHaveCSS('font-size', '28px')
    await expect(frame.locator('script,meta[http-equiv="refresh"],base,form,svg,iframe')).toHaveCount(0)
    await expect(frame.locator('[href],[src],[ping],[onclick]')).toHaveCount(0)
    await frame.getByText('Navigation disabled').click()
    await page.waitForTimeout(700)
    expect(fixture.hits).toEqual([])
    expect(page.url()).toBe(`${studio}/`)
    expect(context.pages()).toHaveLength(1)
  } finally { await context.close(); await fixture.close() }
})
