// Opt-in Dev2 acceptance: two real Mařena replies, native Web Audio, demo Emma only.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/agent-reply-sounds-live.mjs
import { chromium, expect } from '@playwright/test';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import path from 'node:path';
const statePath=process.env.TEAM_CHAT_STATE;if(!statePath)throw new Error('TEAM_CHAT_STATE required');
assert.equal((await fs.stat(statePath)).mode&0o077,0);
const state=JSON.parse(await fs.readFile(statePath,'utf8'));
const base='https://crewship-dev2.unifylab.cz';
const report={server:base,user_id:state.accounts.emma.user_id,checks:[],page_errors:[],audio_evidence:'Native oscillator starts in running AudioContexts; physical output not measured.'};
const browser=await chromium.launch({headless:true});const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
const artifacts=path.resolve('docs/prd/reports/assets/agent-reply-sounds-dev2');await fs.mkdir(artifacts,{recursive:true});
const wait=ms=>new Promise(r=>setTimeout(r,ms));
async function request(url,options={}){for(let n=0;n<5;n++){const r=await ctx.request.fetch(url,options);if(r.status()!==429)return r;let ms=Math.max(Number(r.headers()['retry-after']||60)*1000,1000);assert.ok(Number.isFinite(ms)&&ms<=300000);while(ms>0){console.log('WAIT: respecting authentication Retry-After');const part=Math.min(ms,20000);await wait(part);ms-=part;}}throw new Error('Authentication rate limit persisted');}
async function api(endpoint,data){const r=await request(base+'/api/v1/'+endpoint+(endpoint.includes('?')?'&':'?')+'workspace_id='+state.workspace_id,{method:data?'POST':'GET',...(data?{data}:{})});assert.ok(r.ok(),`Demo API ${endpoint.split('?')[0]} returned ${r.status()}`);return r.status()===204?null:r.json();}
function passed(check){report.checks.push(check);console.log('PASS',check);}
let pages=[];
async function count(){return(await Promise.all(pages.map(p=>p.evaluate(()=>window.__agentReplySounds.length)))).reduce((a,b)=>a+b,0);}
async function settings(page){await page.bringToFront();await page.getByRole('button',{name:'User menu',exact:true}).click();await expect(page.getByRole('menuitem',{name:'Notification sounds',exact:true})).toHaveAttribute('href','/settings?tab=sounds');await page.getByRole('menuitem',{name:'Notification sounds',exact:true}).click();await expect(page.getByRole('menu')).not.toBeVisible();await expect(page.getByRole('region',{name:'Notification sounds'})).toBeVisible();}
try{
 const csrf=await request(base+'/api/auth/csrf');assert.equal(csrf.status(),200);
 const auth=await request(base+'/api/auth/callback/credentials',{method:'POST',data:{email:state.accounts.emma.email,password:state.accounts.emma.password,csrfToken:(await csrf.json()).csrfToken,redirect:'false',json:'true'}});
 assert.equal(auth.status(),200);assert.ok(!(await auth.json()).error,'Login failed; sensitive response suppressed');
 const session=await request(base+'/api/auth/session');assert.equal((await session.json()).user.id,state.accounts.emma.user_id);
 await ctx.addInitScript(()=>{window.__agentReplySounds=[];const native=OscillatorNode.prototype.start;OscillatorNode.prototype.start=function(...args){const result=Reflect.apply(native,this,args);window.__agentReplySounds.push({at:Date.now(),state:this.context.state});return result;};});
 const agents=await api('agents?limit=500');const agent=agents.find(a=>a.slug==='ma-ena');assert.ok(agent?.id);
 const chat=await api(`agents/${agent.id}/chats`,{origin:'WEB'});assert.ok(chat.id);report.chat_id=chat.id;report.agent_id=agent.id;
 const url=base+`/chat/${agent.slug}?session=${chat.id}&workspace_id=${state.workspace_id}`;
 const a=await ctx.newPage(),b=await ctx.newPage();pages=[a,b];
 for(const p of pages){p.on('pageerror',e=>report.page_errors.push(e.message));await p.goto(url);await expect(p.getByPlaceholder('Message Mařena...')).toBeVisible({timeout:30000});}
 assert.equal(await count(),0);passed('Two normal Emma agent-chat tabs open an empty dedicated session without audio');
 for(const p of pages){await settings(p);const enable=p.getByRole('button',{name:'Enable sounds',exact:true});if(await enable.count())await enable.click();else{const activate=p.getByRole('button',{name:'Activate audio',exact:true});if(await activate.count())await activate.click();}await expect(p.getByRole('status').filter({hasText:'Audio is ready'})).toBeVisible();await p.getByRole('combobox',{name:'Chat sound'}).selectOption('soft-pop');await p.getByRole('combobox',{name:'Inbox sound'}).selectOption('chime');await p.goBack();await p.waitForURL(url);await expect(p.getByPlaceholder('Message Mařena...')).toBeVisible();}
 assert.equal(await count(),0);passed('Both tabs activate saved Chat sounds via profile menu without any preview or automatic cue');
 await a.bringToFront();const input=a.getByPlaceholder('Message Mařena...');await input.fill('Reply exactly SOUND_OK');await input.press('Enter');
 await expect(a.getByText('SOUND_OK',{exact:true})).toBeVisible({timeout:120000});
 await expect.poll(count,{timeout:15000}).toBe(1);await wait(3500);assert.equal(await count(),1);
 assert.ok((await Promise.all(pages.map(p=>p.evaluate(()=>window.__agentReplySounds)))).flat().every(entry=>entry.state==='running'));
 await a.screenshot({path:path.join(artifacts,'focused-agent.png'),fullPage:true});
 passed('A real completed Mařena reply plays one Chat cue even while its DM is focused, once across two tabs');
 // Remove every active ChatPanel before completion to exercise the Inbox path.
 await settings(b);await a.bringToFront();await input.fill('Reply exactly SOUND_AWAY_OK');await input.press('Enter');
 await settings(a);const beforeAway=await count();assert.equal(beforeAway,1,'Reply completed before leaving; away scenario did not execute');
 await expect.poll(async()=>{const data=await api(`chats/${chat.id}/messages?limit=100`);return data.messages?.some(message=>message.role==='assistant' && typeof message.content==='string' && message.content.trim()==='SOUND_AWAY_OK');},{timeout:120000,intervals:[1000,2000,3000]}).toBeTruthy();
 await expect.poll(count,{timeout:20000}).toBe(2);await wait(3500);assert.equal(await count(),2);
 passed('Away-from-chat real Mařena completion plays exactly one Chat cue through Inbox, not a second Inbox chime');
 await a.goBack();await a.waitForURL(url);await expect(a.getByText('SOUND_AWAY_OK',{exact:true})).toBeVisible({timeout:20000});await wait(2500);assert.equal(await count(),2);
 passed('Returning to completed agent history does not replay either reply');
 await a.setViewportSize({width:390,height:844});await settings(a);await expect(a.getByRole('region',{name:'Notification sounds'})).toBeVisible();assert.ok(await a.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await a.screenshot({path:path.join(artifacts,'mobile-profile-settings.png'),fullPage:true});
 assert.deepEqual(report.page_errors,[]);passed('Profile-menu sound shortcut is accessible on mobile and no browser runtime errors occurred');
 report.status='passed';report.artifacts=artifacts;
}catch(error){report.status='failed';report.error=error.message;console.error('Agent sound QA failed:',error.message);if(pages[0])await pages[0].screenshot({path:'/tmp/agent-reply-sounds-live-failure.png',fullPage:true});process.exitCode=1;}
finally{await fs.writeFile('/tmp/agent-reply-sounds-live-report.json',JSON.stringify(report,null,2));await ctx.close();await browser.close();}
