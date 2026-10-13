import { FixtureResources } from './resources'
import { test, expect } from '@playwright/test'
import { startGoArtifactBootstrap, artifactPreparationPath } from './go-bootstrap'
import { startFixture, studio, runtime, sink } from './fixture'

// CI retains the exact browser operations when a bootstrap phase stalls.
test.use({ trace: 'retain-on-failure' })

test('prepared bootstrap retains response CSP after one HTML transfer', async ({ browser }) => {
  const resources = new FixtureResources()
  let tearingDown = false
  // A timed-out setup step may still finish its acquisition later. Register
  // before returning, or close late arrivals under the same cleanup bound.
  const owned = async <T extends { close: () => Promise<unknown> }>(acquire: () => Promise<T>): Promise<T> => {
    const resource = await acquire()
    if (tearingDown) {
      const late = new FixtureResources()
      late.own(() => resource.close())
      await late.close()
      throw new Error('Runtime fixture acquisition completed after teardown')
    }
    resources.own(() => resource.close())
    return resource
  }
  try {
    const fixture = await test.step('start owned HTTPS/proxy fixture', () => owned(startFixture), { timeout: 5000 })
    // CI precompiles this Go binary; this phase never compiles or downloads it.
    const bootstrap = await test.step('wait for precompiled Go bootstrap readiness', () => owned(startGoArtifactBootstrap), { timeout: 6000 })
    const context = await test.step('create browser context with owned proxy', () => owned(() => browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })), { timeout: 5000 })
    await context.route(`${runtime}${artifactPreparationPath}*`, async route => {
      await route.fulfill(await bootstrap.response())
    })
    const page = await test.step('create Studio page', () => context.newPage(), { timeout: 5000 })
    await test.step('load HTTPS Studio through proxy', () => page.goto(studio), { timeout: 8000 })
    await test.step('install opaque-frame handshake and transfer exact HTML', () => page.evaluate(({ runtime, sink, artifactPreparationPath }) => {
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
      frame.src = `${runtime}${artifactPreparationPath}#${nonce}`
      document.body.append(frame)
    }, { runtime, sink, artifactPreparationPath }), { timeout: 5000 })
    await test.step('verify execution, telemetry, host isolation and blocked resource sinks', async () => {
      await expect(page.frameLocator('iframe[title="Prepared runtime"]').getByRole('heading', { name: 'Executed revision 7' })).toBeVisible()
      await expect(page.frameLocator('iframe[title="Prepared runtime"]').locator('body')).toHaveAttribute('data-executed', 'yes')
      await expect.poll(() => page.evaluate(() => (window as unknown as { telemetry: { type: string }[] }).telemetry.some(item => item.type === 'ready'))).toBe(true)
      await page.waitForTimeout(300)
      expect(fixture.hits).toEqual([])
      expect(await page.locator('body').getAttribute('data-compromised')).toBeNull()
      expect(page.url()).toBe(`${studio}/`)
    }, { timeout: 12000 })
  } finally { tearingDown = true; await resources.close() }
})
