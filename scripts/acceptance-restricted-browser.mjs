// Owned synthetic Router fixture; no shared-instance credentials or browser state.
import { readFile, mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium, expect } from '@playwright/test'

const fixture = JSON.parse(await readFile(process.env.CREWSHIP_BROWSER_FIXTURE, 'utf8'))
const scratch = await mkdtemp(join(tmpdir(), 'crewship-private-browser-'))
const base = fixture.server
const browser = await chromium.launch({ headless: true })
try {
  for (const [actor, width] of [['native-h1', 1280], ['native-h2', 390]]) {
    const context = await browser.newContext({ viewport: { width, height: 900 }, acceptDownloads: true })
    await context.addCookies([{ name: 'authjs.session-token', value: fixture.tokens[actor], url: base, httpOnly: true, sameSite: 'Lax' }])
    await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
    const page = await context.newPage()
    const failed = []
    const modelRequests = []
    page.on('request', request => { if (request.method() === 'POST' && request.url().includes('/restricted-run?')) modelRequests.push(request) })
    page.on('response', response => { if (response.url().includes('/api/') && response.status() >= 400) failed.push(`${response.status()} ${new URL(response.url()).pathname}`) })
    try {
      await page.goto(`${base}/chat/native-agent?session=${actor}-chat`, { waitUntil: 'domcontentloaded', timeout: 180000 })
      await expect(page.getByText('Conversation memory', { exact: true })).toBeVisible({ timeout: 90000 })
      await page.getByText('Conversation memory', { exact: true }).click()
      const memory = page.locator('details').filter({ has: page.getByText('Conversation memory', { exact: true }) })
      await expect(memory.getByText(`MEMORY_${actor}`, { exact: true })).toBeVisible()
      await expect(page.getByText(`MEMORY_${actor === 'native-h1' ? 'native-h2' : 'native-h1'}`, { exact: true })).toHaveCount(0)
      await memory.getByLabel('New conversation memory').fill(`BROWSER_NOTE_${actor}`)
      await memory.getByRole('button', { name: 'Save note', exact: true }).click()
      await expect(memory.getByText(`BROWSER_NOTE_${actor}`, { exact: true })).toBeVisible()
      const note = memory.getByRole('listitem').filter({ hasText: `BROWSER_NOTE_${actor}` })
      await note.getByRole('button', { name: 'Remove note', exact: true }).click()
      await expect(memory.getByText(`BROWSER_NOTE_${actor}`, { exact: true })).toHaveCount(0)
      await page.getByText('Output files', { exact: true }).click()
      const downloadPromise = page.waitForEvent('download')
      await page.getByRole('button', { name: 'Download output.txt', exact: true }).click()
      const download = await downloadPromise
      const path = await download.path()
      if ((await readFile(path, 'utf8')) !== `PRIVATE_${actor}`) throw new Error('Browser received foreign or incomplete output')
      const foreign = actor === 'native-h1' ? 'native-h2' : 'native-h1'
      const denied = await context.request.get(`${base}/api/v1/chats/${foreign}-chat/restricted-files?workspace_id=${fixture.workspace}`)
      if (denied.status() !== 404) throw new Error(`Foreign chat files returned ${denied.status()}`)
      const ownFile = fixture.project_files[actor]
      const foreignFile = fixture.project_files[foreign]
      const openInputs = async () => {
        const summary = page.locator('summary').filter({ hasText: /^Project inputs/ })
        await expect(summary).toBeVisible({ timeout: 60000 })
        await summary.click()
        const picker = page.locator('details').filter({ has: summary })
        await expect(picker.getByText(ownFile.name, { exact: true })).toBeVisible()
        await expect(picker.getByText(foreignFile.name, { exact: true })).toHaveCount(0)
        return picker
      }
      // A fresh draft requires an explicit authenticated preparation, never a
      // paid warmup or an inferred agent profile.
      await page.goto(`${base}/chat/native-agent?session=${actor}-input-draft`, { waitUntil: 'domcontentloaded' })
      await expect(page.getByRole('checkbox')).toHaveCount(0)
      const prepareResponse = page.waitForResponse(response => response.request().method() === 'POST' && response.url().includes('/agents/native-agent/chats?'))
      await page.getByRole('button', { name: 'Prepare project inputs', exact: true }).click()
      if ((await prepareResponse).status() >= 400) throw new Error('Draft preparation failed')
      let picker = await openInputs()
      if (modelRequests.length !== 0) throw new Error('Preparing inputs unexpectedly sent a model request')
      await expect(picker.getByRole('checkbox')).not.toBeChecked()
      await picker.getByRole('checkbox').check()
      // Changing conversation discards selection instead of silently carrying
      // a source from another message or reviving an earlier choice.
      await page.goto(`${base}/chat/native-agent?session=${actor}-chat`, { waitUntil: 'domcontentloaded' })
      picker = await openInputs()
      await expect(picker.getByRole('checkbox')).not.toBeChecked()
      await picker.getByRole('checkbox').check()
      const transcript = page.getByRole('log')
      const replies = transcript.getByText(`DONE_${actor}`, { exact: true })
      await expect(replies).toHaveCount(1)
      const before = await replies.count()
      const composer = page.getByPlaceholder('Message Native...')
      const runResponse = page.waitForResponse(response => response.url().includes('/restricted-run?') && response.request().method() === 'POST')
      await composer.fill(`ACTOR_${actor} BROWSER_TURN`)
      await composer.press('Enter')
      const response = await runResponse
      if (response.status() !== 200) throw new Error('Browser native run was denied')
      const payload = response.request().postDataJSON()
      if (JSON.stringify(payload.project_file_versions) !== JSON.stringify([ownFile.version_id])) throw new Error('Browser did not forward only the explicit own version')
      await expect(picker.getByRole('checkbox')).not.toBeChecked()
      await expect(replies).toHaveCount(before + 1, { timeout: 60000 })
      // Navigation recreated the disclosure in its closed default state.
      // Open it as a user would before counting accessible retained downloads.
      const outputFiles = page.locator('details').filter({ has: page.getByText('Output files', { exact: true }) })
      await outputFiles.getByText('Output files', { exact: true }).click()
      const retainedDownloads = outputFiles.getByRole('button', { name: 'Download output.txt', exact: true })
      await expect(retainedDownloads).toHaveCount(2, { timeout: 60000 })
      const retainedDownloadPromise = page.waitForEvent('download')
      await retainedDownloads.last().click()
      const retainedDownload = await retainedDownloadPromise
      if ((await readFile(await retainedDownload.path(), 'utf8')) !== `PRIVATE_${actor}`) throw new Error('Selected native turn did not retain exact own output bytes')
      console.log(`PASS actual browser ${actor} width=${width}: own memory, create/remove note, SHA-verified file download, foreign 404, explicit draft preparation, own-only picker/reset, selected native transport and retained output`)
    } catch (error) {
      await page.screenshot({ path: join(scratch, `${actor}-failure.png`), fullPage: true })
      console.error(`Browser failure ${actor}; API errors ${failed.join(', ')}; evidence ${scratch}`)
      throw error
    } finally { await context.close() }
  }
} finally {
  await browser.close()
}
