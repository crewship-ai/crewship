import { readPrivateJson, createPrivateArtifacts } from './helpers/private-artifacts.mjs';
// Opt-in live Dev2 verification. Passwords are read only from protected seed state.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/team-chat-live.mjs
// Uses normal password + CSRF authentication; no minted/copy-pasted owner tokens.
import { chromium, expect } from '@playwright/test';
import fs from 'node:fs/promises';
import crypto from 'node:crypto';
import assert from 'node:assert/strict';
import path from 'node:path';
const statePath=process.env.TEAM_CHAT_STATE;
if (!statePath) throw new Error('TEAM_CHAT_STATE must identify protected seed state');
const state = await readPrivateJson(statePath);
assert.ok(state.channel_id && state.workspace_id);
const base='https://crewship-dev2.unifylab.cz';
const roles={thomas:'ADMIN',paul:'MANAGER',peter:'MEMBER',anna:'MANAGER',sofia:'MEMBER',emma:'VIEWER'};
const report={server:base,workspace_id:state.workspace_id,channel_id:state.channel_id,checks:[],actors:[],page_errors:[]};
const artifacts = await createPrivateArtifacts('team-chat-live');
const browser=await chromium.launch({headless:true});
const contexts=[];
async function boundedWait(ms,actor){
 while(ms>0){const part=Math.min(ms,20000);console.log(`WAIT ${actor}: respecting authentication Retry-After`);await new Promise(resolve=>setTimeout(resolve,part));ms-=part;}
}
async function request(ctx,url,options,actor){
 for(let attempt=0;attempt<5;attempt++){
  const r=await ctx.request.fetch(url,options);
  if(r.status()!==429)return r;
  const retry=r.headers()['retry-after'];
  const seconds=Number(retry);
  const wait=retry && Number.isFinite(seconds)?Math.max(seconds*1000,1000):retry && Number.isFinite(Date.parse(retry))?Math.max(Date.parse(retry)-Date.now(),1000):60000;
  assert.ok(wait<=300000,'Unexpected excessive Retry-After; refusing early retry');
  await boundedWait(wait,actor);
 }
 throw new Error(`Rate limit persisted for ${actor}; no authentication bypass attempted`);
}
async function login(key){
 const ctx=await browser.newContext({viewport:{width:1440,height:1100}});contexts.push(ctx);
 const csrf=await request(ctx,base+'/api/auth/csrf',{method:'GET'},key);assert.equal(csrf.status(),200);
 const token=(await csrf.json()).csrfToken;
 const account=state.accounts[key];assert.ok(account?.password && account?.email);
 const response=await request(ctx,base+'/api/auth/callback/credentials',{method:'POST',data:{email:account.email,password:account.password,csrfToken:token,redirect:'false',json:'true'}},key);
 assert.equal(response.status(),200,`Normal password login ${key}`);
 const result=await response.json();assert.ok(!result.error,`Login failed for ${key}; response suppressed`);
 const session=await request(ctx,base+'/api/auth/session',{method:'GET'},key);
 const user=(await session.json()).user;assert.equal(user.id,account.user_id);
 return ctx;
}
async function api(ctx,key,endpoint,data,method='GET'){
 return request(ctx,base+'/api/v1/'+endpoint+(endpoint.includes('?')?'&':'?')+'workspace_id='+encodeURIComponent(state.workspace_id),{method,...(data!==undefined?{data}:{})},key);
}
function passed(name){report.checks.push(name);console.log('PASS',name);}
try{
 let emma;
 const hashes=new Set();
 for(const [key,role] of Object.entries(roles)){
  const ctx=await login(key);if(key==='emma')emma=ctx;
  const account=state.accounts[key];
  const membersResponse=await api(ctx,key,`workspaces/${state.workspace_id}/members`);assert.equal(membersResponse.status(),200);
  const members=await membersResponse.json();
  const member=members.find(m=>m.user.id===account.user_id);assert.equal(member.role,role);
  assert.ok(member.user.avatar_url?.startsWith('/api/v1/users/'+account.user_id+'/avatar?'));
  const avatar=await request(ctx,base+member.user.avatar_url,{method:'GET'},key);assert.equal(avatar.status(),200);assert.match(avatar.headers()['content-type'],/^image\/png/);
  const bytes=await avatar.body();assert.equal(bytes.subarray(0,8).toString('hex'),'89504e470d0a1a0a');
  const hash=crypto.createHash('sha256').update(bytes).digest('hex');hashes.add(hash);
  const bundled=await fs.readFile(`cmd/crewship/seeddata/team-avatars/${key}.png`);
  assert.equal(hash,crypto.createHash('sha256').update(bundled).digest('hex'));
  const messagesResponse=await api(ctx,key,`conversations/${state.channel_id}/messages`);assert.equal(messagesResponse.status(),200);
  const messages=(await messagesResponse.json()).messages;
  const examples=messages.filter(m=>m.client_id==='team-chat-demo-planning-v1');
  assert.equal(examples.length,6);assert.equal(new Set(examples.map(m=>m.author_user_id)).size,6);
  assert.equal(messages.filter(m=>m.client_id==='team-chat-demo-owner-v1').length,1);
  const own=examples.find(m=>m.author_user_id===account.user_id);assert.ok(own);assert.equal(own.author_avatar_url,member.user.avatar_url);
  // A retry verifies Chat write access without adding test noise to the channel.
  const retry=await api(ctx,key,`conversations/${state.channel_id}/messages`,{client_id:own.client_id,content:own.content,mentioned_agent_ids:[]},'POST');assert.equal(retry.status(),200);assert.equal((await retry.json()).id,own.id);
  const provisioning=await api(ctx,key,`workspaces/${state.workspace_id}/members/provision`,{},'POST');assert.equal(provisioning.status(),role==='ADMIN'?400:403);
  const issue=await api(ctx,key,'crews/does-not-exist-team-demo-rbac/issues/NONE-0',{},'PATCH');assert.equal(issue.status(),['ADMIN','MANAGER'].includes(role)?404:403);
  const roleChange=await api(ctx,key,`workspaces/${state.workspace_id}/members/does-not-exist-team-demo-rbac`,{role:'VIEWER'},'PATCH');assert.equal(roleChange.status(),['ADMIN','MANAGER'].includes(role)?404:403);
  const activity=await api(ctx,key,`conversations/${state.channel_id}/activity`,{issues:false,routines:false},'PUT');assert.equal(activity.status(),404);
  const other=state.accounts[key==='thomas'?'emma':'thomas'];
  const forbidden=await api(ctx,key,`conversations/${other.direct_conversation_id}/messages`);assert.equal(forbidden.status(),404);
  report.actors.push({key,role,user_id:account.user_id,avatar_sha256:hash});
  passed(`${key}: real ${role}, own PNG avatar, idempotent Chat send, role gates and private-DM denial`);
 }
 assert.equal(hashes.size,6);passed('Six distinct bundled PNG avatars and seven seeded channel messages');
 const page=await emma.newPage();page.on('pageerror',e=>report.page_errors.push(e.message));
 await page.goto(base+`/chat?conversation=${state.channel_id}&workspace_id=${state.workspace_id}`);
 await expect(page.getByRole('heading',{name:'Team lounge · demo',exact:true})).toBeVisible({timeout:30000});
 await page.mouse.move(900,600);
 for(const key of Object.keys(roles)){
  const article=page.getByRole('article',{name:new RegExp('^'+key[0].toUpperCase()+key.slice(1)+' · demo')});
  await expect(article).toBeAttached();
  const portrait=article.locator('img');await expect(portrait).toBeAttached();
  await expect.poll(()=>portrait.evaluate(img=>img.complete && img.naturalWidth>0)).toBe(true);
 }
 await page.screenshot({path:path.join(artifacts,'desktop.png'),fullPage:true});
 await page.setViewportSize({width:390,height:844});
 await page.getByRole('article',{name:/Emma · demo/}).scrollIntoViewIfNeeded();
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 await page.screenshot({path:path.join(artifacts,'mobile.png'),fullPage:true});
 await page.getByRole('button',{name:'People',exact:true}).last().click();
 const dialog=page.getByRole('dialog');await expect(dialog).toBeVisible();
 for(const key of Object.keys(roles))await expect(dialog.getByText(key[0].toUpperCase()+key.slice(1)+' · demo',{exact:true})).toBeAttached();
 await page.screenshot({path:path.join(artifacts,'mobile-people.png'),fullPage:true});
 assert.deepEqual(report.page_errors,[]);passed('Emma VIEWER sees six loaded portraits, roster and responsive Chat without browser errors');
 report.status='passed';report.artifacts=artifacts;
}finally{
 await fs.writeFile(path.join(artifacts, 'chat-seed-team-live-validation.json'),JSON.stringify(report,null,2));
 for(const ctx of contexts)await ctx.close();await browser.close();
}
