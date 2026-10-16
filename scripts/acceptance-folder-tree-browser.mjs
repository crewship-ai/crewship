// Runs only through TestFolderTreeBrowser, against its owned synthetic Router.
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { chromium, expect } from '@playwright/test'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_FOLDER_TREE_FIXTURE, 'utf8'))
const output = process.env.CREWSHIP_FOLDER_TREE_OUTPUT || await mkdtemp(join(tmpdir(), 'crewship-folder-tree-'))
await mkdir(output, { recursive: true })
const captures = []
async function capture(page, name) {
  const path = join(output, `${name}.png`)
  await page.screenshot({ path, fullPage: true })
  captures.push({ name, path })
}
const browser = await chromium.launch({ headless: true })
const contexts = {}
const suffix = `?workspace_id=${encodeURIComponent(fixture.workspace)}`
const endpoint = path => `${fixture.server}/api/v1/page-folders${path}${suffix}`
try {
  for (const actor of ['owner', 'reader']) {
    const context = contexts[actor] = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await context.addCookies([{ name: 'authjs.session-token', value: fixture.tokens[fixture[actor]], url: fixture.server, httpOnly: true, sameSite: 'Lax' }])
    await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
  }
  const page = await contexts.owner.newPage()
  await page.goto(`${fixture.server}/pages`)
  async function create(name, parent = '') {
    await page.getByRole('button', { name: 'New folder', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('textbox', { name: /^Name/ }).fill(name)
    await dialog.getByLabel('Owning crew').selectOption(`crew/${fixture.crew}`)
    if (parent) await dialog.getByLabel('Parent folder').selectOption(parent)
    const response = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/v1/page-folders')
    await dialog.getByRole('button', { name: 'Create folder', exact: true }).click()
    assert.equal((await response).status(), 201)
    await expect(dialog).toHaveCount(0)
    await expect(page.getByRole('button', { name: `Actions for folder ${name}`, exact: true })).toBeVisible()
  }
  await create('Browser parent')
  await create('Browser child', 'browser-parent')
  await create('Browser other')
  let listing = await (await contexts.owner.request.get(endpoint(''))).json()
  let child = listing.folders.find(f => f.slug === 'browser-child')
  const parent = listing.folders.find(f => f.slug === 'browser-parent')
  assert.equal(child.parent_id, parent.id)
  await capture(page, 'owner-nested')
  await page.getByRole('button', { name: 'Actions for folder Browser child', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Move folder…', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('New parent').selectOption('browser-other')
  const moved = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/browser-child/parent'))
  await dialog.getByRole('button', { name: 'Move folder', exact: true }).click()
  assert.equal((await moved).status(), 200)
  await expect(dialog).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('button', { name: 'Actions for folder Browser child', exact: true })).toBeVisible()
  listing = await (await contexts.owner.request.get(endpoint(''))).json()
  child = listing.folders.find(f => f.slug === 'browser-child')
  assert.equal(child.parent_id, listing.folders.find(f => f.slug === 'browser-other').id)
  await capture(page, 'owner-reparented')
  // Direct leaf share does not grant visibility to its parent.
  const granted = await contexts.owner.request.put(endpoint('/browser-child/acl'), { data: { subject_type: 'user', subject_id: fixture.reader, can_read: true, can_write: false } })
  assert.equal(granted.status(), 200)
  const reader = await contexts.reader.newPage()
  await reader.goto(`${fixture.server}/pages`)
  await expect(reader.locator('[data-rail-header]').filter({ hasText: 'Browser child' })).toBeVisible()
  await expect(reader.getByText('Browser other', { exact: true })).toHaveCount(0)
  const raw = await (await contexts.reader.request.get(endpoint(''))).text()
  assert.equal(JSON.parse(raw).folders.find(f => f.slug === 'browser-child').parent_id, null)
  assert.ok(!raw.includes('browser-other'))
  await writeFile(join(output, 'reader-folders.json'), raw)
  await capture(reader, 'reader-hidden-parent')
  assert.equal((await contexts.owner.request.delete(endpoint(`/browser-child/acl/user/${fixture.reader}`))).status(), 204)
  await reader.reload()
  await expect(reader.locator('[data-rail-header]').filter({ hasText: 'Browser child' })).toHaveCount(0)
  await capture(reader, 'reader-revoked')
  const sourceHead = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
  const sourceDirty = execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }).trim()
  const report = { status: 'PASS', sourceHead, sourceDirty, execution: 'owned real Router/DB/static export; synthetic preauthenticated sessions; no HTTP mocks', scenarios: ['UI create', 'nested create', 'reparent', 'reload', 'hidden parent rawJSON/UI', 'revocation reload'], captures }
  await writeFile(join(output, 'report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify({ ...report, report: join(output, 'report.json') }))
} finally {
  await Promise.all(Object.values(contexts).map(context => context.close()))
  await browser.close()
}
