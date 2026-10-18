import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { readPrivateJson } from '../helpers/private-artifacts.mjs'
const execute=promisify(execFile)

// Shell-free subprocess; never print command output or credentials into reports.
export async function cli(config,role,args) {
  const env=Object.fromEntries(Object.entries(process.env).filter(([key])=>!key.startsWith('CREWSHIP_')))
  const {stdout}=await execute(config.binary,['--server',config.server,'--workspace',config.workspace,...args],{
    env:{...env,CREWSHIP_CONFIG:config.identities[role].cliConfig,CREWSHIP_NO_SLUG_CACHE:'1'},
    timeout:180_000,maxBuffer:4<<20,encoding:'utf8',killSignal:'SIGTERM',
  })
  return stdout
}

export function validateDocumentConfig(config) {
  assert.ok(config.binary?.startsWith('/'),'absolute real CLI binary required')
  for(const key of ['workspace','chatId','chatPath','agent','title','initialText','humanText']) assert.ok(typeof config[key]==='string' && config[key].length>0,`missing ${key}`)
  assert.ok(config.chatPath.startsWith('/') && !config.chatPath.startsWith('//'))
  const chatURL=new URL(config.chatPath,config.server)
  assert.equal(chatURL.origin,new URL(config.server).origin)
  assert.ok(!chatURL.searchParams.has('ref'),'chatPath must open the existing chat')
}

export async function documentAdapter(config,browser) {
  validateDocumentConfig(config)
  const contexts={}
  try {
    for(const role of ['OWNER','ADMIN','MEMBER']) {
      const state=await readPrivateJson(config.identities[role].storageState)
      contexts[role]=await browser.newContext({storageState:state})
      const session=await contexts[role].request.get(new URL('/api/auth/session',config.server).href)
      assert.equal(session.status(),200)
      assert.equal((await session.json()).user?.id,config.identities[role].userId)
    }
    const roster=await contexts.OWNER.request.get(new URL(`/api/v1/workspaces/${encodeURIComponent(config.workspace)}/members?workspace_id=${encodeURIComponent(config.workspace)}`,config.server).href)
    assert.equal(roster.status(),200)
    const members=await roster.json()
    for(const role of ['OWNER','ADMIN','MEMBER']) {
      const member=members.find(m=>m.user_id===config.identities[role].userId && m.role===role)
      assert.ok(member,'live membership must match configured role')
      const identity=JSON.parse(await cli(config,role,['whoami','--format','json']))
      assert.equal(identity.workspace?.id,config.workspace)
      assert.equal(identity.workspace?.role,role)
      assert.ok(member.user?.email)
      assert.equal(identity.user_email,member.user.email,'CLI and browser must represent the same human')
    }
  } catch(error) {await Promise.all(Object.values(contexts).map(c=>c.close()));throw error}
  let result,editorPage
  const read=async role=>JSON.parse(await cli(config,role,['doc','read',result.page_slug]))
  return {
    async agentCreate() {
      // The MEMBER is the requester and document owner. Precreate this private chat.
      const previous=JSON.parse(await cli(config,'MEMBER',['doc','chat-results',config.chatId]))
      assert.ok(!previous.results.some(r=>r.title===config.title),'use a fresh unique title')
      await cli(config,'MEMBER',['run',config.agent,'--chat',config.chatId,
        `Create one populated private Document titled ${JSON.stringify(config.title)} containing exactly this text: ${JSON.stringify(config.initialText)}. Use the supported document creation tool. Do not create any other resource.`])
      const saved=JSON.parse(await cli(config,'MEMBER',['doc','chat-results',config.chatId]))
      const matches=saved.results.filter(r=>r.title===config.title)
      assert.equal(matches.length,1)
      result=matches[0]
      assert.equal(result.type,'document')
      assert.equal(result.on_behalf_of_user_id,config.identities.MEMBER.userId)
      assert.ok(result.page_id && result.page_slug && result.run_id)
      const state=await read('MEMBER')
      assert.equal(state.page_id,result.page_id)
      assert.ok(JSON.stringify(state.content).includes(config.initialText))
      // CLI error is insufficient: require HTTP 404 for the same object as ADMIN.
      const path=`/api/v1/pages/${encodeURIComponent(result.page_slug)}/document?workspace_id=${encodeURIComponent(config.workspace)}`
      const admin=await contexts.ADMIN.request.get(new URL(path,config.server).href)
      assert.equal(admin.status(),404)
      const owner=await read('OWNER')
      assert.equal(owner.page_id,result.page_id)
      return {status:'passed',checks:['T-AG-01: CLI agent run created one durable Document','T-ACL-02: MEMBER requester, ADMIN 404, OWNER read'],source:{pageId:result.page_id,runId:result.run_id,chatId:config.chatId}}
    },
    async openChatPanel() {
      const page=await contexts.MEMBER.newPage()
      await page.goto(new URL(config.chatPath,config.server).href)
      const link=page.getByRole('link',{name:`Open document ${config.title}`,exact:true})
      await link.waitFor()
      const href=new URL(await link.getAttribute('href'),config.server)
      assert.equal(href.origin,new URL(config.server).origin)
      assert.equal(href.searchParams.get('ref'),result.page_id)
      const opened=contexts.MEMBER.waitForEvent('page')
      await link.click()
      editorPage=await opened
      await editorPage.getByLabel('Document editor',{exact:true}).waitFor()
      assert.ok((await editorPage.getByLabel('Document editor',{exact:true}).innerText()).includes(config.initialText))
      return {status:'passed',checks:['T-AG-01: saved card opened the created Document editor']}
    },
    async humanEditVersions() {
      if(!editorPage) return {status:'incomplete',reason:'chat card did not open editor'}
      const before=await read('MEMBER')
      const reply=editorPage.waitForResponse(r=>r.url().includes('/document/patch') && r.request().method()==='POST')
      await editorPage.getByLabel('Document editor',{exact:true}).fill(config.humanText)
      assert.equal((await reply).status(),200)
      await editorPage.getByRole('status').filter({hasText:/^Saved$/}).waitFor()
      const after=await read('MEMBER')
      assert.ok(after.rev>before.rev)
      assert.ok(JSON.stringify(after.content).includes(config.humanText))
      await editorPage.reload()
      const editor=editorPage.getByLabel('Document editor',{exact:true})
      await editor.waitFor()
      assert.equal(await editor.innerText(),config.humanText)
      return {status:'incomplete',reason:'Version compare/restore UI is not covered by this adapter',checks:['T-DOC-01: human edit persisted through reload','durable revision advanced (not checkpoint/resume acceptance)']}
    },
    async close(){await Promise.all(Object.values(contexts).map(c=>c.close()))},
  }
}
