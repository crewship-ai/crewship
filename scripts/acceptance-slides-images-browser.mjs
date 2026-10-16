// Real production Router/SQLite/static UI with distinct owner and read-only users.
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium, expect } from '@playwright/test'
const fixture=JSON.parse(await readFile(process.env.CREWSHIP_SLIDES_IMAGES_FIXTURE,'utf8'))
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture.server))throw new Error('Owned localhost fixture required')
const output=process.env.CREWSHIP_SLIDES_IMAGES_OUTPUT??await mkdtemp(join(tmpdir(),'crewship-slide-images-'));await mkdir(output,{recursive:true})
const browser=await chromium.launch({headless:true}),contexts=[],results=[]
async function check(name){results.push({name,status:'PASS'});await writeFile(join(output,'results.json'),JSON.stringify({execution:'actual Router/SQLite/private raster/static UI; two distinct synthetic human identities; no model or response mocks',results},null,2))}
async function context(token,id,touch=false){const ctx=await browser.newContext({viewport:touch?{width:390,height:844}:{width:1280,height:900},hasTouch:touch});contexts.push(ctx);await ctx.addCookies([{name:'authjs.session-token',value:token,url:fixture.server,httpOnly:true,sameSite:'Lax'}]);await ctx.addInitScript(id=>localStorage.setItem('crewship.workspaceId',id),fixture.workspace);const probe=await ctx.request.get(`${fixture.server}/api/auth/session`);expect(probe.status()).toBe(200);expect((await probe.json()).user.id).toBe(id);return ctx}
const endpoint=(path='')=>`${fixture.server}/api/v1/pages/images-native/slides${path}?workspace_id=${fixture.workspace}`
try{
 const owner=await context(fixture.owner_token,'boss'),reader=await context(fixture.reader_token,'slide-reader',true),page=await owner.newPage(),readPage=await reader.newPage()
 const external=[];page.on('request',request=>{const url=new URL(request.url());if(['http:','https:'].includes(url.protocol)&&url.origin!==fixture.server)external.push(url.origin)})
 await page.goto(`${fixture.server}/pages/images-native`)
 const work=page.getByRole('region',{name:'Slides Page',exact:true}),insert=page.getByRole('region',{name:'Insert private slide image',exact:true})
 await expect(work.getByRole('button',{name:'Save changes',exact:true})).toBeDisabled()
 await insert.getByLabel('PNG or JPEG file',{exact:true}).setInputFiles(fixture.image_path);await insert.getByLabel('Image alternative text',{exact:true}).fill('Private gradient <script>window.imageExecuted=true</script>')
 let uploadBody,uploadURL,uploadReceipt,droppedUpload=false
 await page.route('**/assets?*',async route=>{if(route.request().method()!=='POST'||droppedUpload)return route.continue();droppedUpload=true;uploadBody=route.request().postDataBuffer();uploadURL=route.request().url();const response=await route.fetch();expect(response.status()).toBe(201);uploadReceipt=await response.json();await route.abort('failed')})
 await insert.getByRole('button',{name:'Upload and insert image',exact:true}).click();await expect(insert.getByRole('button',{name:'Retry original image insertion',exact:true})).toBeEnabled()
 expect((await(await owner.request.get(endpoint())).json()).revision).toBe(1)
 const retry=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname.endsWith('/assets'))
 await insert.getByRole('button',{name:'Retry original image insertion',exact:true}).click();const retried=await retry;expect(retried.request().url()).toBe(uploadURL);expect(retried.request().postDataBuffer().equals(uploadBody)).toBe(true);expect((await retried.json()).id).toBe(uploadReceipt.id)
 await expect(insert.getByRole('status')).toContainText('unsaved Slides draft');await expect(work.locator('image[href^="blob:"]')).toHaveCount(1)
 let patchBody,patchReceipt,droppedPatch=false
 await page.route('**/slides/patch?*',async route=>{if(droppedPatch)return route.continue();droppedPatch=true;patchBody=route.request().postData();const response=await route.fetch();expect(response.status()).toBe(200);patchReceipt=await response.json();await route.abort('failed')})
 await work.getByRole('button',{name:'Save changes',exact:true}).click();await expect(work.getByRole('button',{name:'Retry original save',exact:true})).toBeEnabled()
 const original=JSON.parse(patchBody);expect(original.schema_version).toBe(1);expect(original.target_schema_version).toBe(2);expect(original.base_rev).toBe(1);expect(original.if_content_hash).toMatch(/^[a-f0-9]{64}$/)
 const saveRetry=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname.endsWith('/slides/patch'))
 await work.getByRole('button',{name:'Retry original save',exact:true}).click();const saved=await saveRetry;expect(saved.request().postData()).toBe(patchBody);expect((await saved.json()).duplicate).toBe(true)
 await expect(work.getByRole('button',{name:'Save changes',exact:true})).toBeDisabled();let state=await(await owner.request.get(endpoint())).json();expect(state.revision).toBe(patchReceipt.revision);expect(state.schema_version).toBe(2);expect(state.revision).toBe(2)
 const image=state.deck.slides.flatMap(s=>s.elements).find(e=>e.type==='image');expect(image.image.asset_id).toBe(uploadReceipt.id);expect(image.image.expected_sha256).toBe(uploadReceipt.sha256)
 await page.reload();await expect(work.locator('image[href^="blob:"]')).toHaveCount(1);expect(await page.evaluate(()=>window.imageExecuted)).toBeUndefined();await page.screenshot({path:join(output,'desktop-private-image.png'),fullPage:true})
 await check('Raw upload and atomic v1→v2 save survive real lost acknowledgements with original payload/identity and exact duplicate receipts')
 await work.getByRole('button',{name:'Duplicate slide',exact:true}).click();await work.getByRole('button',{name:'Save changes',exact:true}).click();await expect(work.getByRole('button',{name:'Save changes',exact:true})).toBeDisabled();state=await(await owner.request.get(endpoint())).json();expect(state.deck.slides).toHaveLength(2)
 const refs=state.deck.slides.flatMap(s=>s.elements.filter(e=>e.type==='image'));expect(refs).toHaveLength(2);expect(new Set(refs.map(e=>e.id)).size).toBe(2);expect(new Set(refs.map(e=>e.image.asset_id)).size).toBe(1)
 await work.getByRole('button',{name:'Slide 2',exact:true}).click();await work.getByRole('button',{name:'Present',exact:true}).click();await expect(page.getByRole('dialog').locator('image[href^="blob:"]')).toHaveCount(1);await page.screenshot({path:join(output,'desktop-image-present.png'),fullPage:true});await page.getByRole('dialog').getByRole('button',{name:'Close presentation',exact:true}).click()
 await work.getByRole('button',{name:'History',exact:true}).click();await work.getByRole('button',{name:'Restore revision 1',exact:true}).click();await expect(work.locator('image')).toHaveCount(0);expect((await(await owner.request.get(endpoint())).json()).schema_version).toBe(1)
 await work.getByRole('button',{name:'Restore revision 2',exact:true}).click();await expect(work.locator('image[href^="blob:"]')).toHaveCount(1);expect((await(await owner.request.get(endpoint())).json()).schema_version).toBe(2)
 await check('Duplicate preserves private asset refs with fresh element IDs; populated Present and mixed schema history restore remain native')
 await readPage.goto(`${fixture.server}/pages/images-native`);const readWork=readPage.getByRole('region',{name:'Slides Page',exact:true});await expect(readWork.locator('image[href^="blob:"]')).toHaveCount(1)
 await expect(readPage.getByRole('button',{name:'Upload and insert image',exact:true})).toBeDisabled()
 const forbidden=await reader.request.post(`${fixture.server}/api/v1/pages/images-native/assets?workspace_id=${fixture.workspace}&mutation_id=reader_forbidden`,{data:await readFile(fixture.image_path),headers:{'Content-Type':'image/png'}});expect(forbidden.status()).toBe(403)
 await readWork.getByRole('button',{name:'Present',exact:true}).click();await expect(readPage.getByRole('dialog').locator('image[href^="blob:"]')).toHaveCount(1);await readPage.screenshot({path:join(output,'touch-private-image-present.png'),fullPage:true});await readPage.getByRole('dialog').getByRole('button',{name:'Close presentation',exact:true}).click()
 await readWork.getByRole('button',{name:'History',exact:true}).click();await readWork.getByRole('button',{name:'View revision 2',exact:true}).click();await expect(readWork.locator('image[href^="blob:"]')).toHaveCount(2)
 const revoked=await owner.request.delete(`${fixture.server}/api/v1/pages/images-native/grants?workspace_id=${fixture.workspace}&subject_type=user&subject=reader%40example.invalid`);expect(revoked.status()).toBe(200)
 await readWork.getByRole('button',{name:'Reload authorized images',exact:true}).click();await expect(readWork.locator('image[href^="blob:"]')).toHaveCount(0);await expect(readWork.getByText(/Image unavailable:/).first()).toBeVisible();await readPage.screenshot({path:join(output,'touch-history-images-revoked.png'),fullPage:true})
 expect(external).toEqual([]);await check('Distinct read-only identity sees authorized bytes but cannot upload; live revocation clears current and historical private image URLs')
}catch(error){for(const[i,ctx]of contexts.entries())for(const page of ctx.pages()){if(page.isClosed())continue;await page.screenshot({path:join(output,`failure-${i}.png`),fullPage:true}).catch(()=>{});await writeFile(join(output,`failure-${i}.txt`),`${page.url()}\n${await page.locator('body').innerText().catch(()=>'<unavailable>')}`)}throw error}finally{await browser.close()}
console.log(`Native images browser: ${results.length} checks PASS; evidence ${output}`)
