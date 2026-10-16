// Owned synthetic sessions, actual Router/Node/canonical DB/static export. No HTTP mocks.
import assert from 'node:assert/strict'
import { readFile, mkdtemp, mkdir, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { chromium, expect } from '@playwright/test'
import { loopbackOrigin } from '../tools/docs-d0/fixture-boundaries.mjs'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_PAGE_SEARCH_FIXTURE, 'utf8'))
const origin = loopbackOrigin(fixture.server)
const outputBase = process.env.CREWSHIP_PAGE_SEARCH_OUTPUT || tmpdir()
await mkdir(outputBase, { recursive: true, mode: 0o700 })
const output = await mkdtemp(join(outputBase, 'crewship-native-search-'))
const browser = await chromium.launch({ headless: true })
const contexts = []
const captures = []
async function context(actor) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  contexts.push(context)
  await context.addCookies([{ name: 'authjs.session-token', value: fixture.tokens[fixture[actor]], url: origin, httpOnly: true, sameSite: 'Lax' }])
  await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
  const probe = await context.request.get(`${origin}/api/auth/session`)
  assert.equal(probe.status(), 200)
  assert.equal((await probe.json()).user.id, fixture[actor], 'real cookie session must identify intended synthetic actor')
  return context
}
async function capture(page, name) {
  const path = join(output, `${name}.png`)
  await page.screenshot({ path, fullPage: true })
  captures.push({ name, path })
}
const searchURL = `${origin}/api/v1/pages/search/content?workspace_id=${encodeURIComponent(fixture.workspace)}&q=needle%20second`
try {
  const owner = await context('owner')
  const ownerResult = await owner.request.get(searchURL)
  assert.equal(ownerResult.status(), 200)
  assert.equal((await ownerResult.json()).matches.length, 3, 'owner must index private matching Page too')
  const reader = await context('reader')
  const page = await reader.newPage()
  await page.goto(`${origin}/pages`)
  await page.getByRole('button', { name: 'Search native text', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('textbox', { name: 'Words to find' }).fill('needle second')
  await dialog.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(dialog.getByRole('button', { name: /search-browser-original/ })).toBeVisible()
  await expect(dialog.getByRole('button', { name: /Canonical Doc/ })).toBeVisible()
  await expect(dialog.getByText('search-browser-hidden', { exact: true })).toHaveCount(0)
  const raw = await (await reader.request.get(searchURL)).text()
  const result = JSON.parse(raw)
  assert.deepEqual(result.matches.map(item => item.page_id), ['search-browser-doc', 'search-browser-original'])
  assert.ok(!raw.includes('search-browser-hidden'))
  assert.equal(result.has_more, false)
  await writeFile(join(output, 'reader-results.json'), raw, { mode: 0o600 })
  await capture(page, 'native-doc-slides-visible')

  // The server renames and reuses the slug at actual immutable-ID resolution.
  await dialog.getByRole('button', { name: /search-browser-original/ }).click()
  await expect(page.getByRole('img', { name: 'Slide 1', exact: true })).toBeVisible()
  assert.equal(new URL(page.url()).searchParams.get('ref'), 'search-browser-original')
  assert.equal(new URL(page.url()).searchParams.get('workspace_id'), fixture.workspace)
  assert.equal(new URL(page.url()).pathname, '/pages/search-browser-renamed')
  await expect(page.getByText('Forbidden reused search body', { exact: true })).toHaveCount(0)
  await capture(page, 'stable-result-renamed')

  await page.getByRole('button', { name: 'Search native text', exact: true }).click()
  await dialog.getByRole('textbox').fill('needle second')
  await dialog.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(dialog.getByRole('button', { name: /search-browser-original/ })).toBeVisible()
  const revoked = await owner.request.delete(`${origin}/api/v1/pages/search-browser-renamed/grants?workspace_id=${encodeURIComponent(fixture.workspace)}&subject_type=user&subject=${encodeURIComponent(fixture.reader)}`)
  assert.equal(revoked.status(), 200)
  // Actual server grant revoke emits no universal Page event. Assert the
  // documented bounded local expiry, then explicit live canonical re-query.
  await expect(dialog.getByRole('button', { name: /search-browser-original/ })).toHaveCount(0, { timeout: 18000 })
  await expect(dialog.getByRole('alert')).toContainText('Search again')
  await dialog.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(dialog.getByRole('button', { name: /Canonical Doc/ })).toBeVisible()
  await expect(dialog.getByRole('button', { name: /search-browser-original/ })).toHaveCount(0)
  await capture(page, 'revoked-expired-and-requeried')

  assert.equal((await owner.request.post(`${origin}/fixture-stop-docs`)).status(), 204)
  await dialog.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('not ready')
  await expect(dialog.getByRole('button', { name: /Canonical Doc/ })).toHaveCount(0)
  await expect(dialog.getByText('No matching supported native Pages.', { exact: true })).toHaveCount(0)
  await capture(page, 'managed-engine-unavailable')
  const report = { status: 'PASS', sourceHead: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), sourceDirty: execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }).trim(), execution: 'actual Router + private Node + canonical Docs/Slides + static export; distinct synthetic owner/reader JWT sessions; no HTTP mocks', scenarios: ['native Doc and Slides body text', 'owner-indexed hidden match absent', 'immutable ID result navigation survives rename/reused slug', 'actual grant revoke clears local batch within 15s then explicit requery excludes', 'stopped managed engine yields error, not empty results'], captures }
  await writeFile(join(output, 'report.json'), JSON.stringify(report, null, 2), { mode: 0o600 })
  console.log(JSON.stringify({ ...report, report: join(output, 'report.json') }))
} finally {
  await Promise.all(contexts.map(context => context.close()))
  await browser.close()
}
