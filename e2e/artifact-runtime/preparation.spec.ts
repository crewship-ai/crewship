import { test, expect } from '@playwright/test'
import { artifactRuntimePreparationCSP, artifactRuntimePreparationDocument } from '../../lib/pages/artifact-runtime-bootstrap'
import { startFixture, studio, runtime, sink } from './fixture'

test('prepared bootstrap retains response CSP after one HTML transfer', async ({ browser }) => {
  const fixture = await startFixture()
  fixture.documents.set('/prepared', artifactRuntimePreparationDocument(studio))
  const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
  try {
    // Apply the candidate constant response-header policy, never a meta policy.
    await context.route(`${runtime}/prepared*`, async route => {
      const response = await route.fetch()
      await route.fulfill({ response, headers: { ...response.headers(), 'content-security-policy': artifactRuntimePreparationCSP(studio) } })
    })
    const page = await context.newPage()
    await page.goto(studio)
    await page.evaluate(({ runtime, sink }) => {
      const nonce = crypto.randomUUID()
      const frame = document.createElement('iframe')
      frame.sandbox.add('allow-scripts')
      frame.setAttribute('credentialless', '')
      frame.title = 'Prepared runtime'
      const win = window as unknown as { telemetry: unknown[] }
      win.telemetry = []
      const ready = (event: MessageEvent) => {
        if (event.source !== frame.contentWindow || event.origin !== 'null' || event.data?.type !== 'artifact.ready' || event.data?.nonce !== nonce) return
        removeEventListener('message', ready)
        const channel = new MessageChannel()
        channel.port1.onmessage = event => win.telemetry.push(event.data)
        frame.contentWindow!.postMessage({ type: 'artifact.connect', nonce }, '*', [channel.port2])
        channel.port1.postMessage({ type: 'load', protocol: 1, identity: { artifactId: 'exact-page', revision: 7, profile: 'interactive-html-v1' }, html: `<h1>Executed revision 7</h1><script>document.body.dataset.executed='yes';fetch('${sink}/fetch').catch(()=>{});try{parent.document.body.dataset.compromised='yes'}catch{}<\/script><img src="${sink}/image"><iframe src="${sink}/frame"></iframe><style>@import '${sink}/style';</style>` })
      }
      addEventListener('message', ready)
      frame.src = `${runtime}/prepared#${nonce}`
      document.body.append(frame)
    }, { runtime, sink })
    await expect(page.frameLocator('iframe[title="Prepared runtime"]').getByRole('heading', { name: 'Executed revision 7' })).toBeVisible()
    await expect(page.frameLocator('iframe[title="Prepared runtime"]').locator('body')).toHaveAttribute('data-executed', 'yes')
    await expect.poll(() => page.evaluate(() => (window as unknown as { telemetry: { type: string }[] }).telemetry.some(item => item.type === 'ready'))).toBe(true)
    await page.waitForTimeout(300)
    expect(fixture.hits).toEqual([])
    expect(await page.locator('body').getAttribute('data-compromised')).toBeNull()
    expect(page.url()).toBe(`${studio}/`)
  } finally { await context.close(); await fixture.close() }
})
