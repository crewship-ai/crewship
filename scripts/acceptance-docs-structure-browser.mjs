// Actual production UI/Router/SQLite/matched Node. One human identity in two
// browser contexts, no model/worker execution and no fabricated API responses.
import {readFile,mkdir,mkdtemp,writeFile} from 'node:fs/promises'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import {chromium,expect} from '@playwright/test'
const fixture=JSON.parse(await readFile(process.env.CREWSHIP_DOCS_STRUCTURE_FIXTURE,'utf8'))
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture.server))throw new Error('Owned numeric loopback fixture required')
const output=process.env.CREWSHIP_DOCS_STRUCTURE_OUTPUT??await mkdtemp(join(tmpdir(),'crewship-docs-structure-'));await mkdir(output,{recursive:true})
console.log(`Structural Docs browser evidence: ${output}`)
const browser=await chromium.launch({headless:true}),contexts=[],results=[]
const endpoint=`${fixture.server}/api/v1/pages/${fixture.slug}/document?workspace_id=${fixture.workspace}`
async function context(touch=false){const ctx=await browser.newContext({viewport:touch?{width:390,height:844}:{width:1280,height:900},hasTouch:touch});contexts.push(ctx);await ctx.addCookies([{name:'authjs.session-token',value:fixture.token,url:fixture.server,httpOnly:true,sameSite:'Lax'}]);await ctx.addInitScript(id=>localStorage.setItem('crewship.workspaceId',id),fixture.workspace);const auth=await ctx.request.get(`${fixture.server}/api/auth/session`);expect(auth.status()).toBe(200);expect((await auth.json()).user.id).toBe(fixture.user_id);return ctx}
async function check(name){results.push({name,status:'PASS'});await writeFile(join(output,'results.json'),JSON.stringify({execution:'production UI/Router/SQLite/managed Node; one synthetic human in desktop/touch contexts; queued admission only, no worker/model',results},null,2))}
const region=page=>page.getByRole('region',{name:'Doc page',exact:true})
const work=page=>page.getByRole('region',{name:'Document agent work',exact:true})
async function saved(page){await expect(region(page).getByRole('status').filter({hasText:/^Saved$/})).toBeVisible({timeout:20000})}
try{
 const owner=await context(),touch=await context(true),page=await owner.newPage(),other=await touch.newPage()
 await page.goto(`${fixture.server}/pages/${fixture.slug}`);await saved(page)
 expect((await(await owner.request.get(endpoint)).json()).content.content).toHaveLength(200)
 await work(page).getByLabel('Editing scope',{exact:true}).selectOption('blocks')
 await work(page).getByRole('button',{name:'Select all blocks',exact:true}).click()
 await expect(work(page).getByLabel('Selected whole blocks for agent',{exact:true})).toContainText('200 whole blocks')
 await work(page).getByLabel('Private agent chat',{exact:true}).selectOption(fixture.chat_id)
 await work(page).getByLabel('Agent instructions',{exact:true}).fill('Reorganize all explicitly selected blocks.')
 await page.screenshot({path:join(output,'desktop-explicit-all-200.png'),fullPage:true})
 let original,retried,dropped=false
 await page.route('**/document/requests?*',async route=>{if(route.request().method()!=='POST')return route.continue();const response=await route.fetch();expect(response.status()).toBe(202);if(!dropped){original=route.request().postData();dropped=true;await route.abort('failed')}else{retried=route.request().postData();await route.fulfill({response})}})
 await work(page).getByRole('button',{name:'Send to agent',exact:true}).click()
 await expect(work(page).getByRole('button',{name:'Retry send',exact:true})).toBeEnabled()
 await work(page).getByRole('button',{name:'Retry send',exact:true}).click()
 await expect(work(page).getByLabel('Selected whole blocks for agent',{exact:true})).toHaveCount(0)
 expect(retried).toBe(original);const input=JSON.parse(original);expect(input.structure).toEqual({block_ids:Array.from({length:200},(_,i)=>`p${i}`),allow_insert_after:true});expect(input.targets).toBeUndefined();expect(input.structure.block_ids).toHaveLength(200)
 const requests=await(await owner.request.get(endpoint.replace('/document?','/document/requests?'))).json();expect(requests.requests).toHaveLength(1);expect(requests.requests[0].status).toBe('queued');await page.unroute('**/document/requests?*')
 await check('explicit whole-document selection preserves all200 root IDs; actual admission lost-ACK retry queues exactly one original request')
 await work(page).getByLabel('Select block 1: paragraph',{exact:true}).check()
 await work(page).getByLabel('Select block 2: paragraph',{exact:true}).check()
 await work(page).getByRole('button',{name:'Use selected blocks',exact:true}).click()
 await work(page).getByLabel('Private agent chat',{exact:true}).selectOption(fixture.chat_id)
 await work(page).getByLabel('Agent instructions',{exact:true}).fill('Keep this original two-block scope.')
 await other.goto(`${fixture.server}/pages/${fixture.slug}`);await saved(other)
 await other.locator('[data-block-id="p199"]').click();await other.keyboard.press('End');await other.keyboard.type(' human change');await saved(other)
 await expect(work(page).getByText(/The document changed after this selection/)).toBeVisible({timeout:20000})
 await expect(work(page).getByRole('button',{name:'Send to agent',exact:true})).toBeDisabled()
 await expect(work(page).getByLabel('Selected whole blocks for agent',{exact:true})).toContainText('2 whole blocks')
 await expect(work(page).getByLabel('Agent instructions',{exact:true})).toHaveValue('Keep this original two-block scope.')
 await page.screenshot({path:join(output,'desktop-stale-structural-draft.png'),fullPage:true})
 await work(other).getByLabel('Editing scope',{exact:true}).selectOption('blocks');await work(other).getByRole('button',{name:'Select all blocks',exact:true}).click();await expect(work(other).getByLabel('Selected whole blocks for agent',{exact:true})).toContainText('200 whole blocks');await other.screenshot({path:join(output,'touch-explicit-all-200.png'),fullPage:true})
 expect((await(await owner.request.get(endpoint.replace('/document?','/document/requests?'))).json()).requests).toHaveLength(1)
 await check('actual concurrent human save invalidates captured block grant, disables send and retains instructions/scope; desktop and touch explicit whole-document controls')
}catch(error){for(const[index,ctx]of contexts.entries())for(const page of ctx.pages()){if(!page.isClosed()){await page.screenshot({path:join(output,`failure-${index}.png`),fullPage:true}).catch(()=>{});await writeFile(join(output,`failure-${index}.txt`),await page.locator('body').innerText().catch(()=>'<unavailable>'))}}throw error}finally{await browser.close()}
console.log(`Actual structural Docs browser: ${results.length} checks PASS; evidence ${output}`)
