import assert from 'node:assert/strict'
export const STEPS = Object.freeze(['agentCreate','openChatPanel','iterateMessage','humanEditVersions','explicitSharing','gallery','export'])
export function validateConfig(c) {
  assert.ok(['deterministic-fixture','manual-sonnet-5.5'].includes(c.variant),'select an evidence variant')
  if(c.variant==='manual-sonnet-5.5') assert.equal(c.providerModel,'Sonnet 5.5')
  assert.equal(c.dedicatedTarget,true,'dedicated test target must be explicit')
  const target=new URL(c.server)
  assert.ok(['http:','https:'].includes(target.protocol))
  assert.ok(!target.username && !target.password && !target.search && !target.hash)
  assert.notEqual(target.hostname,'192.168.1.202','production forbidden')
  assert.ok(typeof c.type==='string' && c.type.length>0)
  for(const role of ['OWNER','ADMIN','MEMBER']) {
    const identity=c.identities?.[role]
    for(const field of ['userId','cliConfig','storageState']) assert.ok(typeof identity?.[field]==='string' && identity[field].length>0,`${role} needs ${field}`)
  }
  for(const field of ['userId','cliConfig','storageState']) assert.equal(new Set(Object.values(c.identities).map(i=>i[field])).size,3,`three distinct ${field} values required`)
}
export async function runContract(config,adapter) {
  validateConfig(config)
  const steps=[]
  let failed=false
  for(const id of STEPS) {
    let result={status:'incomplete',reason:failed?'prior operation failed':'operation not implemented for this type'}
    if(!failed && typeof adapter[id]==='function') {
      try {
        result=await adapter[id]()
        assert.ok(['passed','incomplete','failed'].includes(result.status))
        if(result.status==='passed') assert.ok(Array.isArray(result.checks) && result.checks.length>0 && result.checks.every(c=>typeof c==='string' && c.length>0),'pass requires executed checks')
        else assert.ok(typeof result.reason==='string' && result.reason.length>0)
      } catch {result={status:'failed',reason:'adapter operation failed'}}
    }
    failed ||= result.status==='failed'
    steps.push({id,...result})
  }
  const status=failed?'failed':steps.every(s=>s.status==='passed')?'passed':'incomplete'
  return {schema:1,type:config.type,variant:config.variant,status,
    // Provider evidence must be independently inspected; a variant name is not attestation.
    liveAcceptance:false,steps}
}
