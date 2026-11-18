// Owned synthetic Router fixture only; session proofs are never reported.
import { readFile, mkdir, writeFile, mkdtemp } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { chromium } from '@playwright/test'
import { triggerDetailLoadErrorFlow } from '../e2e/trigger-detail-load-error.ts'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_TRIGGER_DETAIL_FIXTURE, 'utf8'))
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture.server)) throw new Error('Owned localhost fixture required')
const output = process.env.CREWSHIP_TRIGGER_DETAIL_OUTPUT ?? await mkdtemp(join(tmpdir(), 'crewship-trigger-detail-'))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
try {
  for (const viewport of [{width:1280,height:900},{width:390,height:844}]) {
    const context = await browser.newContext({baseURL:fixture.server,viewport})
    try {
      await context.addCookies([{name:'authjs.session-token',value:fixture.token,url:fixture.server,httpOnly:true,sameSite:'Lax'}])
      await context.addInitScript(id => localStorage.setItem('crewship.workspaceId',id),fixture.workspace)
      const page = await context.newPage()
      await triggerDetailLoadErrorFlow(page,fixture.trigger,fixture.name)
      await page.screenshot({path:join(output,`not-found-${viewport.width}.png`)})
      await writeFile(join(output,`results-${viewport.width}.json`),JSON.stringify({status:'PASS',viewport,http_failure_retry:true,network_failure_retry:true,real_api_not_found:true},null,2))
      console.log(`PASS trigger detail HTTP/network failure, Retry and real not-found at ${viewport.width}px`)
    } finally { await context.close() }
  }
} finally { await browser.close() }
