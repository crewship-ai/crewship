// Actual embedded Pages browser qualification on an approved own throwaway.
// Future/contradictory response metadata is explicitly injected to exercise an
// old client's fail-closed behavior; server storage/ACL evidence is Go tests.
import { chromium, expect } from '@playwright/test'
import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { mkdir, writeFile } from 'node:fs/promises'
const baseURL = process.env.CREWSHIP_SERVER
if (!baseURL || !/^http:\/\/127\.0\.0\.1:81\d\d$/.test(baseURL)) throw new Error('Run with scripts/throwaway-server.sh run')
const output = process.env.PAGE_TYPES_OUTPUT
if (!output) throw new Error('PAGE_TYPES_OUTPUT must name an own evidence directory')
await mkdir(output, { recursive: true })
const browser = await chromium.launch()
try {
 const context = await browser.newContext({ baseURL, viewport: { width: 1440, height: 1000 } })
 async function api(method, path, data, workspace) {
  const response = await context.request.fetch(path + (workspace ? `?workspace_id=${workspace}` : ''), { method, data, headers: { Origin: baseURL, ...(workspace ? { 'X-Workspace-ID': workspace } : {}) } })
  assert.ok(response.ok(), `${method} ${path}: ${response.status()} ${await response.text()}`)
  return response.json()
 }
 const boot = await api('POST', '/api/v1/bootstrap', { full_name: 'Registry Fixture', email: 'registry@example.test', password: `F1a-${randomBytes(16).toString('hex')}!` })
 const workspace = boot.workspace_id
 const created = await api('POST', '/api/v1/pages', { slug: 'registry-empty', name: 'Registry empty Page', panels: [], allow_empty: true }, workspace)
 assert.deepEqual(created.content_descriptor, { type_id: 'panels', schema_version: 1, supported: true })
 const page = await context.newPage(), errors = []
 page.on('pageerror', error => errors.push(error.message))
 await page.goto('/pages/registry-empty')
 await expect(page.getByRole('heading', { name: 'Attach an HTML artifact' })).toBeVisible()
 await expect(page.getByRole('button', { name: 'Paste HTML', exact: true })).toBeVisible()
 await page.screenshot({ path: `${output}/empty-attach.png` })
 const checks = ['Actual empty human Page retains Attach HTML']
 for (const fixture of [
  { name: 'unknown-primary', content_descriptor: { type_id: 'future', schema_version: 1, supported: true } },
  { name: 'wrong-application-role', has_project: true, has_application: true, application_descriptor: { type_id: 'doc', schema_version: 2, supported: true } },
 ]) {
  const forbiddenRequests = []
  const listener = request => { if (/\/pages\/registry-empty\/(artifact|application|project|document)(?:[/?]|$)/.test(request.url())) forbiddenRequests.push(request.url()) }
  page.on('request', listener)
  await page.route('**/api/v1/pages/registry-empty?*', async route => {
   const response = await route.fetch(), body = await response.json()
   await route.fulfill({ response, json: { ...body, ...fixture } })
  })
  await page.reload()
  await expect(page.getByText(/unsupported or conflicting content format/)).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Attach an HTML artifact' })).toHaveCount(0)
  await expect(page.locator('iframe')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Edit', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeDisabled()
  await page.goto('/pages/registry-empty?mode=edit')
  await expect(page.getByText(/unsupported or conflicting content format/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Edit', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Edit', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeDisabled()
  await expect(page.locator('[data-slot=page-editor]')).toHaveCount(0)
  // Keep the request listener attached through a bounded network quiet period,
  // including React effects after the unsupported state first becomes visible.
  await page.waitForLoadState('networkidle', { timeout: 10000 })
  await page.screenshot({ path: `${output}/${fixture.name}.png` })
  assert.deepEqual(forbiddenRequests, [], `${fixture.name}: content adapter fetched`)
  checks.push(`${fixture.name}: no content adapter request/frame/initialization or active editor toolbar (including direct edit URL)`)
  await page.unroute('**/api/v1/pages/registry-empty?*')
  page.off('request', listener)
 }
 assert.deepEqual(errors, [], 'browser errors')
 await writeFile(`${output}/report.json`, JSON.stringify({ checks, errors, metadataInjection: 'Only future/contradictory Page response metadata injected; bootstrap/create/empty attach use real APIs and embedded UI.', fullF1Acceptance: false }, null, 2) + '\n')
 console.log(JSON.stringify({ checks, errors }))
} finally { await browser.close() }
