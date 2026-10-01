// Owned synthetic Router + production export fixture. Never target a shared instance.
import { readFile } from 'node:fs/promises'
import { chromium, expect } from '@playwright/test'

const fixture = JSON.parse(await readFile(process.env.CREWSHIP_WORKFLOW_BROWSER_FIXTURE, 'utf8'))
const browser = await chromium.launch({ headless: true })
const receipts = new Map()
try {
  for (const [actor, width] of [['browser-h1', 1280], ['browser-h2', 390]]) {
    const context = await browser.newContext({ viewport: { width, height: 900 } })
    await context.addCookies([{ name: 'authjs.session-token', value: fixture.tokens[actor], url: fixture.server, httpOnly: true, sameSite: 'Lax' }])
    await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
    const page = await context.newPage()
    const successfulSharedReads = []
    page.on('response', response => {
      const path = new URL(response.url()).pathname
      if (response.ok() && path.startsWith('/api/') && (path.includes('/journal') || path.includes('/panels/panel/data') || path.endsWith('/pipelines') || path.endsWith('/page-folders'))) successfulSharedReads.push(path)
    })
    try {
      await page.goto(`${fixture.server}/routines`, { waitUntil: 'domcontentloaded', timeout: 90000 })
      await expect(page.getByRole('heading', { name: 'Private routines', exact: true })).toBeVisible({ timeout: 90000 })
      await expect(page.getByRole('option', { name: 'Allowed routine', exact: true })).toHaveCount(1)
      await expect(page.getByText('DENIED_ROUTINE_CANARY', { exact: true })).toHaveCount(0)
      await page.getByRole('combobox', { name: 'Routine', exact: true }).selectOption('private-work')
      await page.getByRole('textbox', { name: 'task', exact: true }).fill(`BROWSER_ROUTINE_${actor}`)
      const admissionPromise = page.waitForResponse(response => response.url().includes('/pipelines/private-work/run') && response.request().method() === 'POST')
      await page.getByRole('button', { name: 'Run routine', exact: true }).click()
      const admission = await admissionPromise
      if (admission.status() !== 202) throw new Error(`Routine admission status ${admission.status()}`)
      receipts.set(actor, (await admission.json()).run_id)
      await expect(page.getByText(`ROUTINE_RESULT_${actor}`, { exact: true })).toBeVisible({ timeout: 90000 })
      const foreign = actor === 'browser-h1' ? 'browser-h2' : 'browser-h1'
      await expect(page.getByText(`ROUTINE_RESULT_${foreign}`, { exact: true })).toHaveCount(0)
      if (receipts.has(foreign)) {
        const response = await context.request.get(`${fixture.server}/api/v1/workspaces/${fixture.workspace}/restricted-routine-runs/${receipts.get(foreign)}`)
        if (response.status() !== 404) throw new Error('Foreign private receipt was disclosed')
      }
      await page.goto(`${fixture.server}/pages`, { waitUntil: 'domcontentloaded', timeout: 90000 })
      await expect(page.getByRole('heading', { name: 'Page actions', exact: true })).toBeVisible({ timeout: 90000 })
      await expect(page.getByText('FIXED_PRIVATE_CANARY', { exact: true })).toHaveCount(0)
      await expect(page.getByText('DENIED_PAGE_CANARY', { exact: true })).toHaveCount(0)
      if (actor === 'browser-h1') {
        await page.getByRole('combobox', { name: 'Action', exact: true }).selectOption(JSON.stringify(['allowed-page', 'panel', 'work']))
        await page.getByRole('textbox', { name: 'task', exact: true }).fill(`BROWSER_PAGE_${actor}`)
        page.once('dialog', dialog => dialog.accept())
        const clickPromise = page.waitForResponse(response => response.url().includes('/panels/panel/actions/work') && response.request().method() === 'POST')
        await page.getByRole('button', { name: 'Run action', exact: true }).click()
        const click = await clickPromise
        if (click.status() !== 202) throw new Error(`Page admission HTTP status=${click.status()} class=admission_rejected`)
        if (!click.request().postDataJSON().expected_intent_hash) throw new Error('Page admission class=missing_intent_hash')
        if (new URL(click.request().url()).searchParams.get('workspace_id') !== fixture.workspace) throw new Error('Page admission class=missing_workspace_scope')
        await expect(page.getByText(`PAGE_RESULT_${actor}`, { exact: true })).toBeVisible({ timeout: 90000 })
      } else {
        await expect(page.getByText('No Page actions are available with your current access.', { exact: true })).toBeVisible()
        await expect(page.getByRole('option', { name: 'Allowed Page — Allowed work', exact: true })).toHaveCount(0)
        const denied = await context.request.post(`${fixture.server}/api/v1/pages/allowed-page/panels/panel/actions/work?workspace_id=${fixture.workspace}`, { data: { inputs: { task: 'foreign' } } })
        if (![403, 404].includes(denied.status())) throw new Error('Page action role floor was bypassed')
      }
      if (successfulSharedReads.length) throw new Error(`Shared data reached restricted workflow UI: ${successfulSharedReads.join(',')}`)
      console.log(`Restricted workflow browser passed actor=${actor} width=${width}`)
    } finally {
      await context.close()
    }
  }
} finally {
  await browser.close()
}
