// Dev2-only acceptance. Fixtures are synthetic uploads, never agent-generated claims.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/files-preview-live.mjs
import {chromium,expect} from '@playwright/test';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import path from 'node:path';
import crypto from 'node:crypto';
const statePath=process.env.TEAM_CHAT_STATE;assert.ok(statePath,'TEAM_CHAT_STATE required');
assert.equal((await fs.stat(statePath)).mode&0o077,0);
const state=JSON.parse(await fs.readFile(statePath,'utf8'));
const manifest=JSON.parse(await fs.readFile(process.env.FILES_PREVIEW_MANIFEST??'/tmp/files-preview-demo-manifest.json','utf8'));
const base='https://crewship-dev2.unifylab.cz';assert.equal(manifest.server,base);
const artifacts=path.resolve('docs/prd/reports/assets/files-preview-dev2');await fs.mkdir(artifacts,{recursive:true});
const report={server:base,user_id:state.accounts.emma.user_id,fixture_directory:manifest.directory,fixture_origin:'Synthetic QA PDF/image/code uploaded by supported owner CLI; no agent execution.',checks:[],page_errors:[],failed_resources:[],pdf_assets:[]};
const browser=await chromium.launch({headless:true});const context=await browser.newContext({viewport:{width:1500,height:1000}});const page=await context.newPage();
page.on('request',r=>{if(r.url().includes('/pdfjs/'))report.pdf_assets.push(r.url());});
page.on('pageerror',e=>report.page_errors.push(e.message));page.on('response',r=>{if(r.status()>=400&&(r.url().includes('/pdfjs/')||r.url().includes('/files/')))report.failed_resources.push({url:new URL(r.url()).pathname,status:r.status()});});
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function request(url,options={}){for(let attempt=0;attempt<5;attempt++){const response=await context.request.fetch(url,options);if(response.status()!==429)return response;let delay=Math.max(1000,Number(response.headers()['retry-after']??60)*1000);assert.ok(Number.isFinite(delay)&&delay<=300000);while(delay>0){console.log('WAIT: respecting authentication rate limit');const part=Math.min(delay,20000);await sleep(part);delay-=part;}}throw Error('Authentication rate limit persisted');}
async function api(route){const r=await request(base+'/api/v1/'+route+(route.includes('?')?'&':'?')+'workspace_id='+state.workspace_id);assert.ok(r.ok(),`Read-only API ${route.split('?')[0]} returned ${r.status()}`);return r.json();}
function pass(text){report.checks.push(text);console.log('PASS',text);}
async function file(name){await page.getByRole('button',{name:new RegExp('^'+name.replace(/[.*+?^${}()|[\]\\]/g,'\\$&'))}).click();}
async function openAgentPDF(){
 const target=page.getByRole('button',{name:/^demo-preview\.pdf/});
 for(let n=0;n<3;n++){
  if(await target.isVisible())break;
  const folder=page.getByRole('button',{name:manifest.directory,exact:true});await expect(folder).toBeVisible();
  if(await folder.locator('.lucide-chevron-right').count())await folder.click();
  try{await expect(target).toBeVisible({timeout:5000});break;}catch(error){if(n===2)throw error;}
 }
 await target.click();
}
async function canvasHash(label){return page.locator(`canvas[aria-label="${label}"]`).evaluate(c=>c.toDataURL());}
try{
 const csrf=await request(base+'/api/auth/csrf');assert.equal(csrf.status(),200);
 const login=await request(base+'/api/auth/callback/credentials',{method:'POST',data:{email:state.accounts.emma.email,password:state.accounts.emma.password,csrfToken:(await csrf.json()).csrfToken,redirect:'false',json:'true'}});assert.equal(login.status(),200);assert.ok(!(await login.json()).error,'Login failed; sensitive response suppressed');
 const session=await request(base+'/api/auth/session');assert.equal((await session.json()).user.id,state.accounts.emma.user_id);
 const agents=await api('agents?limit=500');const agent=agents.find(a=>a.slug==='ma-ena');assert.ok(agent?.id);report.agent_id=agent.id;
 const chats=await api(`agents/${agent.id}/chats`);const rows=Array.isArray(chats)?chats:chats.chats;assert.ok(rows?.length,'An existing Mařena session is required');
 await page.goto(base+`/chat/ma-ena?session=${rows[0].id}&workspace_id=${state.workspace_id}`);
 await expect(page.getByPlaceholder('Message Mařena...')).toBeVisible({timeout:30000});await page.waitForLoadState('networkidle');
 const filesTab=page.getByRole('tab',{name:/^Files/});if(await filesTab.getAttribute('aria-selected')!=='true')await filesTab.click();
 await page.waitForLoadState('networkidle');await openAgentPDF();
 const pdf=page.getByRole('region',{name:'Preview demo-preview.pdf'});await expect(pdf).toBeVisible();
 await expect(pdf.getByText('Page 1 of 2',{exact:true})).toBeVisible({timeout:30000});await expect(pdf.getByText('Rendering PDF…')).not.toBeVisible({timeout:30000});
 const first=await canvasHash('PDF page 1');assert.ok(first.length>1000);await expect(pdf.getByRole('button',{name:'Previous page',exact:true})).toBeDisabled();
 await pdf.getByRole('button',{name:'Next page',exact:true}).click();await expect(pdf.getByText('Page 2 of 2',{exact:true})).toBeVisible();await expect(pdf.getByText('Rendering PDF…')).not.toBeVisible();assert.notEqual(await canvasHash('PDF page 2'),first);
 await expect(pdf.getByRole('button',{name:'Next page',exact:true})).toBeDisabled();await pdf.getByRole('button',{name:'Zoom in',exact:true}).click();await expect(pdf.getByText('125%',{exact:true})).toBeVisible();await expect(pdf.getByText('Rendering PDF…')).not.toBeVisible();
 await page.screenshot({path:path.join(artifacts,'agent-pdf-page-two.png'),fullPage:true});assert.ok(report.pdf_assets.some(url=>url.includes('pdf.worker')));assert.ok(report.pdf_assets.every(url=>new URL(url).origin===base));pass('Emma VIEWER opens a real two-page PDF in Files, renders distinct pages and zooms with same-origin PDF assets');
 const downloaded=page.waitForEvent('download');await pdf.getByRole('button',{name:'Download file',exact:true}).click();const download=await downloaded;assert.equal(download.suggestedFilename(),'demo-preview.pdf');
 const downloadedBytes=await fs.readFile(await download.path());const original=await fs.readFile(path.join(manifest.local,'demo-preview.pdf'));assert.equal(crypto.createHash('sha256').update(downloadedBytes).digest('hex'),crypto.createHash('sha256').update(original).digest('hex'));pass('PDF download preserves exact uploaded bytes and filename');
 await pdf.getByRole('button',{name:'Back to files'}).click();await file('demo-preview.png');const image=page.getByRole('img',{name:'demo-preview.png'});await expect(image).toBeVisible();await expect.poll(()=>image.evaluate(img=>img.complete&&img.naturalWidth===900&&img.naturalHeight===500)).toBeTruthy();await page.screenshot({path:path.join(artifacts,'agent-image.png'),fullPage:true});pass('PNG preview decodes the uploaded 900×500 image');
 await page.getByRole('button',{name:'Back to files'}).click();await file('demo-preview.ts');await expect(page.locator('.cm-content')).toContainText('export const previewDemo');pass('Code files keep the existing text editor preview');
 // Closing the text editor leaves the independent Crew section available.
 const crew=page.getByRole('button',{name:'Crew',exact:true});if(await crew.getAttribute('aria-expanded')!=='true')await crew.click();
 const crewScope=crew.locator('..');await crewScope.getByRole('button',{name:'ma-ena',exact:true}).click();await crewScope.getByRole('button',{name:manifest.directory,exact:true}).click();await crewScope.getByRole('button',{name:/^demo-preview.pdf/}).click();
 const crewPDF=page.getByRole('region',{name:'Preview demo-preview.pdf'});await expect(crewPDF.getByText('Page 1 of 2',{exact:true})).toBeVisible({timeout:30000});await expect(crewPDF.getByText('Rendering PDF…')).not.toBeVisible();pass('The same output PDF opens through the Crew tree and crew-scoped download route');
 report.limitations=['Crew root listing currently exposes the output tree; actual shared-volume sample is readable with CLI --path shared but is not discoverable from that UI root.'];
 await page.setViewportSize({width:390,height:844});
 await page.getByRole('tablist',{name:'Chat panel',exact:true}).getByRole('tab',{name:'Files',exact:true}).click();
 await page.waitForLoadState('networkidle');await openAgentPDF();
 const mobilePDF=page.getByRole('region',{name:'Preview demo-preview.pdf'});await expect(mobilePDF.getByText('Page 1 of 2',{exact:true})).toBeVisible({timeout:30000});await expect(mobilePDF.getByText('Rendering PDF…')).not.toBeVisible();await mobilePDF.getByRole('button',{name:'Next page',exact:true}).click();await expect(mobilePDF.getByText('Page 2 of 2',{exact:true})).toBeVisible();await expect(mobilePDF.getByText('Rendering PDF…')).not.toBeVisible();
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.screenshot({path:path.join(artifacts,'mobile-preview.png'),fullPage:true});pass('At 390px mobile width, Files tab opens PDF preview, renders page two and does not overflow the document');
 assert.deepEqual(report.page_errors,[]);assert.deepEqual(report.failed_resources,[]);report.status='passed';
}catch(error){report.status='failed';report.error=error.message;console.error('Files preview QA failed:',error.message);await page.screenshot({path:'/tmp/files-preview-live-failure.png',fullPage:true});process.exitCode=1;}
finally{await fs.writeFile('/tmp/files-preview-live-report.json',JSON.stringify(report,null,2));await context.close();await browser.close();}
