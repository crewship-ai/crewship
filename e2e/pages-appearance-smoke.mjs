// Explicitly targeted authenticated demo test. Temporarily changes only the
// workspace palette, restores it in finally, and never starts a build/routine.
import { chromium } from 'playwright'
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'

const required = name => { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value }
const base = required('PAGES_SMOKE_BASE_URL').replace(/\/$/, '')
const workspace = required('PAGES_SMOKE_WORKSPACE')
const slug = required('PAGES_SMOKE_SLUG')
const email = required('PAGES_SMOKE_EMAIL')
const password = required('PAGES_SMOKE_PASSWORD')
const output = process.env.PAGES_SMOKE_OUTPUT || '/tmp/pages-appearance-smoke'
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
let context, original, restoreNeeded = false
const endpoint = `${base}/api/v1/workspaces/${encodeURIComponent(workspace)}?workspace_id=${encodeURIComponent(workspace)}`
try {
 context = await browser.newContext({ viewport: { width: 1600, height: 1050 }, reducedMotion: 'no-preference' })
 const page = await context.newPage()
 const errors = []
 page.on('pageerror', error => errors.push(error.message))
 await page.goto(base + '/login')
 await page.locator('#email').fill(email)
 try { await page.locator('#password').fill(password) } catch { throw new Error('Unable to fill login password field') }
 await page.locator('button[type=submit]').click()
 await page.waitForURL(url => !url.pathname.startsWith('/login'))
 const current = await context.request.get(endpoint)
 assert.equal(current.status(), 200, 'read workspace settings')
 original = (await current.json()).pages_theme || {}
 await page.goto(`${base}/pages/${encodeURIComponent(slug)}`)
 const frame = page.frameLocator('iframe[title="Page application"]')
 await frame.locator('.stats article').first().waitFor()
 const accent = () => frame.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--crewship-page-accent').trim())
 const initial = await accent()
 assert.match(initial, /^#[0-9a-f]{6}$/i)
 assert.equal(await frame.locator('.stats article').first().evaluate(el => getComputedStyle(el).animationName), 'page-enter')
 let navigations = 0
 page.on('framenavigated', child => { if (child.url().includes('/api/v1/pages/runtime/bootstrap')) navigations++ })
 const settings = await context.newPage()
 settings.on('pageerror', error => errors.push(error.message))
 await settings.goto(base + '/settings?tab=general')
 const input = settings.getByRole('textbox', { name: 'Brand accent', exact: true })
 await input.waitFor()
 const replacement = initial.toLowerCase() === '#60a5fa' ? '#d8b4fe' : '#60a5fa'
 await input.fill(replacement)
 restoreNeeded = true
 const saved = settings.waitForResponse(response => response.url().includes(`/workspaces/${workspace}`) && response.request().method() === 'PATCH')
 await settings.getByRole('button', { name: /^save$/i }).click()
 assert.equal((await saved).status(), 200, 'save through the settings UI')
 // The original Page stays in the background: this proves realtime propagation,
 // rather than a focus/reload fetch or a rebuild.
 for (let i = 0; i < 100 && await accent() !== replacement; i++) await page.waitForTimeout(100)
 assert.equal(await accent(), replacement, 'palette reached another open tab')
 assert.equal(navigations, 0, 'theme change did not reload the application')
 await settings.screenshot({ path: `${output}/settings.png`, fullPage: true })
 const restored = await context.request.patch(endpoint, { data: { pages_theme: original } })
 assert.equal(restored.status(), 200, 'restore original palette')
 restoreNeeded = false
 for (let i = 0; i < 100 && await accent() !== initial; i++) await page.waitForTimeout(100)
 assert.equal(await accent(), initial)
 await page.bringToFront()
 await page.emulateMedia({ reducedMotion: 'reduce' })
 assert.equal(await frame.locator('.stats article').first().evaluate(el => getComputedStyle(el).animationName), 'none')
 assert.equal(await frame.locator('.primary').first().evaluate(el => getComputedStyle(el).transitionDuration), '0s')
 await page.screenshot({ path: `${output}/operations-desktop.png`, fullPage: true })
 await page.setViewportSize({ width: 900, height: 1050 })
 assert.equal(await frame.locator('html').evaluate(el => el.scrollWidth <= el.clientWidth + 1), true, 'responsive Page fits its frame')
 await page.screenshot({ path: `${output}/operations-tablet.png`, fullPage: true })
 assert.deepEqual(errors, [], 'browser runtime errors')
 console.log(JSON.stringify({ settingsSave: true, crossTabTheme: true, noAppReload: true, originalPaletteRestored: true, entranceAnimation: true, reducedMotion: true, responsive: true, pageErrors: errors }))
} finally {
 try {
  if (restoreNeeded && context) {
   const response = await context.request.patch(endpoint, { data: { pages_theme: original } })
   if (!response.ok()) throw new Error(`Original palette restoration failed: HTTP ${response.status()}`)
  }
 } finally { await browser.close() }
}
