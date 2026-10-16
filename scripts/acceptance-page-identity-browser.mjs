// Owned synthetic sessions, actual Go Router/DB/static export. No HTTP mocks.
import assert from 'node:assert/strict'
import { readFile, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { chromium, expect } from '@playwright/test'
import { loopbackOrigin } from '../tools/docs-d0/fixture-boundaries.mjs'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_PAGE_IDENTITY_FIXTURE, 'utf8'))
const origin = loopbackOrigin(fixture.server)
const output = await mkdtemp(join(tmpdir(), 'crewship-page-identity-'))
const browser = await chromium.launch({ headless: true })
const contexts = []
const captures = []
const link = `${origin}/pages?ref=${encodeURIComponent(fixture.pageID)}&workspace_id=${encodeURIComponent(fixture.workspace)}`
async function context(actor) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] })
  contexts.push(context)
  await context.addCookies([{ name: 'authjs.session-token', value: fixture.tokens[fixture[actor]], url: origin, httpOnly: true, sameSite: 'Lax' }])
  await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
  return context
}
async function capture(page, name) {
  const path = join(output, `${name}.png`)
  await page.screenshot({ path, fullPage: true })
  captures.push({ name, path })
}
try {
  const owner = await context('owner')
  const page = await owner.newPage()
  await page.goto(link)
  await expect(page.getByRole('button', { name: 'Resolve Page link again' })).toBeVisible()
  await expect(page.getByText('Forbidden reused body', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Copy Page link', exact: true })).toHaveCount(0)
  assert.equal(new URL(page.url()).searchParams.get('ref'), fixture.pageID)
  assert.equal(new URL(page.url()).pathname, '/pages/identity-old')
  await capture(page, 'reused-slug-rejected')
  await page.getByRole('button', { name: 'Resolve Page link again' }).click()
  await expect(page.getByRole('button', { name: 'Copy Page link', exact: true })).toBeVisible()
  await expect(page.getByText('Original stable Page', { exact: true }).first()).toBeVisible()
  assert.equal(new URL(page.url()).pathname, '/pages/identity-renamed')
  assert.equal(new URL(page.url()).searchParams.get('ref'), fixture.pageID)
  await page.getByRole('button', { name: 'Copy Page link', exact: true }).click()
  await expect(page.getByText('Page link copied', { exact: true })).toBeVisible()
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), link)
  await page.reload()
  await expect(page.getByRole('button', { name: 'Copy Page link', exact: true })).toBeVisible()
  assert.equal(new URL(page.url()).searchParams.get('ref'), fixture.pageID)
  await capture(page, 'renamed-original-reloaded')

  const reader = await context('reader')
  const view = await reader.newPage()
  await view.goto(link)
  await expect(view.getByRole('button', { name: 'Copy Page link', exact: true })).toBeVisible()
  await expect(view.getByText('Hidden ancestor canary', { exact: true })).toHaveCount(0)
  const identityURL = `${origin}/api/v1/pages/resolve/id/${fixture.pageID}?workspace_id=${fixture.workspace}`
  const raw = await (await reader.request.get(identityURL)).text()
  assert.deepEqual(JSON.parse(raw), { page_id: fixture.pageID, slug: 'identity-renamed' })
  assert.ok(!raw.includes('identity-browser-parent'))
  await writeFile(join(output, 'reader-identity.json'), raw, { mode: 0o600 })
  await capture(view, 'reader-hidden-parent')
  const revoked = await owner.request.delete(`${origin}/api/v1/page-folders/identity-leaf/acl/user/${fixture.reader}?workspace_id=${fixture.workspace}`)
  assert.equal(revoked.status(), 204)
  assert.equal((await reader.request.get(identityURL)).status(), 404)
  await view.reload()
  await expect(view.getByText('Page link is unavailable. Check your workspace and Page access.', { exact: true })).toBeVisible()
  await expect(view.getByRole('button', { name: 'Copy Page link', exact: true })).toHaveCount(0)
  await expect(view.getByText('Original stable Page', { exact: true })).toHaveCount(0)
  await capture(view, 'reader-revoked')
  const report = { status: 'PASS', sourceHead: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), sourceDirty: execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }).trim(), execution: 'owned actual Router/DB/static export; synthetic sessions; real DB rename/reused-slug before detail GET; no HTTP mocks', scenarios: ['wrong raw PageID rejected', 'explicit retry follows original ID', 'canonical URL retains ID', 'clipboard confirmed workspace+ID', 'reload', 'leaf ACL with hidden ancestor', 'revoked reader 404/no mount'], captures }
  await writeFile(join(output, 'report.json'), JSON.stringify(report, null, 2), { mode: 0o600 })
  console.log(JSON.stringify({ ...report, report: join(output, 'report.json') }))
} finally {
  await Promise.all(contexts.map(context => context.close()))
  await browser.close()
}
