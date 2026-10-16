// Actual owned Router/SQLite/static UI, synthetic sessions. Network abort occurs
// only AFTER route.fetch commits through the real authority; no response mocks.
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium, expect } from '@playwright/test'
const fixture=JSON.parse(await readFile(process.env.CREWSHIP_SLIDES_WORKFLOW_FIXTURE,'utf8'))
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture.server))throw new Error('Owned localhost fixture required')
const output=process.env.CREWSHIP_SLIDES_WORKFLOW_OUTPUT??await mkdtemp(join(tmpdir(),'crewship-slides-workflow-'))
await mkdir(output,{recursive:true})
const browser=await chromium.launch({headless:true}),results=[],contexts=[]
async function check(name,condition){results.push({name,status:condition?'PASS':'FAIL'});await writeFile(join(output,'results.json'),JSON.stringify({execution:'real Router/DB/production static frontend; synthetic sessions, no model or mocked API responses',results},null,2));if(!condition)throw new Error(name)}
async function context(token,touch=false){const value=await browser.newContext({viewport:touch?{width:390,height:844}:{width:1280,height:900},hasTouch:touch});await value.addCookies([{name:'authjs.session-token',value:token,url:fixture.server,httpOnly:true,sameSite:'Lax'}]);await value.addInitScript(id=>localStorage.setItem('crewship.workspaceId',id),fixture.workspace);contexts.push(value);const probe=await value.request.get(`${fixture.server}/api/auth/session`);const identity=await probe.json();expect(probe.status()).toBe(200);expect(identity.user?.id).toBe(token===fixture.owner_token?'boss':'slides-reader');return value}
try{
 for(const mode of ['desktop','touch']){
  const ctx=await context(fixture.owner_token,mode==='touch'),page=await ctx.newPage(),slug=`native-${mode}`
  const endpoint=(path='')=>`${fixture.server}/api/v1/pages/${slug}/slides${path}?workspace_id=${fixture.workspace}`
  const state=async()=>{const r=await ctx.request.get(endpoint());expect(r.status()).toBe(200);return r.json()}
  await page.goto(`${fixture.server}/pages`)
  await page.getByRole('button',{name:'New Doc, Slides or artifact',exact:true}).click()
  const dialog=page.getByRole('dialog');await dialog.getByLabel('Content',{exact:true}).selectOption('slides');await dialog.getByLabel('Name',{exact:true}).fill(`Native ${mode}`);await dialog.getByRole('button',{name:'Create Slides page',exact:true}).click()
  const work=page.getByRole('region',{name:'Slides Page',exact:true});await expect(work).toBeVisible()
  await work.getByRole('button',{name:'Add text',exact:true}).click();const literal=`<script>window.__slideExecuted=1</script> Native ${mode} 🐎`
  await work.getByRole('textbox',{name:'Text',exact:true}).fill(literal);await work.getByRole('textbox',{name:'Speaker notes',exact:true}).fill(`Saved notes ${mode}`)
  async function save(){const pending=page.waitForResponse(r=>r.request().method()==='POST'&&/\/slides\/(patch|reorder)$/.test(new URL(r.url()).pathname));await work.getByRole('button',{name:'Save changes',exact:true}).click();const response=await pending;expect(response.status()).toBe(200);const receipt=await response.json();await expect(work.getByRole('button',{name:'Save changes',exact:true})).toBeDisabled();await expect(work.getByRole('status').filter({hasText:`Saved revision ${receipt.revision}`})).toBeVisible();return receipt}
  await save();let saved=await state();expect(saved.deck.slides[0].elements[0].text.value).toBe(literal);expect(saved.deck.slides[0].notes).toBe(`Saved notes ${mode}`);expect(await page.evaluate(()=>window.__slideExecuted)).toBeUndefined()
  await page.reload();await expect(work.getByRole('status').filter({hasText:`Saved revision ${saved.revision}`})).toBeVisible();await work.getByRole('button',{name:'Select text 1',exact:true}).click();await expect(work.getByRole('textbox',{name:'Text',exact:true})).toHaveValue(literal)
  await work.getByRole('button',{name:'Duplicate slide',exact:true}).click();await save();saved=await state();const ids=saved.deck.slides.flatMap(s=>[s.id,...s.elements.map(e=>e.id)]);expect(new Set(ids).size).toBe(ids.length)
  // Delete a newly added third slide; all existing object identities survive.
  const stableIDs=[...ids].sort();await work.getByRole('button',{name:'New slide',exact:true}).click();await save();await work.getByRole('button',{name:'Slide 2',exact:true}).click();await work.getByRole('button',{name:'Delete slide',exact:true}).click();await save();saved=await state();expect(saved.deck.slides.flatMap(s=>[s.id,...s.elements.map(e=>e.id)]).sort()).toEqual(stableIDs);await page.reload();await expect(work.getByRole('status').filter({hasText:`Saved revision ${saved.revision}`})).toBeVisible()
  // The acknowledged revision remount deliberately starts at the first slide.
  await work.getByRole('button',{name:'Slide 2',exact:true}).click();await work.getByRole('button',{name:'Move slide earlier',exact:true}).click();await expect(work.getByRole('textbox',{name:'Speaker notes',exact:true})).toBeDisabled();await save()
  await work.getByRole('textbox',{name:'Speaker notes',exact:true}).fill('Content must save before reorder');await expect(work.getByRole('button',{name:'Move slide later',exact:true})).toBeDisabled();await save()
  await page.screenshot({path:join(output,`${mode}-editor.png`),fullPage:true})
  await work.getByRole('button',{name:'Present',exact:true}).click();const populated=page.getByRole('dialog',{name:'Present slides',exact:true});await expect(populated).toBeVisible();await expect(populated.getByText(literal,{exact:true})).toBeVisible();await page.screenshot({path:join(output,`${mode}-present.png`)});await populated.getByRole('button',{name:'Close presentation',exact:true}).click()
  // Drop one real acknowledgement after the request already committed.
  await work.getByRole('textbox',{name:'Speaker notes',exact:true}).fill('Ack lost but durable')
  let originalBody,committedReceipt,dropped=false
  const pattern='**/slides/patch?*'
  await page.route(pattern,async route=>{if(dropped)return route.continue();dropped=true;originalBody=route.request().postData();const response=await route.fetch();expect(response.status()).toBe(200);committedReceipt=await response.json();await route.abort('failed')})
  await work.getByRole('button',{name:'Save changes',exact:true}).click();await expect(work.getByRole('button',{name:'Retry original save',exact:true})).toBeVisible();const beforeRetry=await state();expect(beforeRetry.revision).toBe(committedReceipt.revision)
  const retry=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname.endsWith('/slides/patch'))
  await work.getByRole('button',{name:'Retry original save',exact:true}).click();const duplicate=await retry;expect(duplicate.request().postData()).toBe(originalBody);expect((await duplicate.json()).duplicate).toBe(true);await expect(work.getByRole('status').filter({hasText:'original request confirmed'})).toBeVisible();expect((await state()).revision).toBe(beforeRetry.revision)
  await work.getByRole('button',{name:'History',exact:true}).click();await work.getByRole('button',{name:'View revision 1',exact:true}).click();await expect(work.getByRole('region',{name:'Saved Slides revision',exact:true})).toBeVisible();await work.getByRole('button',{name:'Close revision preview',exact:true}).click()
  const restoration=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname.endsWith('/slides/restore'));await work.getByRole('button',{name:'Restore revision 1',exact:true}).click();expect((await restoration).status()).toBe(200);await expect(work.getByRole('button',{name:'Save changes',exact:true})).toBeDisabled();saved=await state();expect(saved.deck.slides[0].elements).toEqual([])
  await work.getByRole('button',{name:'Present',exact:true}).click();const present=page.getByRole('dialog',{name:'Present slides',exact:true});await expect(present).toBeVisible();await page.screenshot({path:join(output,`${mode}-restored-empty.png`)});await present.getByRole('button',{name:'Close presentation',exact:true}).click()
  // A real concurrent writer advances head; the UI keeps its original base.
  await work.getByRole('button',{name:'Add text',exact:true}).click();await work.getByRole('textbox',{name:'Text',exact:true}).fill('Retained conflicting draft')
  const outline=await (await ctx.request.get(endpoint('/outline'))).json();const slide=outline.slides[0]
  const external=await ctx.request.post(endpoint('/patch'),{data:{page_id:saved.deck.deck_id,schema_version:1,base_rev:saved.revision,mutation_id:`external_${mode}`,operations:[{op:'set_notes',slide_id:slide.id,if_hash:slide.hash,notes:'Concurrent saved write'}]}});expect(external.status()).toBe(200)
  const conflict=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname.endsWith('/slides/patch'));await work.getByRole('button',{name:'Save changes',exact:true}).click();expect((await conflict).status()).toBe(409);await expect(work.getByRole('textbox',{name:'Text',exact:true})).toHaveValue('Retained conflicting draft');await expect(work.getByRole('status').filter({hasText:'Conflict.'})).toBeVisible();await page.screenshot({path:join(output,`${mode}-conflict.png`),fullPage:true})
  page.once('dialog',dialog=>dialog.accept());await work.getByRole('button',{name:'Discard draft and reopen saved Slides',exact:true}).click();await expect(work.getByRole('textbox',{name:'Speaker notes',exact:true})).toHaveValue('Concurrent saved write');expect((await state()).deck.slides[0].elements).toEqual([])
  await check(`${mode}: UI create/init, native edit+reload, duplicate/delete stable IDs, reorder guards, lost-ack exact retry, real history/restore/Present and conflict recovery`,true)
  await ctx.close()
 }
 const reader=await context(fixture.reader_token),page=await reader.newPage();await page.goto(`${fixture.server}/pages/readonly-slides`);const work=page.getByRole('region',{name:'Slides Page',exact:true});await expect(work).toBeVisible();await expect(work.getByRole('button',{name:'Add text',exact:true})).toBeDisabled();await expect(work.getByRole('button',{name:'Save changes',exact:true})).toBeDisabled();await page.screenshot({path:join(output,'reader-native.png'),fullPage:true});const prefix=`${fixture.server}/api/v1/pages/readonly-slides`;const metadata=await (await reader.request.get(`${prefix}?workspace_id=${fixture.workspace}`)).json();expect(metadata.can_edit_slides).toBe(false)
 const current=await (await reader.request.get(`${prefix}/slides?workspace_id=${fixture.workspace}`)).json(),outline=await (await reader.request.get(`${prefix}/slides/outline?workspace_id=${fixture.workspace}`)).json()
 const denied=await reader.request.post(`${prefix}/slides/patch?workspace_id=${fixture.workspace}`,{data:{page_id:current.deck.deck_id,schema_version:1,base_rev:current.revision,mutation_id:'reader_denied',operations:[{op:'set_notes',slide_id:outline.slides[0].id,if_hash:outline.slides[0].hash,notes:'forbidden'}]}});expect(denied.status()).toBe(403);await check('Read-only current Page capability disables controls and actual Router denies forged write',true);await reader.close()
}catch(error){for(const [index,ctx] of contexts.entries())for(const page of ctx.pages()){if(page.isClosed())continue;await page.screenshot({path:join(output,`failure-${index}.png`),fullPage:true}).catch(()=>{});await writeFile(join(output,`failure-${index}.txt`),`${page.url()}\n${await page.locator('body').innerText().catch(()=>'<unavailable>')}`)}throw error}finally{await browser.close()}
console.log(`Native durable Slides browser: ${results.length} checks PASS; evidence ${output}`)
