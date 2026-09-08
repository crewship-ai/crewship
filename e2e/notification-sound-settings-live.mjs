// Public Dev2 read-only settings QA using Emma's normal credentials.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/notification-sound-settings-live.mjs
import { chromium, expect } from '@playwright/test';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import path from 'node:path';
const statePath=process.env.TEAM_CHAT_STATE;if(!statePath)throw new Error('TEAM_CHAT_STATE required');
assert.equal((await fs.stat(statePath)).mode&0o077,0);
const state=JSON.parse(await fs.readFile(statePath,'utf8'));
const base='https://crewship-dev2.unifylab.cz';
const browser=await chromium.launch({headless:true});
const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
const report={server:base,checks:[],page_errors:[],audio_evidence:'Native oscillator starts in running AudioContext; physical speakers not measured.'};
const artifacts=path.resolve('docs/prd/reports/assets/sounds-settings-dev2');await fs.mkdir(artifacts,{recursive:true});
function passed(check){report.checks.push(check);console.log('PASS',check);}
async function request(url,options={}){
 for(let n=0;n<5;n++){
  const response=await ctx.request.fetch(url,options);if(response.status()!==429)return response;
  let ms=Math.max(Number(response.headers()['retry-after']||60)*1000,1000);assert.ok(Number.isFinite(ms)&&ms<=300000);
  while(ms>0){console.log('WAIT: respecting authentication Retry-After');const part=Math.min(ms,20000);await new Promise(r=>setTimeout(r,part));ms-=part;}
 }
 throw new Error('Authentication rate limit persisted');
}
const page=await ctx.newPage();page.on('pageerror',e=>report.page_errors.push(e.message));
try{
 const csrf=await request(base+'/api/auth/csrf');assert.equal(csrf.status(),200);
 const response=await request(base+'/api/auth/callback/credentials',{method:'POST',data:{email:state.accounts.emma.email,password:state.accounts.emma.password,csrfToken:(await csrf.json()).csrfToken,redirect:'false',json:'true'}});
 assert.equal(response.status(),200);assert.ok(!(await response.json()).error,'Login failed; sensitive response suppressed');
 await page.addInitScript(()=>{window.__soundStarts=[];const native=OscillatorNode.prototype.start;OscillatorNode.prototype.start=function(...args){const result=Reflect.apply(native,this,args);window.__soundStarts.push(this.context.state);return result;};});
 await page.goto(base+'/settings');
 const nav=page.getByRole('navigation',{name:'Settings sections'});
 await expect(nav.getByRole('button',{name:'Profile',exact:true})).toBeVisible();
 await nav.getByRole('button',{name:'Notification sounds',exact:true}).click();
 await page.waitForURL(url=>url.pathname==='/settings'&&url.searchParams.get('tab')==='sounds');
 const form=page.getByRole('region',{name:'Notification sounds',exact:true});await expect(form).toBeVisible();
 assert.equal(await page.getByRole('dialog').count(),0);
 await page.getByRole('button',{name:'User menu',exact:true}).click();
 await expect(page.getByRole('menuitem',{name:'Notification sounds',exact:true})).toHaveAttribute('href','/settings?tab=sounds');
 await page.keyboard.press('Escape');
 passed('Emma VIEWER opens Notification sounds under Settings Account; inline region and canonical profile-menu shortcut');
 assert.equal(await page.evaluate(()=>window.__soundStarts.length),0);
 await form.getByRole('button',{name:'Enable sounds',exact:true}).click();
 await expect(form.getByRole('status')).toContainText('Audio is ready');
 assert.equal(await page.evaluate(()=>window.__soundStarts.length),0);
 await form.getByRole('combobox',{name:'Chat sound'}).selectOption('glass');
 await form.getByRole('combobox',{name:'Inbox sound'}).selectOption('attention');
 await form.getByRole('button',{name:'Preview Chat sound'}).click();
 await expect.poll(()=>page.evaluate(()=>window.__soundStarts.length)).toBe(2);
 assert.ok((await page.evaluate(()=>window.__soundStarts)).every(state=>state==='running'));
 await form.getByRole('checkbox',{name:'Do not disturb'}).check();
 passed('Inline controls enable without playback and explicitly preview native Glass audio');
 await nav.getByRole('button',{name:'Profile',exact:true}).click();
 await expect(form).not.toBeVisible();
 await nav.getByRole('button',{name:'Notification sounds',exact:true}).click();
 await expect(form.getByRole('combobox',{name:'Chat sound'})).toHaveValue('glass');
 await expect(form.getByRole('combobox',{name:'Inbox sound'})).toHaveValue('attention');
 await expect(form.getByRole('checkbox',{name:'Do not disturb'})).toBeChecked();
 assert.equal(await page.evaluate(()=>window.__soundStarts.length),2);
 await form.getByRole('checkbox',{name:'Do not disturb'}).uncheck();
 await page.screenshot({path:path.join(artifacts,'desktop.png'),fullPage:true});
 passed('Profile and sound navigation preserve account/workspace preferences without extra playback');
 await page.reload();await expect(form).toBeVisible();
 await expect(form.getByRole('combobox',{name:'Chat sound'})).toHaveValue('glass');
 await expect(form.getByRole('button',{name:'Activate audio',exact:true})).toBeVisible();
 assert.equal(await page.evaluate(()=>window.__soundStarts.length),0);
 passed('Settings deep link and browser reload restore choices but require renewed audio activation');
 await page.setViewportSize({width:390,height:844});
 await page.getByRole('button',{name:'Open settings navigation',exact:true}).click();
 await page.getByRole('navigation',{name:'Settings sections'}).getByRole('button',{name:'Profile',exact:true}).click();
 await expect(form).not.toBeVisible();
 await page.getByRole('button',{name:'Open settings navigation',exact:true}).click();
 await page.getByRole('navigation',{name:'Settings sections'}).getByRole('button',{name:'Notification sounds',exact:true}).click();
 await expect(form).toBeVisible();await expect(page.getByRole('dialog')).not.toBeVisible();
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 await page.screenshot({path:path.join(artifacts,'mobile.png'),fullPage:true});
 assert.deepEqual(report.page_errors,[]);passed('Mobile Settings navigation reaches the same inline controls without overflow or runtime errors');
 report.status='passed';report.artifacts=artifacts;
}catch(error){report.status='failed';report.error=error.message;console.error(error.message);await page.screenshot({path:'/tmp/notification-sound-settings-live-failure.png',fullPage:true});process.exitCode=1;}
finally{await fs.writeFile('/tmp/notification-sound-settings-live-report.json',JSON.stringify(report,null,2));await ctx.close();await browser.close();}
