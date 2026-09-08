// Dev2 saved-opt-in regression: one real reply, one recipient tab, native audio.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/agent-reply-reactivation-live.mjs
import { chromium, expect } from '@playwright/test';
import fs from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
const statePath=process.env.TEAM_CHAT_STATE;if(!statePath)throw new Error('TEAM_CHAT_STATE required');
assert.equal((await fs.stat(statePath)).mode&0o077,0);
const state=JSON.parse(await fs.readFile(statePath,'utf8'));
const base='https://crewship-dev2.unifylab.cz';
const chatId=process.env.AGENT_SOUND_CHAT_ID||'cmtsftvcj0002380b7512';
const browser=await chromium.launch({headless:true});const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
const page=await ctx.newPage();
const report={server:base,chat_id:chatId,checks:[],page_errors:[],audio_evidence:'Native oscillator started in a running AudioContext after an ordinary composer gesture; no post-reload Activate/Preview.'};
const artifacts=path.resolve('docs/prd/reports/assets/agent-reply-sounds-dev2');await fs.mkdir(artifacts,{recursive:true});
page.on('pageerror',e=>report.page_errors.push(e.message));
const wait=ms=>new Promise(r=>setTimeout(r,ms));
function passed(check){report.checks.push(check);console.log('PASS',check);}
async function request(url,options={}){for(let n=0;n<5;n++){const r=await ctx.request.fetch(url,options);if(r.status()!==429)return r;let ms=Math.max(Number(r.headers()['retry-after']||60)*1000,1000);assert.ok(Number.isFinite(ms)&&ms<=300000);while(ms>0){console.log('WAIT: respecting authentication Retry-After');const part=Math.min(ms,20000);await wait(part);ms-=part;}}throw new Error('Authentication rate limit persisted');}
try{
 const csrf=await request(base+'/api/auth/csrf');assert.equal(csrf.status(),200);
 const login=await request(base+'/api/auth/callback/credentials',{method:'POST',data:{email:state.accounts.emma.email,password:state.accounts.emma.password,csrfToken:(await csrf.json()).csrfToken,redirect:'false',json:'true'}});assert.equal(login.status(),200);assert.ok(!(await login.json()).error,'Login failed; sensitive response suppressed');
 await page.addInitScript(()=>{window.__reactivationSounds=[];const native=OscillatorNode.prototype.start;OscillatorNode.prototype.start=function(...args){const result=Reflect.apply(native,this,args);window.__reactivationSounds.push(this.context.state);return result;};});
 await page.goto(base+'/settings?tab=sounds');const form=page.getByRole('region',{name:'Notification sounds'});await expect(form).toBeVisible();
 await form.getByRole('button',{name:'Enable sounds',exact:true}).click();await expect(form.getByRole('status')).toContainText('Audio is ready');await form.getByRole('combobox',{name:'Chat sound'}).selectOption('soft-pop');
 assert.equal(await page.evaluate(()=>window.__reactivationSounds.length),0);
 await page.goto(base+`/chat/ma-ena?session=${chatId}&workspace_id=${state.workspace_id}`);
 const composer=page.getByPlaceholder('Message Mařena...');await expect(composer).toBeVisible({timeout:30000});
 await page.reload();await expect(composer).toBeVisible();assert.equal(await page.evaluate(()=>window.__reactivationSounds.length),0);
 passed('Saved enabled preference survives a real reload without automatic playback');
 // These are the only post-reload gestures: ordinary use of the composer.
 await composer.click();await composer.fill('Reply exactly SOUND_REACTIVATE_OK');await composer.press('Enter');
 await expect(page.getByText('SOUND_REACTIVATE_OK',{exact:true}).last()).toBeVisible({timeout:120000});
 await expect.poll(()=>page.evaluate(()=>window.__reactivationSounds.length),{timeout:15000}).toBe(1);
 await wait(3000);assert.equal(await page.evaluate(()=>window.__reactivationSounds.length),1);assert.deepEqual(await page.evaluate(()=>window.__reactivationSounds),['running']);
 passed('One real Mařena reply plays after ordinary composer interaction, without Activate or Preview after reload');
 await page.setViewportSize({width:390,height:844});await page.getByRole('button',{name:'User menu',exact:true}).click();
 const menu=page.getByRole('menu');await expect(menu).toBeVisible();await expect(menu.getByText('Viewer',{exact:true})).toBeVisible();assert.equal(await menu.getByText('Owner',{exact:true}).count(),0);await expect(menu.getByText("Demo User's Workspace",{exact:true})).toBeVisible();
 await expect(page.getByRole('menuitem',{name:'Notification sounds',exact:true})).toHaveAttribute('href','/settings?tab=sounds');
 await page.screenshot({path:path.join(artifacts,'mobile-profile-menu.png'),fullPage:true});
 await page.getByRole('menuitem',{name:'Notification sounds',exact:true}).click();await expect(menu).not.toBeVisible();await expect(form).toBeVisible();assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 await page.screenshot({path:path.join(artifacts,'mobile-profile-settings.png'),fullPage:true});
 assert.deepEqual(report.page_errors,[]);passed('Mobile profile menu shows actual Viewer role/workspace and opens inline sound settings without overflow');
 report.status='passed';report.artifacts=artifacts;
}catch(error){report.status='failed';report.error=error.message;console.error('Reactivation QA failed:',error.message);await page.screenshot({path:'/tmp/agent-reply-reactivation-live-failure.png',fullPage:true});process.exitCode=1;}
finally{await fs.writeFile('/tmp/agent-reply-reactivation-live-report.json',JSON.stringify(report,null,2));await ctx.close();await browser.close();}
