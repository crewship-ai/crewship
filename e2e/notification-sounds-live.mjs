// Opt-in live Dev2 sound test. Uses native audio nodes and normal demo logins.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/notification-sounds-live.mjs
import { chromium, expect } from '@playwright/test';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import path from 'node:path';
const statePath=process.env.TEAM_CHAT_STATE;
if(!statePath)throw new Error('TEAM_CHAT_STATE must point to private demo credentials');
assert.equal((await fs.stat(statePath)).mode&0o077,0);
const state=JSON.parse(await fs.readFile(statePath,'utf8'));
const base='https://crewship-dev2.unifylab.cz';
const report={server:base,workspace_id:state.workspace_id,checks:[],page_errors:[],audio_evidence:'Native OscillatorNode.start calls in running AudioContexts; physical speaker output is not measured.'};
const browser=await chromium.launch({headless:true});
const contexts=[];
const artifacts=path.resolve('docs/prd/reports/assets/notification-sounds-dev2');await fs.mkdir(artifacts,{recursive:true});
const wait=ms=>new Promise(r=>setTimeout(r,ms));
async function request(ctx,url,options={}){
 for(let attempt=0;attempt<5;attempt++){
  const response=await ctx.request.fetch(url,options);
  if(response.status()!==429)return response;
  const raw=response.headers()['retry-after'];const seconds=Number(raw);
  let ms=raw&&Number.isFinite(seconds)?Math.max(seconds*1000,1000):60000;
  assert.ok(ms<=300000);
  while(ms>0){console.log('WAIT: respecting server authentication Retry-After');const part=Math.min(ms,20000);await wait(part);ms-=part;}
 }
 throw new Error('Rate limit persisted; no bypass attempted');
}
async function login(key){
 const ctx=await browser.newContext({viewport:{width:1440,height:1000}});contexts.push(ctx);
 const csrf=await request(ctx,base+'/api/auth/csrf');assert.equal(csrf.status(),200);
 const token=(await csrf.json()).csrfToken;const account=state.accounts[key];
 const response=await request(ctx,base+'/api/auth/callback/credentials',{method:'POST',data:{email:account.email,password:account.password,csrfToken:token,redirect:'false',json:'true'}});
 assert.equal(response.status(),200);assert.ok(!(await response.json()).error,'Password login failed; response suppressed');return ctx;
}
async function api(ctx,endpoint,data,method){
 const response=await request(ctx,base+'/api/v1/'+endpoint+(endpoint.includes('?')?'&':'?')+'workspace_id='+state.workspace_id,{method:method||(data?'POST':'GET'),...(data?{data}:{})});
 assert.ok(response.ok(),`Demo API ${endpoint.split('?')[0]}: HTTP ${response.status()}`);
 return response.status()===204?null:response.json();
}
function passed(name){report.checks.push(name);console.log('PASS',name);}
let pages=[];
async function starts(){return (await Promise.all(pages.map(p=>p.evaluate(()=>window.__notificationAudioStarts.length)))).reduce((a,b)=>a+b,0);}
async function settings(page){await page.bringToFront();await page.getByRole('link',{name:'Notification sounds',exact:true}).click();await expect(page.getByRole('region',{name:'Notification sounds'})).toBeVisible();}
async function closeSettings(page){await page.goBack();await expect(page.getByRole('region',{name:'Notification sounds'})).not.toBeVisible();}
async function unlock(page){
 await settings(page);
 const enable=page.getByRole('button',{name:'Enable sounds',exact:true});
 if(await enable.count())await enable.click();else{const activate=page.getByRole('button',{name:'Activate audio',exact:true});if(await activate.count())await activate.click();}
 await expect(page.getByRole('status').filter({hasText:'Audio is ready'})).toBeVisible();
 await closeSettings(page);
}
try{
 const thomas=await login('thomas');const emma=await login('emma');
 await emma.addInitScript(()=>{
  window.__notificationAudioStarts=[];
  const original=OscillatorNode.prototype.start;
  OscillatorNode.prototype.start=function(...args){
   const result=Reflect.apply(original,this,args);
   window.__notificationAudioStarts.push({at:Date.now(),state:this.context.state,frequency:this.frequency.value});
   return result;
  };
 });
 const stamp=Date.now();
 const room=await api(thomas,'conversations',{title:`Notification sound QA · ${stamp}`,kind:'group',member_ids:[state.accounts.emma.user_id]});
 report.conversation_id=room.id;
 let messageNo=0;
 async function send(ctx,label){return api(ctx,`conversations/${room.id}/messages`,{client_id:`sound-qa-${stamp}-${++messageNo}`,content:`SOUND QA: ${label}. Synthetic test only.`,mentioned_agent_ids:[]});}
 await send(thomas,'historical baseline before audio was enabled');
 const a=await emma.newPage(),b=await emma.newPage();pages=[a,b];
 for(const p of pages){p.on('pageerror',e=>report.page_errors.push(e.message));await p.goto(base+`/chat?conversation=${state.accounts.emma.direct_conversation_id}&workspace_id=${state.workspace_id}`);await expect(p.getByRole('heading',{name:'Demo User',exact:true})).toBeVisible();}
 assert.equal(await starts(),0);passed('Initial history and two fresh tabs do not create or play audio');
 await unlock(a);await unlock(b);assert.equal(await starts(),0);
 await settings(a);
 for(const [id,count]of [['soft-pop',1],['glass',2],['chime',3],['inbox-drop',2],['attention',2]]){
  await a.getByRole('combobox',{name:'Chat sound'}).selectOption(id);const before=await starts();
  await a.getByRole('button',{name:'Preview Chat sound'}).click();
  await expect.poll(starts).toBe(before+count);await wait(650);
 }
 await a.getByRole('combobox',{name:'Chat sound'}).selectOption('soft-pop');
 await a.screenshot({path:path.join(artifacts,'desktop.png'),fullPage:true});
 await closeSettings(a);
 for(const p of pages)assert.ok((await p.evaluate(()=>window.__notificationAudioStarts)).every(entry=>entry.state==='running'));
 passed('All five previews start their real native audio nodes; Enable itself stays silent');
 await wait(2300);let before=await starts();
 await send(thomas,'new human message while both recipient tabs read a different conversation');
 await expect.poll(starts,{timeout:15000}).toBe(before+1);await wait(2500);assert.equal(await starts(),before+1);
 passed('A real WebSocket-delivered human message plays exactly one soft-pop across two tabs');
 await wait(2300);before=await starts();await send(emma,'own message stays silent');await wait(3000);assert.equal(await starts(),before);
 passed('Own human messages do not play a notification');
 async function silence(label,change,restore){
  await settings(a);await change(a);await closeSettings(a);await wait(2300);
  const before=await starts();await send(thomas,label);await wait(3000);assert.equal(await starts(),before,label);
  await settings(a);await restore(a);await closeSettings(a);passed(label);
 }
 await silence('Master Disable sounds suppresses incoming messages',p=>p.getByRole('button',{name:'Disable sounds',exact:true}).click(),p=>p.getByRole('button',{name:'Enable sounds',exact:true}).click());
 await silence('Do not disturb suppresses incoming messages',p=>p.getByRole('checkbox',{name:'Do not disturb'}).check(),p=>p.getByRole('checkbox',{name:'Do not disturb'}).uncheck());
 await silence('Chat preset Off suppresses incoming messages',p=>p.getByRole('combobox',{name:'Chat sound'}).selectOption('off'),p=>p.getByRole('combobox',{name:'Chat sound'}).selectOption('soft-pop'));
 const volume=async(p,value)=>{const slider=p.getByRole('slider',{name:'Notification volume'});await slider.focus();await slider.press('Home');for(let i=0;i<value;i++)await slider.press('ArrowRight');};
 await silence('Zero volume suppresses incoming messages',p=>volume(p,0),p=>volume(p,35));
 await wait(2300);await api(emma,`conversations/${room.id}/mute`,{muted:true});before=await starts();await send(thomas,'muted conversation stays silent');await wait(3000);assert.equal(await starts(),before);await api(emma,`conversations/${room.id}/mute`,{muted:false});
 passed('Personal room mute suppresses incoming messages in both tabs');
 await a.goto(base+`/chat?conversation=${room.id}&workspace_id=${state.workspace_id}`);await expect(a.getByRole('heading',{name:room.title,exact:true})).toBeVisible();await unlock(a);
 await a.bringToFront();await a.getByRole('textbox',{name:/^Message /}).click();
 await expect.poll(()=>a.evaluate(()=>document.hasFocus())).toBe(true);await wait(2300);
 before=await starts();const focused=await send(thomas,'actively read conversation stays silent');await expect(a.getByText(focused.content,{exact:true})).toBeVisible({timeout:10000});await wait(3000);assert.equal(await starts(),before);
 passed('Actively focused conversation at latest message stays silent across tabs');
 await a.reload();await expect(a.getByRole('heading',{name:room.title,exact:true})).toBeVisible();assert.equal(await a.evaluate(()=>window.__notificationAudioStarts.length),0);
 before=await starts();await unlock(a);await wait(3000);assert.equal(await starts(),before);
 passed('Reload and renewed audio activation never replay historical messages');
 await settings(a);await a.setViewportSize({width:390,height:844});assert.ok(await a.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await a.screenshot({path:path.join(artifacts,'mobile.png'),fullPage:true});await closeSettings(a);
 assert.deepEqual(report.page_errors,[]);passed('Desktop/mobile personal settings are accessible with no browser runtime errors');
 report.status='passed';report.artifacts=artifacts;
}catch(error){report.status='failed';report.error=error.message;console.error('Sound QA failed:',error.message);if(pages[0])await pages[0].screenshot({path:'/tmp/notification-sounds-live-failure.png',fullPage:true});process.exitCode=1;}
finally{await fs.writeFile('/tmp/notification-sounds-live-report.json',JSON.stringify(report,null,2));for(const ctx of contexts)await ctx.close();await browser.close();}
