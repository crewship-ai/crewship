import assert from 'node:assert/strict'
import { test } from 'node:test'
import { runContract, validateConfig, STEPS } from './contract.mjs'
const config = () => ({type:'document',variant:'deterministic-fixture',server:'http://127.0.0.1:1234',dedicatedTarget:true,
  identities:Object.fromEntries(['OWNER','ADMIN','MEMBER'].map(role=>[role,{userId:role,cliConfig:`/tmp/${role}`,storageState:`/tmp/${role}.json`}]))})
test('requires distinct identities and explicit target and variant',()=>{
  assert.doesNotThrow(()=>validateConfig(config()))
  for(const mutate of [c=>c.identities.ADMIN.userId='OWNER',c=>delete c.identities.MEMBER,c=>c.variant='live',c=>c.dedicatedTarget=false,c=>c.server='http://192.168.1.202']) {
    const c=config();mutate(c);assert.throws(()=>validateConfig(c))
  }
})
test('seven ordered steps, unknown types and absent adapters stay incomplete',async()=>{
  const result=await runContract({...config(),type:'slides'}, {})
  assert.deepEqual(result.steps.map(s=>s.id),STEPS)
  assert.equal(result.status,'incomplete')
  assert.ok(result.steps.every(s=>s.status==='incomplete'))
  assert.equal(result.liveAcceptance,false)
})
test('partial evidence cannot pass a composite step',async()=>{
  const result=await runContract(config(),{humanEditVersions:async()=>({status:'incomplete',reason:'versions unavailable',checks:['edit persisted']})})
  assert.equal(result.steps[3].status,'incomplete')
  assert.deepEqual(result.steps[3].checks,['edit persisted'])
})
test('failures do not hide unexecuted steps, invalid statuses fail',async()=>{
  const result=await runContract(config(),{agentCreate:async()=>{throw Error('secret response')},openChatPanel:async()=>({status:'skipped'})})
  assert.equal(result.steps[0].status,'failed')
  assert.equal(result.steps[0].reason,'adapter operation failed')
  assert.equal(result.steps[1].status,'incomplete')
  assert.equal(result.steps.length,7)
  assert.equal(result.status,'failed')
})
test('empty pass is rejected and synthetic success never becomes live evidence',async()=>{
  const bad=await runContract(config(),{agentCreate:async()=>({status:'passed',checks:[]})})
  assert.equal(bad.steps[0].status,'failed')
  const adapter=Object.fromEntries(STEPS.map(id=>[id,async()=>({status:'passed',checks:['verified operation']})]))
  const result=await runContract(config(),adapter)
  assert.equal(result.status,'passed');assert.equal(result.liveAcceptance,false)
})
test('manual variant requires provider designation and reports incomplete truthfully',async()=>{
  const c={...config(),variant:'manual-sonnet-5.5'}
  assert.throws(()=>validateConfig(c))
  c.providerModel='Sonnet 5.5'
  const result=await runContract(c,{})
  assert.equal(result.variant,'manual-sonnet-5.5');assert.equal(result.liveAcceptance,false)
})

test('CLI transport invokes a binary directly and pins identity and target',async()=>{
  const {mkdtemp,writeFile,rm}=await import('node:fs/promises')
  const {tmpdir}=await import('node:os')
  const {join}=await import('node:path')
  const {cli}=await import('./document.mjs')
  const root=await mkdtemp(join(tmpdir(),'artifact-contract-transport-'))
  try {
    // This tiny binary is transport-unit evidence, never a real Crewship CLI pass.
    const binary=join(root,'fixture')
    await writeFile(binary,'#!/usr/bin/env node\nconsole.log(JSON.stringify({args:process.argv.slice(2),config:process.env.CREWSHIP_CONFIG,server:process.env.CREWSHIP_SERVER,cache:process.env.CREWSHIP_NO_SLUG_CACHE}))\n',{mode:0o700})
    const c={...config(),binary,workspace:'workspace-id'}
    const result=JSON.parse(await cli(c,'ADMIN',['doc','read','literal-$(touch nope)']))
    assert.deepEqual(result.args,['--server',c.server,'--workspace',c.workspace,'doc','read','literal-$(touch nope)'])
    assert.equal(result.config,c.identities.ADMIN.cliConfig)
    assert.equal(result.cache,'1');assert.equal(result.server,undefined)
    await assert.rejects(()=>cli({...c,binary:join(root,'absent')},'MEMBER',[]))
  } finally {await rm(root,{recursive:true,force:true})}
})

test('invalid adapter status is a failure rather than skip evidence',async()=>{
  const result=await runContract(config(),{agentCreate:async()=>({status:'skipped'})})
  assert.equal(result.status,'failed')
  assert.equal(result.steps[0].status,'failed')
})

test('fixture CLI writes private incomplete report without browser or provider',async()=>{
  const {mkdtemp,writeFile,readFile,stat,rm}=await import('node:fs/promises')
  const {tmpdir}=await import('node:os')
  const {join,resolve}=await import('node:path')
  const {execFile}=await import('node:child_process')
  const {promisify}=await import('node:util')
  const root=await mkdtemp(join(tmpdir(),'artifact-contract-entry-'))
  let output
  try {
    const input=join(root,'config.json')
    await writeFile(input,JSON.stringify(config()),{mode:0o600})
    let failure
    try {await promisify(execFile)(process.execPath,[resolve('e2e/artifacts-contract/run.mjs'),input])}
    catch(error){failure=error}
    assert.equal(failure?.code,2,'incomplete is not a zero exit pass')
    output=failure.stdout.match(/Artifacts: (.+)/)?.[1]
    assert.ok(output)
    const report=JSON.parse(await readFile(join(output,'report.json'),'utf8'))
    assert.equal(report.steps.length,7)
    assert.ok(report.steps.every(s=>s.status==='incomplete'))
    assert.match(report.steps[0].reason,/no provider or agent was invoked/)
    assert.equal(report.liveAcceptance,false)
    assert.equal((await stat(output)).mode & 0o777,0o700)
    assert.equal((await stat(join(output,'report.json'))).mode & 0o777,0o600)
  } finally {
    await rm(root,{recursive:true,force:true})
    if(output) await rm(output,{recursive:true,force:true})
  }
})
