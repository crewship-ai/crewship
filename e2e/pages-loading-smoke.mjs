// Read-only live acceptance: hold the bootstrap response to inspect the loading
// state and actual frame in desktop Chromium; WebKit must offer the panel fallback.
import { chromium, webkit } from 'playwright'
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const required = name => { if (!process.env[name]) throw new Error(`${name} is required`); return process.env[name] }
const base = required('PAGES_SMOKE_BASE_URL')
const email = required('PAGES_SMOKE_EMAIL')
const password = required('PAGES_SMOKE_PASSWORD')
const output = required('PAGES_SMOKE_OUTPUT')
await mkdir(output, { recursive: true })
for (const [name, engine] of Object.entries({ chromium, webkit })) {
 const browser = await engine.launch()
 try {
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 }, reducedMotion: 'no-preference' })
  let page = await context.newPage()
  const errors = []
  await page.goto(base + '/login')
  await page.locator('#email').fill(email)
  try { await page.locator('#password').fill(password) } catch { throw new Error('Login password field unavailable') }
  await page.locator('button[type=submit]').click()
  await page.waitForURL(url => !url.pathname.startsWith('/login'))
  // Use a fresh authenticated tab: WebKit reports cancelled dashboard fetches
  // as page errors when navigation tears down the post-login dashboard.
  const loginPage = page
  page = await context.newPage()
  await loginPage.close()
  await page.bringToFront()
  page.on('pageerror', e => errors.push(e.message))
  let release
  const gate = new Promise(resolve => { release = resolve })
  await page.route('**/api/v1/pages/runtime/bootstrap', async route => { await gate; await route.continue() })
  await page.goto(base + '/pages/custom-operations', { waitUntil: 'domcontentloaded' })
  if (name === 'webkit') {
   await page.getByText(/Custom Page applications require desktop Chrome or Edge/).waitFor()
   assert.equal(await page.locator('iframe[title="Page application"]').count(), 0)
   await page.screenshot({ path: `${output}/${name}-panel-fallback.png` })
   assert.deepEqual(errors, [])
   console.log(JSON.stringify({ browser: name, unsupportedEngineBlocked: true, errors }))
   continue
  }
  const iframe = page.locator('iframe[title="Page application"]')
  await iframe.waitFor({ state: 'attached' })
  assert.equal(await iframe.evaluate(el => getComputedStyle(el).opacity), '0')
  assert.equal(await iframe.getAttribute('aria-hidden'), 'true')
  assert.notEqual(await iframe.evaluate(el => getComputedStyle(el.parentElement).backgroundColor), 'rgb(255, 255, 255)')
  await page.getByRole('status').filter({ hasText: 'Loading application' }).waitFor()
  await page.screenshot({ path: `${output}/${name}-loading.png` })
  release()
  const frame = page.frameLocator('iframe[title="Page application"]')
  await frame.getByRole('heading', { name: 'Provoz pod kontrolou.' }).waitFor()
  await page.waitForFunction(() => document.querySelector('iframe[title="Page application"]')?.getAttribute('aria-hidden') === 'false')
  await page.waitForFunction(() => {
   const frame = document.querySelector('iframe[title="Page application"]')
   return frame && getComputedStyle(frame).opacity === '1'
  })
  assert.equal(await iframe.evaluate(el => getComputedStyle(el).opacity), '1')
  assert.equal(await iframe.evaluate(el => getComputedStyle(el).transitionDuration), '0.3s')
  await page.screenshot({ path: `${output}/${name}-desktop.png` })
  for (const width of [900, 390]) {
   await page.setViewportSize({ width, height: 950 })
   await page.waitForTimeout(250)
   await page.screenshot({ path: `${output}/${name}-${width}.png` })
   const dimensions = await frame.locator('html').evaluate(el => ({ width: el.clientWidth, scroll: el.scrollWidth, overflow: [...el.querySelectorAll('*')].filter(child => child.getBoundingClientRect().right > el.clientWidth + 1).slice(0, 12).map(child => ({ tag: child.tagName, class: child.className, right: child.getBoundingClientRect().right })) }))
   assert.ok(dimensions.scroll <= dimensions.width + 1, `${name}: Page overflow at ${width}: ${JSON.stringify(dimensions)}`)
   if (width === 390) {
    const title = await page.getByRole('heading', { name: 'Pages', exact: true }).boundingBox()
    const settings = await page.getByRole('button', { name: 'Settings', exact: true }).boundingBox()
    assert.ok(title && settings && title.x + title.width <= settings.x, 'mobile header title does not overlap actions')
   }
   await page.screenshot({ path: `${output}/${name}-${width}.png` })
  }
  await frame.getByRole('button', { name: 'Služby', exact: true }).click()
  await frame.locator('tbody tr').first().waitFor()
  assert.ok(await frame.locator('html').evaluate(el => el.scrollWidth <= el.clientWidth + 1), `${name}: services fit mobile`)
  await frame.getByRole('button', { name: 'Načíst historii', exact: true }).first().click()
  await frame.locator('tbody details').first().waitFor()
  await frame.locator('tbody summary').first().click()
  assert.ok(await frame.locator('html').evaluate(el => el.scrollWidth <= el.clientWidth + 1), `${name}: expanded history fits mobile`)
  await page.screenshot({ path: `${output}/${name}-mobile-history.png` })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  assert.equal(await iframe.evaluate(el => getComputedStyle(el).transitionProperty), 'none')
  assert.deepEqual(errors, [])
  console.log(JSON.stringify({ browser: name, delayedBootstrapConcealed: true, renderedFade: true, narrowDesktopViewport: true, reducedMotion: true, errors }))
 } finally { await browser.close() }
}
